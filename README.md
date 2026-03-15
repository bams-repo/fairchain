# Fairchain

A Go-based blockchain proof-of-concept designed as a modular foundation for fairness-oriented consensus research.

## What This Is

Fairchain is a minimal but real blockchain node written in Go. It implements Nakamoto-style proof-of-work as a baseline consensus mechanism, with a pluggable `consensus.Engine` interface designed so future agents can swap in identity-bound, ticket-based, sequential-work consensus without rewriting the node.

## Build

```bash
make build
```

Produces two binaries in `bin/`:
- `fairchaind` — the full node daemon
- `fairchain-cli` — command-line RPC client (bitcoin-cli compatible)

Optional build targets:
```bash
make genesis      # Build the genesis block mining tool
make adversary    # Build the adversarial block generator
```

## Quick Start

### Run a regtest node with mining

```bash
make run-regtest
```

Or manually:

```bash
mkdir -p /tmp/fairchain-regtest
fairchaind \
  -network regtest \
  -datadir /tmp/fairchain-regtest \
  -listen 0.0.0.0:19444 \
  -rpcbind 127.0.0.1 \
  -rpcport 19445 \
  -mine
```

### Query node status

```bash
fairchain-cli getblockchaininfo
fairchain-cli getblockcount
fairchain-cli getpeerinfo
fairchain-cli getnetworkinfo
```

### Connect to a remote node

```bash
fairchain-cli -rpcconnect=45.32.196.26 -rpcport=19445 getblockchaininfo
```

### Run a second node connected to the first

```bash
mkdir -p /tmp/fairchain-regtest2
fairchaind \
  -network regtest \
  -datadir /tmp/fairchain-regtest2 \
  -listen 0.0.0.0:19446 \
  -rpcbind 127.0.0.1 \
  -rpcport 19447 \
  -addnode 127.0.0.1:19444
```

### Stop the daemon

```bash
fairchain-cli stop
```

## Daemon Flags (fairchaind)

| Flag | Description | Default |
|------|-------------|---------|
| `-network` | Network: mainnet, testnet, regtest | regtest |
| `-datadir` | Data directory | `~/.fairchain` |
| `-listen` | P2P listen address | `0.0.0.0:19444` |
| `-rpcbind` | RPC bind address | `127.0.0.1` |
| `-rpcport` | RPC port | `19445` |
| `-mine` | Enable mining | false |
| `-addnode` | Add a peer to connect to | |
| `-seed-peers` | Comma-separated seed peers | |
| `-conf` | Path to fairchain.conf | |
| `-log-level` | Log level: debug, info, warn, error | info |
| `-version` | Print version and exit | |

## CLI Commands (fairchain-cli)

### Options

| Flag | Description | Default |
|------|-------------|---------|
| `-rpcconnect` | RPC server host | `127.0.0.1` |
| `-rpcport` | RPC server port | `19445` |
| `-version` | Print version and exit | |

### Blockchain

| Command | Description |
|---------|-------------|
| `getblockchaininfo` | Get blockchain state |
| `getblockcount` | Get current block height |
| `getbestblockhash` | Get hash of best block |
| `getblockhash <height>` | Get block hash at height |
| `getblock <hash>` | Get block data by hash |
| `getdifficulty` | Get current difficulty |

### Network

| Command | Description |
|---------|-------------|
| `getnetworkinfo` | Get network state |
| `getpeerinfo` | Get connected peer details |
| `getconnectioncount` | Get number of connections |
| `addnode <ip:port>` | Connect to a node |
| `disconnectnode <addr>` | Disconnect a peer |

### Mempool

| Command | Description |
|---------|-------------|
| `getmempoolinfo` | Get mempool state |
| `getrawmempool [true]` | List mempool txids (verbose for details) |
| `getmempoolentry <txid>` | Get mempool entry for a transaction |

### UTXO

| Command | Description |
|---------|-------------|
| `gettxout <txid> <n>` | Get unspent output |
| `gettxoutsetinfo` | Get UTXO set statistics |

### Control

| Command | Description |
|---------|-------------|
| `getinfo` | Get node overview |
| `stop` | Stop the daemon |
| `help` | Show help |

## What Is Implemented

- **Core types**: Hash, BlockHeader, Block, Transaction (UTXO-style), canonical binary serialization
- **Crypto**: Double-SHA256, secp256k1 ECDSA, P2PKH scripts, Merkle roots, compact bits/target
- **Consensus**: Pluggable `consensus.Engine` interface with baseline PoW implementation
- **Validation**: Block structure, coinbase rules, merkle root, duplicate tx, subsidy, timestamps, difficulty retargeting, script execution
- **UTXO set**: In-memory with LevelDB persistence, connect/disconnect per block, undo data for reorgs
- **Mempool**: UTXO-validated, script-validated, fee-rate priority, double-spend detection, eviction
- **Mining**: Block template builder, fee-inclusive coinbase, P2PKH reward scripts
- **P2P networking**: Version handshake, ping/pong keepalive, inventory gossip, block/tx propagation, initial block sync, peer address gossip, misbehavior scoring, IP banning, rate limiting, inbound eviction, exponential reconnection backoff
- **Wire protocol**: Binary message encoding (version, verack, ping/pong, inv, getdata, block, tx, getblocks, addr)
- **RPC API**: Bitcoin Core-compatible HTTP JSON API (20 endpoints)
- **CLI**: Bitcoin-cli compatible command-line client
- **Storage**: LevelDB block index + flat file blocks (blk*.dat/rev*.dat) + LevelDB chainstate
- **Tests**: 60+ unit tests + 9 fuzz targets + 16-phase chaos test

## Configuration

Supports both JSON config files and Bitcoin Core-style `fairchain.conf` (INI format with `[main]`, `[test]`, `[regtest]` sections). All settings can be overridden via CLI flags.

## Architecture

See `WORKFILE.md` for detailed architecture documentation.

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
| Daemon entrypoint | `cmd/node/` |
| CLI | `cmd/cli/` |
