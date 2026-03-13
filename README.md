# Fairchain

A Go-based blockchain proof-of-concept designed as a modular foundation for fairness-oriented consensus research.

## What This Is

Fairchain is a minimal but real blockchain node written in Go. It implements Nakamoto-style proof-of-work as a baseline consensus mechanism, with a pluggable `consensus.Engine` interface designed so future agents can swap in identity-bound, ticket-based, sequential-work consensus without rewriting the node.

## What Is Implemented

- **Core types**: Hash, BlockHeader, Block, Transaction (UTXO-style), canonical binary serialization
- **Crypto**: Double-SHA256 hashing, Merkle root computation, compact bits/target conversion, chainwork calculation
- **Chain params**: Mainnet, testnet, and regtest network definitions with full parameterization
- **Consensus**: Pluggable `consensus.Engine` interface with baseline PoW implementation
- **Validation**: Block structure validation, coinbase rules, merkle root verification, duplicate tx rejection, subsidy enforcement, timestamp rules (median-11 and prev+1), difficulty retargeting
- **Genesis**: Configurable genesis block builder and PoW miner; reproducible from fixed inputs
- **Chain manager**: Tip tracking, block acceptance, orphan pool, basic reorg support
- **Storage**: Abstract `BlockStore`/`PeerStore` interfaces with bbolt implementation
- **Mempool**: Thread-safe transaction pool with admission control
- **Miner**: Block template builder, coinbase creation, nonce-iterating mining loop
- **P2P networking**: TCP peer connections, version handshake, self-connection detection, inventory-based gossip, block/tx propagation, initial block sync, peer address gossip
- **Peer discovery**: Seed peers from config, persistent peer store, reconnection logic
- **Wire protocol**: Deterministic binary message encoding (version, verack, ping/pong, inv, getdata, block, tx, getblocks, addr)
- **RPC API**: Local HTTP JSON API (getinfo, getblockcount, getbestblockhash, getpeerinfo, getblock, submitblock, getmempoolinfo)
- **CLI**: Command-line tool for querying node status
- **Tests**: 43 passing unit tests + 9 fuzz targets covering serialization, hashing, merkle roots, compact bits, genesis mining, block validation, chain operations, protocol encoding, and storage
- **Adversarial testing**: Dedicated tool and 16-phase chaos test covering invalid PoW, corrupted merkle roots, duplicate blocks, time-warp attacks, orphan floods, inflated coinbase, and empty blocks
- **Structured logging**: `log/slog` with configurable log levels (`--log-level debug|info|warn|error`)
- **Metrics**: Atomic counters for blocks accepted/rejected/mined, reorgs, peers, orphans; exposed via `/metrics` RPC endpoint

## What Is Not Implemented Yet

- Full UTXO spend validation (coinbase-only for now)
- Script execution
- Wallet / key management
- Headers-first sync (uses simple block sync)
- Advanced peer scoring
- Identity registration transactions
- Epoch/ticket consensus
- VRF-based eligibility
- Sequential memory-hard proofs
- Reward damping
- Collateral-backed mining identities

## Build

```bash
make build
```

Produces four binaries in `bin/`:
- `fairchain-node` — the full node
- `fairchain-genesis` — genesis block mining tool
- `fairchain-cli` — CLI query tool
- `fairchain-adversary` — adversarial block generator for chaos/security testing

## Quick Start

### Mine a genesis block (informational)

```bash
bin/fairchain-genesis --network regtest
```

### Run a single regtest node with mining

```bash
make run-regtest
```

Or manually:

```bash
mkdir -p /tmp/fairchain-regtest
bin/fairchain-node \
  --network regtest \
  --datadir /tmp/fairchain-regtest \
  --listen 0.0.0.0:19444 \
  --rpc 127.0.0.1:19445 \
  --mine
```

### Query node status

```bash
bin/fairchain-cli --rpc http://127.0.0.1:19445 getinfo
```

### Run a second node (connects to first)

```bash
mkdir -p /tmp/fairchain-regtest2
bin/fairchain-node \
  --network regtest \
  --datadir /tmp/fairchain-regtest2 \
  --listen 0.0.0.0:19446 \
  --rpc 127.0.0.1:19447
```

To connect them, add `--seed-peers` or configure `seed_peers` in the config file pointing to the first node's listen address.

### Run tests

```bash
make test
```

### Run chaos/adversarial stress test

```bash
python scripts/chaos_test.py
```

## Where to Look First

| Area | Path |
|------|------|
| Core types & serialization | `internal/types/` |
| Hashing & merkle | `internal/crypto/` |
| Chain params | `internal/params/` |
| Consensus interface | `internal/consensus/engine.go` |
| PoW engine | `internal/consensus/pow/` |
| Block validation | `internal/consensus/validation.go` |
| Chain manager | `internal/chain/` |
| Storage | `internal/store/` |
| Wire protocol | `internal/protocol/` |
| P2P networking | `internal/p2p/` |
| Miner | `internal/miner/` |
| RPC API | `internal/rpc/` |
| Node entrypoint | `cmd/node/` |
| Genesis tool | `cmd/genesis/` |

## Configuration

Copy `config.sample.json` and edit as needed. All settings can also be overridden via CLI flags.

## Architecture

See `WORKFILE.md` for detailed architecture documentation and handoff notes for future agents.

See `TODO.md` for prioritized development milestones.
