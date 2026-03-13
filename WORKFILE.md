# WORKFILE — Fairchain Blockchain POC

## 1. Project Purpose

Fairchain is a Go blockchain proof-of-concept designed as a modular foundation for fairness-oriented consensus research. The immediate goal is a robust baseline chain with deterministic behavior and clear upgrade boundaries, so future agents can swap consensus components (identity-bound tickets, VRF eligibility, sequential memory-hard proofs) without rewriting the node.

The baseline uses Nakamoto-style proof-of-work. All consensus-critical code is explicit, auditable, and free of hidden state.

## 2. Current Architecture Overview

Single-binary node architecture with clean package separation:

```
cmd/           → Entrypoints (node, genesis tool, CLI, adversary tool)
internal/      → All library code (not importable externally)
  types/       → Consensus structs, canonical serialization
  crypto/      → Hashing, merkle, target math
  params/      → Chain parameters, genesis config, network definitions
  consensus/   → Engine interface, structural validation, timestamp rules, tx validation
  consensus/pow/ → Baseline PoW engine
  chain/       → Blockchain state manager (UTXO-aware)
  store/       → Abstract storage + bbolt implementation (blocks, UTXOs, undo data, peers)
  utxo/        → UTXO set, entries, connect/disconnect blocks, undo data serialization
  mempool/     → Transaction pool (UTXO-validated, fee-rate priority, double-spend detection)
  miner/       → Block template building, mining loop (fee-inclusive coinbase)
  p2p/         → Peer manager, gossip, sync
  p2p/discovery/ → Peer discovery
  protocol/    → Wire message definitions, binary encoding
  rpc/         → Local HTTP JSON API (Bitcoin Core-compatible endpoints)
  config/      → Config loading
  logging/     → Structured logging (log/slog wrapper)
  metrics/     → Atomic counters for node activity
  version/     → Centralized version constants
  util/        → Non-consensus helpers
```

**Key design principle**: Consensus code has zero knowledge of networking. Network code calls into consensus only through the `consensus.Engine` interface and the chain manager.

## 3. Directory Tree

```
fairchain/
├── cmd/
│   ├── node/main.go          # Full node entrypoint
│   ├── genesis/main.go        # Genesis mining tool
│   └── cli/main.go            # CLI query tool
├── internal/
│   ├── types/
│   │   ├── hash.go            # Hash type, comparison, encoding
│   │   ├── transaction.go     # Transaction, TxInput, TxOutput, OutPoint, VarInt
│   │   ├── block.go           # BlockHeader, Block
│   │   └── types_test.go      # Serialization roundtrip tests
│   ├── crypto/
│   │   ├── hash.go            # DoubleSHA256, HashBlockHeader, HashTransaction
│   │   ├── merkle.go          # MerkleRoot, ComputeMerkleRoot
│   │   ├── target.go          # CompactToBig, BigToCompact, CalcWork, ValidatePoW
│   │   └── crypto_test.go     # Hash, merkle, bits, work tests
│   ├── params/
│   │   ├── params.go          # ChainParams struct, CalcSubsidy
│   │   ├── genesis.go         # GenesisConfig, BuildGenesisBlock
│   │   └── networks.go        # Mainnet, Testnet, Regtest definitions
│   ├── consensus/
│   │   ├── engine.go          # consensus.Engine interface
│   │   ├── validation.go      # ValidateBlockStructure, ValidateHeaderTimestamp
│   │   ├── validation_test.go # Block validation tests
│   │   └── pow/
│   │       ├── engine.go      # PoW engine: validate, retarget, seal, mine genesis
│   │       └── pow_test.go    # PoW-specific tests
│   ├── chain/
│   │   ├── chain.go           # Chain state manager
│   │   └── chain_test.go      # Chain integration tests
│   ├── store/
│   │   ├── store.go           # BlockStore, PeerStore interfaces
│   │   ├── bolt.go            # bbolt implementation
│   │   └── store_test.go      # Storage roundtrip tests
│   ├── mempool/
│   │   └── mempool.go         # Thread-safe mempool
│   ├── miner/
│   │   └── miner.go           # Mining loop, template builder
│   ├── p2p/
│   │   ├── peer.go            # Peer connection wrapper
│   │   ├── manager.go         # P2P manager, handshake, message routing
│   │   └── discovery/
│   │       └── discovery.go   # Peer discovery
│   ├── protocol/
│   │   ├── messages.go        # Wire message types and encoding
│   │   ├── checksum.go        # Message checksum helper
│   │   └── protocol_test.go   # Message encode/decode tests
│   ├── utxo/
│   │   ├── utxo.go            # UTXO set, entries, connect/disconnect, undo data
│   │   └── utxo_test.go       # UTXO serialization, set operations, undo roundtrip tests
│   ├── rpc/
│   │   └── server.go          # HTTP JSON API (14 endpoints)
│   ├── config/
│   │   └── config.go          # Config struct, loading, defaults
│   └── util/
│       └── util.go            # Non-consensus helpers
├── go.mod
├── go.sum
├── Makefile
├── config.sample.json
├── .gitignore
├── README.md
├── TODO.md
└── WORKFILE.md
```

## 4. Consensus-Critical Invariants

These rules MUST be preserved by any future changes:

1. **Canonical serialization**: All consensus types use explicit little-endian binary encoding. No reflection, no JSON, no encoding/gob for consensus data.

2. **Deterministic hashing**: Block header hash = DoubleSHA256(80-byte canonical header). Transaction hash = DoubleSHA256(canonical tx bytes). These must produce identical results on all machines.

3. **Merkle root**: Bitcoin-style binary merkle tree. Odd levels duplicate the last hash. Order is transaction order in the block.

4. **Target comparison**: Hash is treated as a 256-bit little-endian integer. Block is valid if hash ≤ target.

5. **Compact bits**: Uses Bitcoin nBits encoding. `CompactToBig` and `BigToCompact` must round-trip correctly.

6. **Subsidy**: `subsidy = InitialSubsidy >> (height / SubsidyHalvingInterval)`. Pure integer arithmetic, no floats.

7. **Retargeting**: `newTarget = oldTarget * actualTimespan / targetTimespan`, clamped to [1/4, 4x]. Integer arithmetic only.

8. **Coinbase**: First transaction in every block must be coinbase (zero-hash outpoint, index 0xFFFFFFFF). No other transaction may be coinbase.

9. **No map iteration in consensus**: Any code that iterates over maps in a consensus-critical path must sort keys first.

10. **No float math in consensus**: All target, work, subsidy, and timing calculations use integer arithmetic.

11. **Explicit endianness**: All multi-byte values use little-endian encoding in wire/storage format, except height-to-bytes in the store index (big-endian for lexicographic ordering).

## 5. Network Protocol Overview

Wire messages use a 24-byte header: magic(4) + command(12, null-padded) + length(4 LE) + checksum(4, first 4 bytes of DoubleSHA256 of payload).

| Command | Purpose |
|---------|---------|
| version | Initial handshake, exchange height/version/nonce |
| verack | Handshake acknowledgment |
| ping/pong | Liveness check with nonce echo |
| inv | Announce inventory (blocks/txs) by hash |
| getdata | Request specific inventory items |
| block | Full block payload |
| tx | Full transaction payload |
| getblocks | Request block hashes from locator |
| addr | Gossip peer addresses |

Self-connection detection uses a random nonce in the version message.

## 6. Current Chain Params

### Regtest (primary development network)
- Magic: `FA 1C C0 FF`
- Port: 19444
- Block spacing: 1 second
- Initial bits: `0x207fffff` (very easy)
- No retarget
- Subsidy: 50 coins (5,000,000,000 units)
- Halving interval: 150 blocks
- Coinbase maturity: 1 block

### Testnet
- Magic: `FA 1C C0 02`
- Port: 19334
- Block spacing: 1 minute
- Initial bits: `0x1e0fffff`

### Mainnet
- Magic: `FA 1C C0 01`
- Port: 19333
- Block spacing: 2 minutes
- Initial bits: `0x1d00ffff`

## 7. Genesis Block Details

Genesis blocks are mined at node startup if not already cached. The mining uses a fixed timestamp (1741651200 = 2025-03-11T00:00:00Z) and fixed coinbase message for reproducibility.

Genesis config structure:
- NetworkName, CoinbaseMessage, Timestamp, Bits, Version, Reward, RewardScript
- `BuildGenesisBlock()` constructs the block with zero nonce
- `MineGenesis()` iterates nonce until hash ≤ target
- Same inputs always produce the same genesis block

## 8. What Is Fully Implemented

- All core types with canonical serialization and deserialization
- Double-SHA256 hashing for blocks and transactions
- Merkle root computation
- Compact bits ↔ target conversion
- Chainwork calculation
- PoW validation
- Genesis block building and mining
- Chain params for 3 networks
- Subsidy schedule
- Block structure validation (7 rules)
- Timestamp validation (2 modes)
- Difficulty retargeting with clamping
- Pluggable consensus.Engine interface
- Baseline PoW consensus engine
- Chain state manager with orphan pool and reorg
- bbolt persistent storage (blocks, headers, chain state, UTXOs, undo data, peers)
- **UTXO set** (in-memory with bbolt persistence, connect/disconnect per block)
- **Transaction input validation**: UTXO existence, value checks, coinbase maturity
- **Fee calculation**: input sum - output sum, coinbase capped at subsidy + fees
- **UTXO-aware reorg**: disconnect old chain blocks (restore spent UTXOs), reconnect new chain
- **Block undo data**: serialized per-block for rollback support
- Thread-safe mempool with **UTXO validation, double-spend detection, fee-rate priority, eviction**
- Mining loop with template building, **fee-inclusive coinbase**
- P2P peer management with handshake
- Inventory-based gossip protocol
- Block and transaction propagation
- Initial block sync
- Peer address gossip and persistence
- Local HTTP JSON RPC API (14 endpoints including /metrics)
  - Bitcoin Core-style: gettxout, gettxoutsetinfo, getrawmempool, getmempoolentry
- CLI query tool
- Adversarial block generator (8 attack types)
- 60+ passing unit tests + 9 fuzz targets
- 16-phase chaos test (network chaos + adversarial attacks)
- Structured logging (log/slog) with configurable levels
- Metrics skeleton: atomic counters for blocks, peers, reorgs, orphans
- Graceful shutdown with ordered teardown

## 9. What Is Stubbed but Planned

- **Script validation**: `TxInput.SignatureScript` and `TxOutput.PkScript` fields exist but are not interpreted. UTXO validation checks existence and value but not script execution.

- **Headers-first sync**: Current sync uses `getblocks` to request block hashes, then fetches full blocks. The protocol supports a headers-first approach but it's not implemented.

- **Peer scoring**: The peer struct has infrastructure for tracking behavior but no scoring/banning logic.

- **Activation heights**: `ChainParams.ActivationHeights` map exists but no activation logic uses it yet.

## 10. Known Limitations

1. **No script validation**: Transactions are validated for UTXO existence, value, and maturity, but signature/script verification is not implemented. Any transaction with valid UTXO references is accepted.

2. **UTXO set rebuilt on startup**: The in-memory UTXO set is rebuilt by replaying all blocks from genesis. For very long chains this could be slow. Persistent UTXO snapshots would improve startup time.

3. **Genesis mined at startup**: Each network's genesis is mined on first run. For regtest this is instant; for mainnet/testnet it could be slow. Pre-computed genesis hashes should be hardcoded for production.

4. **No wallet**: No key generation, address derivation, or transaction signing.

5. **Single-threaded mining**: The miner uses a single goroutine. No parallel nonce search.

6. **No checkpoints**: No checkpoint validation for fast sync.

7. **Memory-resident chain index**: The height↔hash index is rebuilt from storage on startup. For very long chains this could be slow.

## 11. Consensus Audit — Failure Analysis & Fix Tasks

### 11.1 Audit Summary (2026-03-12)

A 10-node chaos cluster (2 seeds, 8 miners, testnet params: 5s blocks, retarget/20) revealed 4 critical consensus failures during stress testing. All adversarial and UTXO validation tests passed. The failures are in chain selection, persistence, and peer topology.

### 11.2 Confirmed Root Causes

| ID | Bug | Severity | File | Lines |
|----|-----|----------|------|-------|
| RC-1 | `getAncestorUnsafe` returns main-chain blocks when validating side-chain headers at retarget boundaries | CRITICAL | `internal/chain/chain.go` | 703-713 |
| RC-2 | `seenBlocks` cache in P2P prevents re-evaluation of rejected blocks | CRITICAL | `internal/p2p/manager.go` | 604 |
| RC-3 | `DiskBlockIndex` written before reorg validation, poisoning `HasBlock` | HIGH | `internal/chain/chain.go` | 383 |
| RC-4 | No proactive addr gossip; star topology (miners only connect to seeds) | HIGH | `internal/p2p/manager.go` | 215-273 |
| RC-5 | Block index writes not synced to disk (`nil` write options) | MEDIUM | `internal/store/blockindex.go` | 141 |
| RC-6 | `workForParentChain` walks entire chain O(n) instead of using stored ChainWork | MEDIUM | `internal/chain/chain.go` | 686-701 |
| RC-7 | `syncLoop` only syncs from peer's handshake-time height, never updates | MEDIUM | `internal/p2p/manager.go` | 710-734 |
| RC-8 | Reorg disconnects old chain before validating new chain (no rollback on failure) | MEDIUM | `internal/chain/chain.go` | 477-507 |

### 11.3 Fix Tasks

Each task below is a discrete, testable unit of work. Tasks are ordered by dependency.

#### TASK-01: Side-chain-aware ancestor lookup [CRITICAL]
- **File**: `internal/chain/chain.go`
- **What**: Replace `getAncestorUnsafe` with a function that walks the side chain's ancestry via `store.GetBlockIndex` to build a temporary height→hash map for the fork, falling back to main chain below the fork point.
- **Why**: Without this, blocks at retarget boundaries on side chains are always rejected because `CalcNextBits` uses wrong timestamps.
- **Interface**: `getAncestorForBlock(prevBlockHash types.Hash) func(uint32) *types.BlockHeader`
- **Apply at**: `ProcessBlock` lines 314, 318; `processOrphans` lines 607, 613
- **Test**: Create two chains that diverge before a retarget boundary. Verify the shorter chain's node accepts the longer chain's blocks and reorgs.

#### TASK-02: Fix seenBlocks cache to allow re-evaluation [CRITICAL]
- **File**: `internal/p2p/manager.go`
- **What**: Only add to `seenBlocks` after `ProcessBlock` succeeds (including side-chain acceptance). Do not cache blocks that were rejected due to validation failure.
- **Why**: Currently rejected blocks are cached forever, preventing convergence.
- **Test**: Submit a block that is initially rejected (e.g., orphan), then submit its parent. Verify the orphan is re-evaluated and accepted.

#### TASK-03: Defer DiskBlockIndex write until after validation [HIGH]
- **File**: `internal/chain/chain.go`
- **What**: Move `PutBlockIndex` at line 383 to after the reorg succeeds. For the side-chain-stored-but-not-reorged path (line 394), write the index entry only after confirming the block is structurally valid.
- **Why**: Writing the index before validation causes `HasBlock` to return true for invalid blocks.
- **Test**: Submit an invalid side-chain block. Verify `HasBlock` returns false after rejection.

#### TASK-04: Implement addr gossip protocol [HIGH]
- **File**: `internal/p2p/manager.go`
- **What**: Add `addrBroadcastLoop` that sends known peer addresses to all connected peers every 30s. Add `getaddr` handler. On new connection, exchange addresses.
- **Why**: Without this, miners never discover each other, creating a fragile star topology.
- **Test**: Start 5 nodes where only node0 knows seeds. Verify all 5 nodes discover each other within 60s.

#### TASK-05: Use stored ChainWork instead of recomputing [MEDIUM]
- **File**: `internal/chain/chain.go`
- **What**: Replace `workForParentChain` body with a single `GetBlockIndex` call to read `rec.ChainWork`.
- **Why**: O(1) instead of O(n). Also eliminates potential for inconsistency between stored and computed work.
- **Test**: Verify chainwork comparison produces same results before and after change.

#### TASK-06: Add sync writes to block index [MEDIUM]
- **File**: `internal/store/blockindex.go`
- **What**: Pass `&opt.WriteOptions{Sync: true}` to all `db.Put` calls.
- **Why**: Without sync, a crash can lose block index entries that were acknowledged as written.
- **Test**: Kill node during block acceptance. Verify block index is consistent on restart.

#### TASK-07: Validate new chain before disconnecting old chain during reorg [MEDIUM]
- **File**: `internal/chain/chain.go`
- **What**: Clone the UTXO set, validate all new-chain blocks against the clone, then proceed with disconnect+connect only if all validations pass.
- **Why**: Currently a failed reorg leaves the node in a broken state with the old chain disconnected but the new chain not connected.
- **Test**: Trigger a reorg where the new chain has an invalid transaction. Verify the node remains on the old chain.

#### TASK-08: Update peer heights dynamically [MEDIUM]
- **File**: `internal/p2p/manager.go`
- **What**: After accepting a block from a peer, update that peer's known height. In `syncLoop`, use the dynamically updated height instead of the handshake-time `StartHeight`.
- **Why**: Current sync only works for initial block download. After handshake, the node never learns that peers have grown taller.
- **Test**: Connect two nodes. Mine 10 blocks on node A. Verify node B syncs all 10 blocks.

#### TASK-09: Add orphan expiry and parent request [LOW]
- **File**: `internal/chain/chain.go`, `internal/p2p/manager.go`
- **What**: Add timestamps to orphan entries, evict after 60s. When a block is orphaned, request its parent from the source peer.
- **Why**: Prevents orphan pool exhaustion and actively resolves orphans.
- **Test**: Submit an orphan block. Verify the node requests the parent from the peer.

### 11.4 Implementation Order

```
Phase 1: TASK-04 (addr gossip) — can be done independently
Phase 2: TASK-01 (ancestor lookup) + TASK-05 (chainwork) — core consensus fix
Phase 3: TASK-09 (orphan handling) — depends on TASK-01
Phase 4: TASK-06 (sync writes) + TASK-03 (deferred index write) — persistence fixes
Phase 5: TASK-07 (safe reorg) + TASK-08 (dynamic peer height) — hardening
Phase 6: TASK-02 (seenBlocks fix) — depends on TASK-01 and TASK-03
```

### 11.5 Testing Checklist

- [ ] Two-partition test: partitions mine past retarget boundary, reconnect, verify convergence
- [ ] Deep reorg test: 20+ block reorg across retarget boundary
- [ ] Restart persistence: kill all nodes, restart, verify same tip and UTXO set
- [ ] Peer mesh: verify miners discover each other (>4 peers per miner)
- [ ] Orphan resolution: submit blocks out of order, verify all accepted
- [ ] Equal-work fork: submit two blocks at same height with equal work, verify deterministic resolution
- [ ] Crash recovery: kill node during reorg, verify consistent state on restart
- [ ] UTXO consistency: verify all nodes at same height have identical UTXO set

### 11.6 Regression Tests

Add the following unit tests to `internal/chain/chain_test.go`:

1. `TestSideChainAncestorLookup` — verify ancestor lookup returns side-chain blocks, not main-chain blocks
2. `TestReorgAcrossRetargetBoundary` — verify reorg succeeds when fork spans a retarget boundary
3. `TestReorgFailureRollback` — verify node stays on old chain if new chain has invalid tx
4. `TestChainworkFromIndex` — verify chainwork lookup matches recomputed chainwork
5. `TestPersistenceAfterReorg` — verify chain tip and UTXO set survive restart after reorg

Add the following to `internal/p2p/manager_test.go`:

6. `TestAddrGossip` — verify addr messages are sent and received
7. `TestSeenBlocksEviction` — verify rejected blocks are not permanently cached

### 11.7 Chaos Test Validation Steps

After implementing all fixes, re-run the full chaos test:

```bash
python scripts/chaos_test.py
```

Expected results:
- Phase D (deep reorg): spread ≤ 3, all nodes converge
- Phase F (height index): 0 mismatches
- Phase H (restart): all nodes preserve chain state
- All peer counts ≥ 4 for miners
- Final consensus check: spread ≤ 2

## 12. Previous Task History

### Completed (Phase 4)
1. ~~Multi-node simulation harness~~ → 16-phase chaos test with 10 nodes + adversarial tool
2. **Pre-compute genesis hashes**: Run the genesis miner for each network and hardcode the results in `networks.go`.
3. ~~Structured logging~~ → Migrated to log/slog with --log-level flag

### Completed (Phase 5)
4. ~~UTXO set~~ → In-memory set backed by bbolt, connect/disconnect per block, undo data for reorgs.
5. ~~Input validation~~ → UTXO existence, value checks, coinbase maturity enforcement.
6. ~~Coinbase maturity~~ → Enforced via `CoinbaseMaturity` param in both block and mempool validation.
7. ~~Fee calculation~~ → input_sum - output_sum, coinbase capped at subsidy + fees, miner collects fees.
8. ~~Double-spend detection~~ → Mempool tracks spent outpoints, rejects conflicts.
9. ~~Fee-rate priority~~ → Mempool orders by fee/byte, eviction removes lowest fee-rate.
10. ~~Bitcoin Core RPC~~ → gettxout, gettxoutsetinfo, getrawmempool, getmempoolentry.

### Next (Phase 6 — Consensus Hardening)
11. **Fix consensus failures** — See section 11.3 above for the 9 fix tasks.
12. **Identity registration tx type**: Add a new transaction version or type for registering a mining identity (pubkey + collateral deposit).
13. **Consensus engine swap**: Implement a second `consensus.Engine` that uses identity-based eligibility instead of PoW.
14. **Epoch seed interface**: Define how epoch seeds are computed from chain history.

## 12. Safe Extension Points

These areas are designed for extension and can be modified without breaking existing consensus:

- **`consensus.Engine` interface**: Add new implementations freely. The chain manager calls through this interface.
- **`ChainParams.ActivationHeights`**: Use this to gate new consensus rules by block height.
- **`Transaction.Version`**: Use new version numbers for new transaction types (identity registration, etc.).
- **`TxOutput.PkScript`**: Currently a raw byte placeholder. Can be extended to support real locking scripts.
- **`TxInput.SignatureScript`**: Currently unused for validation. Can be extended for signature verification.
- **`BlockHeader.Version`**: Use for signaling consensus upgrades.
- **Mempool admission**: Add validation rules in `mempool.AddTx()` without affecting consensus.
- **RPC endpoints**: Add new endpoints to `rpc/server.go` freely.
- **Peer message types**: Add new command strings to the protocol. Unknown commands are logged and ignored.

## 13. Unsafe Areas Where Changes Can Break Consensus

**DO NOT modify these without extreme care:**

- **`types/hash.go`**: Hash comparison order affects PoW validation and chain selection.
- **`types/transaction.go` serialization**: Any change to Serialize/Deserialize changes transaction hashes and merkle roots.
- **`types/block.go` serialization**: Any change to header serialization changes block hashes.
- **`crypto/hash.go`**: DoubleSHA256 is the identity function for blocks and transactions.
- **`crypto/merkle.go`**: Merkle root algorithm must match exactly or blocks will fail validation.
- **`crypto/target.go`**: CompactToBig/BigToCompact must match Bitcoin's nBits encoding or difficulty is wrong.
- **`consensus/validation.go`**: Block structure rules are consensus-critical.
- **`consensus/pow/engine.go` CalcNextBits**: Retarget logic must be identical on all nodes.
- **`params/params.go` CalcSubsidy**: Subsidy calculation is consensus-critical.

- **`utxo/utxo.go` ConnectBlock/DisconnectBlock**: UTXO state transitions must be deterministic and reversible.
- **`consensus/txvalidation.go`**: Transaction input validation and fee calculation are consensus-critical.

**Rule of thumb**: If a change affects the output of `HashBlockHeader`, `HashTransaction`, `ComputeMerkleRoot`, `CompactToBig`, `CalcSubsidy`, `CalcNextBits`, or UTXO connect/disconnect behavior, it is a consensus-breaking change and requires a hard fork.

## 14. Suggested Workflow for Future Agents

1. **Read this WORKFILE first.** Understand the architecture before making changes.
2. **Run `make test`** before and after every change.
3. **Check invariants in section 4** before modifying any consensus code.
4. **Use the `consensus.Engine` interface** for new consensus mechanisms. Don't modify the PoW engine; create a new engine.
5. **Use `ChainParams.ActivationHeights`** to gate new rules by height.
6. **Use new `Transaction.Version` values** for new transaction types.
7. **Keep consensus code free of network concerns.** If you need network info in consensus, pass it as a parameter.
8. **Test determinism explicitly.** Any new consensus function should have a test that runs it twice with the same inputs and asserts identical output.
9. **Don't use maps in consensus-critical iteration** without sorting keys first.
10. **Don't use floats in consensus code.** Ever.

## 15. Commands

```bash
# Build all binaries
make build

# Run all tests
make test

# Mine a genesis block (informational output)
make mine-genesis

# Run a regtest node with mining
make run-regtest

# Run a second regtest node
make run-regtest2

# Query node status
make status

# Or directly:
bin/fairchain-cli --rpc http://127.0.0.1:19445 getinfo
bin/fairchain-cli --rpc http://127.0.0.1:19445 getblockcount
bin/fairchain-cli --rpc http://127.0.0.1:19445 peers
bin/fairchain-cli --rpc http://127.0.0.1:19445 getblock <hash>
```

---

## 12. Pre-Implementation Validation — Chaos Test Findings (Run 4)

### Test Execution Summary

Four chaos test runs were performed after adding RC-1 unit test and RC-2 debug logging.

| Run | Result | Notes |
|-----|--------|-------|
| 1 | ALL PASS | No divergence triggered |
| 2 | ALL PASS | No divergence triggered |
| 3 | ALL PASS | No divergence triggered |
| 4 | **6 FAILURES** | UTXO divergence + persistent fork (spread=41) |

The bug is non-deterministic — it depends on whether a 1-deep reorg at the chain tip coincides with a new block arriving whose parent is the pre-reorg tip.

### Run 4 Failure Details

**Final state**: 10 nodes, range=[121..162], spread=41 blocks.

- Node 0 (SEED): stuck at height 121
- Node 2 (miner): stuck at height 122
- All other nodes: advanced to 162

**Failed phases**:
- Phase I UTXO consistency: node0 has 5 UTXOs (25B), all others have 6 (30B)
- Phase J: DIVERGENCE spread=9
- Phase K: DIVERGENCE spread=23
- Phase L: DIVERGENCE spread=31
- Phase M: DIVERGENCE spread=41
- FINAL: DIVERGENCE spread=41

### RC-2 Confirmed: Orphan Poisoning via HasBlock + inv Cache

The RC-2 debug logging captured the exact failure mechanism on Node 0:

**Timeline (Node 0)**:
1. `01:53:10` — Accepted block at height 121 (hash `000000900c...`)
2. `01:53:13` — Reorged to different block at height 121 (hash `0000004d3a...`, 1-deep reorg)
3. `01:53:20` — Received block at height 122 (hash `00000011...`) from a peer. Its parent is the OLD height-121 block that was disconnected by the reorg. Block rejected as orphan: "parent unknown".
4. `01:53:20` — **The orphan block was written to the block index** (`PutBlockIndex` in `ProcessBlock`). Now `HasBlock()` returns true for this hash.
5. `01:53:20-24` — All 9 other peers announce this same block via inv. Every single inv is rejected with `"inv: block already known, not requesting"` (RC-2 debug tag confirmed). The block is **permanently poisoned** — no peer can deliver it again.
6. `01:53:24+` — All subsequent blocks arrive as orphans (parent chain broken). Each orphan is also written to the index, poisoning the entire chain of blocks.

**RC-2 stats for stuck nodes**:
- Node 0: 2,986 inv_skips, 45 block rejections (all orphans), 4 reorgs
- Node 2: 17,177 inv_skips, 73 block rejections, 4 reorgs

### Root Cause Chain (Confirmed)

The divergence is caused by the interaction of three bugs:

1. **RC-2a (handleInv)**: `HasBlock()` checks the block index, which includes orphaned/rejected blocks. Once a block is written to the index (even as an orphan), `handleInv` refuses to request it from any peer, permanently preventing re-evaluation.

2. **RC-2b (seenBlocks)**: The `seenBlocks` sync.Map stores hashes of all blocks ever received, including rejected ones. If a block is received and rejected, `handleBlock` will skip it on any future delivery from another peer.

3. **RC-5 (orphan writes to block index)**: `ProcessBlock` writes the `DiskBlockIndex` record for side-chain and orphan blocks before determining if they should be accepted. Once written, `HasBlock()` returns true, and the block can never be re-requested.

The trigger is a 1-deep reorg at the tip: Node A reorgs from block X to block Y at the same height. A peer then sends block Z (child of X). Node A rejects Z as an orphan (parent X is no longer on the active chain), writes Z to the index, and then permanently refuses to re-request Z from any peer. All subsequent blocks in that chain become unreachable orphans.

### RC-1 Unit Test Result

The `TestRC1_SideChainAncestorLookupBug` test confirmed that `getAncestorUnsafe` returns the wrong block for side-chain heights at retarget boundaries:
- Main chain block at height 5: ts=1700000300
- Side chain block at height 5: ts=1700000420
- `getAncestorUnsafe(5)` always returns the main chain block regardless of which chain is being validated

In this test's specific scenario, the bits happened to match due to clamping (both timespans clamped to the same bound), so the reorg succeeded. In the chaos test with testnet params (interval=20, 5s blocks), longer chains produce unclamped timespans where the bits actually differ, causing block rejection.

### Recommended Fix Priority (Updated)

Based on the chaos test findings, the fix priority should be:

1. **CRITICAL — RC-2a/RC-5**: Fix `handleInv` to not skip blocks that are only known as orphans. Either: (a) don't write orphan blocks to the block index, or (b) check the block's status in `handleInv` and re-request blocks that are orphaned/rejected.

2. **CRITICAL — RC-2b**: Clear `seenBlocks` entries for rejected blocks so they can be re-delivered by other peers.

3. **HIGH — RC-1**: Replace `getAncestorUnsafe` with a side-chain-aware ancestor lookup that walks the block's actual parent chain rather than using `hashByHeight` (which only tracks the active chain).

4. **MEDIUM — RC-5**: Make block index writes atomic with chain tip updates to prevent inconsistent state on crash.
