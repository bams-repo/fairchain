# Mainnet Consensus Audit - Confirmed Critical Findings

Date: 2026-03-12
Scope: Consensus integrity review focused on block acceptance, fork-choice, reorg behavior, and deterministic validation.
Status: This write-up includes only issues that can positively cause consensus divergence.

---

## Finding 1: Mainnet Genesis Is Runtime-Derived Instead of Protocol-Pinned

Severity: Critical
Type: Chain identity / consensus root mismatch

### Summary

`mainnet` does not ship with a fixed `GenesisBlock` and `GenesisHash` in `internal/params/networks.go`.  
At startup, `cmd/node/main.go` calls `initNetworkGenesis`, which mines and sets genesis if the hash is zero.

In consensus systems, genesis is a hard constant. If genesis is derived at runtime, chain identity depends on implementation details and startup behavior instead of protocol constants. That is a consensus break vector.

### Where this happens

- `internal/params/networks.go`
  - `Mainnet` has no hardcoded `GenesisBlock` and `GenesisHash` values.
- `cmd/node/main.go`
  - `initNetworkGenesis(p *fcparams.ChainParams)`:
    - Returns early only if `p.GenesisHash` is already set.
    - Otherwise builds a genesis template and calls `pow.MineGenesis(&block)`.
    - Computes hash and mutates params via `fcparams.InitGenesis(p, block, hash)`.

### Why this can split consensus

Mainnet nodes must all agree on the exact same genesis header and hash before any other rule matters.

When genesis is mined at runtime:

1. Different implementations (or future refactors) can produce a different mined nonce/hash for the same template.
2. Startup behavior can alter genesis derivation path (for example, template changes, timestamp source changes, mining loop changes, or changed defaults).
3. Any node with a different genesis hash forms a different chain from height 0 and cannot converge with canonical peers.

This is not just operational fragility; it is a consensus identity failure.

### Practical failure modes

- A release changes genesis template construction logic and new nodes bootstrap to a different genesis than existing nodes.
- A non-Go implementation follows the written protocol but does not mirror this runtime-mining behavior byte-for-byte and lands on a different genesis.
- Tooling/tests pass locally because all nodes in that test group share the same binary, while cross-client interoperability fails at network level.

### Required remediation

1. Pin mainnet genesis as immutable constants in params:
   - Set `Mainnet.GenesisBlock` and `Mainnet.GenesisHash` in `internal/params/networks.go`.
2. Remove runtime genesis mining for mainnet startup path:
   - `initNetworkGenesis` should not mine when `network == mainnet`.
3. Treat genesis mismatch as fatal:
   - On startup, verify computed hash of hardcoded genesis equals hardcoded `GenesisHash`; abort otherwise.
4. Keep genesis generation only as an offline tool:
   - `cmd/genesis` should be the place where genesis is mined/prepared, not `cmd/node`.

### Suggested tests

- Test that `params.Mainnet.GenesisHash` is non-zero and stable.
- Test that hashing `params.Mainnet.GenesisBlock.Header` equals `params.Mainnet.GenesisHash`.
- Test that `cmd/node` never attempts runtime genesis mining for mainnet.

---

## Finding 2: Non-Canonical Varints Accepted, Then Canonicalized Before Consensus Hashing/Size Checks

Severity: High
Type: Encoding malleability at consensus boundary

### Summary

Deserializer logic accepts non-minimal varint encodings in transactions/blocks.  
Later, consensus-critical operations (transaction hash and block size checks) operate on reserialized canonical bytes.

This creates an ambiguity where two wire encodings of the "same parsed object" are treated as equivalent by this implementation, while another implementation that enforces minimal varints (or hashes raw accepted bytes) can reject or derive different identifiers. That is a direct cross-implementation consensus split risk.

### Where this happens

- `internal/types/transaction.go`
  - `ReadVarInt` accepts `0xFD`, `0xFE`, `0xFF` forms without minimality validation.
- `internal/crypto/hash.go`
  - `HashTransaction` hashes `tx.SerializeToBytes()` (canonical re-encoding).
- `internal/consensus/validation.go`
  - `ValidateBlockStructure` computes size by `block.SerializeToBytes()` (canonical re-encoding), not original wire bytes.

### Why this can split consensus

Consensus systems must define one accepted encoding (or fully define equivalence classes and hashing semantics).

Current behavior:

1. Node accepts non-canonical varint encodings.
2. Node reserializes to canonical form for txid and block size checks.
3. Canonicalized view is used for consensus decisions.

If peer/client B enforces canonical varints at parse time, B rejects a block/tx accepted by this node.  
If peer/client C hashes/validates using raw accepted encoding, C can compute different tx identifiers than this node for semantically identical payloads.

Either path yields deterministic divergence.

### Example class of problematic payload

- A count or length that should be 1-byte varint (value `< 0xFD`) but is encoded using `0xFD xx xx`.
- This node parses it, normalizes it on serialize, and proceeds.
- A stricter node rejects immediately as non-canonical.

### Required remediation

1. Enforce minimal varint encodings at decode time:
   - In `ReadVarInt`, reject:
     - `0xFD` values `< 0xFD`
     - `0xFE` values `<= 0xFFFF`
     - `0xFF` values `<= 0xFFFFFFFF`
2. Ensure consensus size checks are based on canonical rules only if canonical encoding is strictly enforced at parse boundary.
3. Add regression tests for non-canonical varint rejection in:
   - tx input/output counts
   - script length prefixes
   - block tx count

### Suggested tests

- Unit tests for `ReadVarInt` minimality failures.
- Block decode tests with non-canonical tx count varints (must fail).
- Transaction decode tests with non-canonical script length varints (must fail).

---

## Finding 3: Reorg Writes Undo Data Twice, Corrupting Stored Offsets

Severity: High
Type: Storage corruption / reorg failure

### Summary

The `reorg()` method in `internal/chain/chain.go` writes undo data for each new-chain block twice: once during the connect loop and again in a separate persist loop. The second write appends duplicate undo data at new offsets, and only the second offsets are persisted to the block index. The first write's data is orphaned in the rev file.

### Where this happens

- `internal/chain/chain.go`, `reorg()` method

**First write (connect loop, ~lines 588-601):**
```
for i := len(newChain) - 1; i >= 0; i-- {
    // ...
    undoBytes := utxo.SerializeUndoData(undoData)
    undoOffset, undoSize, err := c.store.WriteUndo(rec.FileNum, undoBytes)
    rec.UndoFile = rec.FileNum
    rec.UndoPos = undoOffset
    rec.UndoSize = undoSize
    rec.Status |= store.StatusHaveUndo | store.StatusValidTx
    c.heightByHash[blockHash] = h
    c.hashByHeight[h] = blockHash
}
```

This loop updates the in-memory `rec` but never calls `PutBlockIndex` to persist it.

**Second write (persist loop, ~lines 611-633):**
```
for i := len(newChain) - 1; i >= 0; i-- {
    blockHash := newChain[i]
    rec, err := c.store.GetBlockIndex(blockHash)  // reads STALE rec from disk
    // ...
    undoBytes := utxo.SerializeUndoData(undoData)
    undoOffset, undoSize, err := c.store.WriteUndo(rec.FileNum, undoBytes)
    rec.UndoFile = rec.FileNum
    rec.UndoPos = undoOffset
    rec.UndoSize = undoSize
    rec.Status |= store.StatusHaveUndo | store.StatusValidTx
    c.store.PutBlockIndex(blockHash, rec)  // persists second offsets
}
```

This loop re-reads `rec` from the store (which has the pre-reorg values since the first loop never persisted), then writes undo data again to new offsets.

### Why this can break consensus

1. The first write's undo data is orphaned in the rev file, wasting space and shifting subsequent write positions.
2. If the rev file is shared across multiple blocks (which it is -- blocks are grouped into numbered files), the orphaned data from the first write shifts all subsequent offsets within that file.
3. After a reorg, if those blocks later need to be disconnected (a deeper reorg), the node reads undo data from the persisted offsets. If the rev file layout was corrupted by the double-write, the read returns wrong data, causing UTXO rollback to produce an incorrect state.
4. Two nodes that experienced the same reorg but with different rev file states (e.g., one restarted between reorgs) will compute different UTXO sets after disconnect, splitting consensus.

### Required remediation

Remove the first write loop entirely. The connect loop should only compute undo data and store it in the `undoByHash` map. The persist loop should be the sole writer of undo data and block index updates. Alternatively, merge both loops into one that writes undo, connects UTXOs, and persists the index entry in a single pass.

### Suggested tests

- Integration test: trigger a reorg, then trigger a deeper reorg that disconnects the first reorg's blocks. Verify UTXO set matches expected state.
- Unit test: verify that after `reorg()`, each block's stored `UndoPos`/`UndoSize` in the block index correctly points to valid, deserializable undo data in the rev file.

---

## Finding 4: Duplicate CalcWork / compactToBig Implementations Diverge on Edge Cases

Severity: Medium
Type: Work calculation inconsistency between startup and runtime

### Summary

Two independent implementations of compact-to-big-integer conversion and work calculation exist in the codebase. They produce different results for edge-case inputs (negative-bit targets), meaning chain work computed at startup can differ from chain work computed during block processing.

### Where this happens

- `internal/crypto/target.go` — `CompactToBig` (lines 15-33) and `CalcWork` (lines 96-105)
  - Handles the negative sign bit (`0x00800000`).
  - Used by consensus validation, PoW checks, and difficulty retargeting.
  - Used by `ProcessBlock` (chain.go line 348) for new block work.

- `internal/store/store.go` — `compactToBig` (lines 74-84) and `CalcWork` (lines 63-72)
  - Does **not** handle the negative sign bit.
  - Used during `Chain.Init()` (chain.go line 103) to rebuild `tipWork` from the block index on startup.

### Why this can break consensus

On startup, `Chain.Init()` walks backwards from the tip and accumulates work using `store.CalcWork`. During runtime, `ProcessBlock` computes block work using `crypto.CalcWork`. If any block in the chain has a `Bits` value where the negative bit matters (compact value with bit 23 set in the mantissa), the two functions return different work values.

This means `tipWork` after a restart can differ from `tipWork` computed incrementally during runtime. Since chain selection (`ProcessBlock` line 404) compares `newWork` against `c.tipWork`, a restart can change which chain the node considers best, causing it to accept or reject blocks differently than before the restart.

While normal difficulty values don't set the negative bit, the code does not validate that `Bits` values in accepted blocks have the negative bit clear. A carefully crafted block header could exploit this.

### Required remediation

1. Remove `store.compactToBig` and `store.CalcWork` entirely.
2. Have `store.go` import and use `crypto.CompactToBig` and `crypto.CalcWork`.
3. Alternatively, add a validation rule rejecting blocks where `Bits & 0x00800000 != 0`.

### Suggested tests

- Unit test: verify `store.CalcWork(bits)` equals `crypto.CalcWork(bits)` for all bits values encountered in the chain, including edge cases with bit 23 set.
- Startup test: verify `tipWork` after restart matches `tipWork` before shutdown.

---

## Finding 5: Side-Chain Blocks Corrupt Mempool State via P2P Handler

Severity: Medium
Type: Mempool state divergence across nodes

### Summary

When a block is accepted as a side-chain block (`ErrSideChain`), the P2P `handleBlock` method still removes the block's transactions from the mempool and updates the mempool's tip height to the side-chain block's height. This corrupts the mempool's view of the chain state.

### Where this happens

- `internal/p2p/manager.go`, `handleBlock` method (lines 603-653)

```
height, err := m.chain.ProcessBlock(block)
if err != nil {
    if errors.Is(err, chain.ErrSideChain) {
        logging.L.Debug("block stored as side chain", ...)
        // falls through — does NOT return
    } else {
        m.seenBlocks.Delete(blockHash)
        if !errors.Is(err, chain.ErrOrphanBlock) {
            logging.L.Warn("block rejected", ...)
        }
        return
    }
}

// This code runs for BOTH main-chain AND side-chain blocks:
var confirmedHashes []types.Hash
for _, tx := range block.Transactions {
    txHash, hashErr := crypto.HashTransaction(&tx)
    if hashErr == nil {
        confirmedHashes = append(confirmedHashes, txHash)
    }
}
m.mempool.RemoveTxs(confirmedHashes)
m.mempool.SetTipHeight(height)
```

### Why this can break consensus

1. **Transaction removal:** Transactions from a side-chain block are removed from the mempool even though they are not confirmed on the main chain. These transactions should remain available for inclusion in future main-chain blocks. Nodes that saw the side-chain block will have different mempool contents than nodes that didn't.

2. **Tip height corruption:** `SetTipHeight(height)` is called with the side-chain block's height, which may be higher than the actual main-chain tip. This corrupts coinbase maturity checks in `ValidateSingleTransaction` (which uses `tipHeight + 1` as the spend height). Transactions that should be accepted into the mempool may be rejected (or vice versa) depending on which side-chain blocks a node has seen.

3. **Block template divergence:** Miners on different nodes will construct different block templates because their mempools diverge. While this doesn't directly cause a consensus split (blocks are validated independently), it degrades network consistency and can cause valid transactions to be delayed or dropped.

### Required remediation

Add a `return` after the side-chain log line so that mempool updates are skipped for non-main-chain blocks:

```go
if errors.Is(err, chain.ErrSideChain) {
    logging.L.Debug("block stored as side chain", ...)
    return  // do not update mempool for side-chain blocks
}
```

### Suggested tests

- Test that receiving a side-chain block does not remove its transactions from the mempool.
- Test that `mempool.tipHeight` is not updated when a side-chain block is processed.

---

## Finding 6: workForParentChain Silently Returns Partial Work on Disk Errors

Severity: Medium
Type: Incorrect chain selection under storage faults

### Summary

`workForParentChain` computes cumulative chain work by walking backwards from a given block to genesis, loading each header from disk. If any header load fails (disk error, corruption, missing data), the function silently breaks out of the loop and returns whatever partial work it has accumulated so far.

### Where this happens

- `internal/chain/chain.go`, `workForParentChain` (lines 777-792)

```
func (c *Chain) workForParentChain(blockHash types.Hash) *big.Int {
    work := big.NewInt(0)
    current := blockHash
    for {
        header, err := c.store.GetHeader(current)
        if err != nil {
            break  // silent failure — returns partial work
        }
        work.Add(work, crypto.CalcWork(header.Bits))
        if header.PrevBlock.IsZero() {
            break
        }
        current = header.PrevBlock
    }
    return work
}
```

This function is called during `ProcessBlock` (line 349) and orphan processing (line 715) to determine whether a new block's chain has more work than the current tip.

### Why this can break consensus

1. If a transient disk read error occurs (e.g., I/O timeout, file descriptor exhaustion, brief storage unavailability), the function returns a work value that is too low.
2. This causes the chain-selection comparison (`newWork.Cmp(c.tipWork)`) to incorrectly conclude the new chain has less work than the current tip.
3. A block that should trigger a reorg (because its chain has more total work) is instead stored as a side-chain block and never activated.
4. After a restart (where `tipWork` is recomputed cleanly from the block index), the node may now see the side chain as having more work and reorg to it — but other nodes that didn't experience the disk error already reorged when the block arrived.
5. This creates a window where nodes disagree on the active chain based on transient storage conditions.

### Required remediation

1. Return an error from `workForParentChain` instead of silently returning partial work.
2. Propagate the error to `ProcessBlock` so the block is rejected with a clear error rather than silently misclassified.
3. Consider caching `ChainWork` in the `DiskBlockIndex` (it's already stored there) and reading it directly from the block index instead of recomputing by walking the chain.

### Suggested tests

- Unit test: inject a storage error mid-chain and verify that `workForParentChain` returns an error rather than partial work.
- Integration test: verify that a block is not silently demoted to side-chain status when a transient storage error occurs during work calculation.

---

## Priority and Rollout Guidance

1. **Fix Finding 1 first** (genesis pinning) — chain identity critical, blocks all other work.
2. **Fix Finding 3 next** (double undo write) — high severity, will cause data corruption on first mainnet reorg.
3. **Fix Finding 2** (varint canonicalization) — consensus-hardening before any multi-client/mainnet rollout.
4. **Fix Finding 4** (duplicate CalcWork) — unify implementations to prevent startup/runtime divergence.
5. **Fix Finding 5** (side-chain mempool corruption) — straightforward one-line fix with immediate benefit.
6. **Fix Finding 6** (silent partial work) — requires error propagation refactor but prevents chain-selection bugs under storage faults.
7. After all fixes, run interoperability tests against at least one independent parser/validator implementation.
