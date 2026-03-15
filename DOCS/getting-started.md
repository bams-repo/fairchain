# Getting Started

This guide covers building Fairchain from source, running your first node, and basic operations.

## Prerequisites

- **Go 1.25+** — [https://go.dev/dl/](https://go.dev/dl/)
- **Git** — for cloning the repository
- **Linux, macOS, or Windows (WSL/Git Bash)** — all platforms are supported

## Building

Clone the repository and build:

```bash
git clone https://github.com/bams-repo/fairchain.git
cd fairchain
make build
```

This produces two binaries in `bin/`:

| Binary | Description |
|--------|-------------|
| `fairchaind` | The full node daemon |
| `fairchain-cli` | Command-line RPC client |

Optional build targets:

```bash
make genesis      # Genesis block mining tool
make adversary    # Adversarial block generator (testing)
```

## Networks

Fairchain supports three networks:

| Network | Purpose | Block Time | Default P2P Port | Default RPC Port |
|---------|---------|------------|-------------------|------------------|
| `mainnet` | Production network | 10 minutes | 19333 | 19445 |
| `testnet` | Public test network | 5 seconds | 19334 | 19445 |
| `regtest` | Local regression testing | 1 second | 19444 | 19445 |

## Running a Node

### Regtest (local testing)

The fastest way to get started:

```bash
make run-regtest
```

Or manually:

```bash
mkdir -p /tmp/fairchain-regtest
./bin/fairchaind \
  -network regtest \
  -datadir /tmp/fairchain-regtest \
  -listen 0.0.0.0:19444 \
  -rpcbind 127.0.0.1 \
  -rpcport 19445 \
  -mine
```

### Testnet

Join the public test network:

```bash
mkdir -p /tmp/fairchain-testnet
./bin/fairchaind \
  -network testnet \
  -datadir /tmp/fairchain-testnet \
  -listen 0.0.0.0:19334 \
  -rpcbind 127.0.0.1 \
  -rpcport 19335 \
  -mine
```

Testnet has hardcoded seed nodes, so the node will automatically discover peers and begin syncing.

### Mainnet

```bash
./bin/fairchaind \
  -network mainnet \
  -datadir ~/.fairchain \
  -listen 0.0.0.0:19333 \
  -rpcbind 127.0.0.1 \
  -rpcport 19445
```

## Daemon Flags

| Flag | Description | Default |
|------|-------------|---------|
| `-network` | Network: `mainnet`, `testnet`, `regtest` | `regtest` |
| `-datadir` | Root data directory | `~/.fairchain` |
| `-listen` | P2P listen address (host:port) | `0.0.0.0:19444` |
| `-rpcbind` | RPC bind address | `127.0.0.1` |
| `-rpcport` | RPC port | `19445` |
| `-mine` | Enable the built-in miner | `false` |
| `-addnode` | Add a peer to connect to (ip:port) | |
| `-seed-peers` | Comma-separated seed peers | |
| `-connect` | Connect ONLY to these peers (disables discovery) | |
| `-noseednode` | Suppress hardcoded seed nodes | `false` |
| `-conf` | Path to `fairchain.conf` (INI-style) | |
| `-config` | Path to JSON config file | |
| `-norpcauth` | Disable RPC authentication (testing only) | `false` |
| `-log-level` | Log level: `debug`, `info`, `warn`, `error` | `info` |
| `-debug` | Enable verbose debug output | `false` |
| `-migrate` | Migrate legacy blocks.db to new format | `false` |
| `-version` | Print version and exit | |

## Configuration Files

Fairchain supports two config file formats. CLI flags always take priority over config file values.

### JSON format

```json
{
  "network": "regtest",
  "data_dir": "/tmp/fairchain-regtest",
  "listen_addr": "0.0.0.0:19444",
  "rpc_addr": "127.0.0.1:19445",
  "seed_peers": [],
  "max_inbound": 25,
  "max_outbound": 8,
  "mining_enabled": false,
  "log_level": "info",
  "rpc_user": "",
  "rpc_password": ""
}
```

Load with: `fairchaind -config /path/to/config.json`

### INI format (fairchain.conf)

Bitcoin Core-style config with network sections:

```ini
# Global options
datadir=/home/user/.fairchain
loglevel=info

[main]
listen=0.0.0.0:19333
rpc=127.0.0.1:19445

[test]
listen=0.0.0.0:19334
rpc=127.0.0.1:19335
mine=1

[regtest]
listen=0.0.0.0:19444
rpc=127.0.0.1:19445
mine=1
```

The daemon automatically looks for `fairchain.conf` in the data directory root. Override with: `fairchaind -conf /path/to/fairchain.conf`

## RPC Authentication

By default, the RPC server generates a random cookie file (`.cookie`) in the data directory, matching Bitcoin Core's cookie-based auth. The CLI reads this automatically when connecting to `localhost`.

To set explicit credentials, use the config file:

```json
{
  "rpc_user": "myuser",
  "rpc_password": "mypassword"
}
```

For local testing/regtest, you can disable auth entirely with `-norpcauth`.

## Data Directory Layout

```
~/.fairchain/                    # mainnet (root)
  blocks/
    blk00000.dat                 # Raw block data (flat files)
    rev00000.dat                 # Undo/revert data for reorgs
    index/                       # LevelDB block index
  chainstate/                    # LevelDB UTXO set
  wallet/                        # Mining key storage
  peers.db                       # Peer database (bbolt)
  peers.dat                      # Peer address dump
  mempool.dat                    # Persisted mempool
  .cookie                        # RPC auth cookie
  .lock                          # Instance lock file
  fairchain.conf                 # Optional config

  testnet2/                      # Testnet subdirectory
    blocks/
    chainstate/
    wallet/
    ...

  regtest/                       # Regtest subdirectory
    blocks/
    chainstate/
    wallet/
    ...
```

## Basic Operations

### Check node status

```bash
./bin/fairchain-cli getblockchaininfo
```

### Check block height

```bash
./bin/fairchain-cli getblockcount
```

### View connected peers

```bash
./bin/fairchain-cli getpeerinfo
```

### Connect to a remote node's RPC

```bash
./bin/fairchain-cli -rpcconnect=45.32.196.26 -rpcport=19335 getblockchaininfo
```

### Connect a second local node

```bash
mkdir -p /tmp/fairchain-regtest2
./bin/fairchaind \
  -network regtest \
  -datadir /tmp/fairchain-regtest2 \
  -listen 0.0.0.0:19446 \
  -rpcbind 127.0.0.1 \
  -rpcport 19447 \
  -addnode 127.0.0.1:19444
```

### Stop the daemon

```bash
./bin/fairchain-cli stop
```

Or send `SIGINT`/`SIGTERM` (Ctrl+C) to the process.

## Running as a Systemd Service

For long-running deployments (e.g., testnet seed nodes), create a systemd unit:

```ini
[Unit]
Description=Fairchain Node
After=network.target

[Service]
Type=simple
User=fairchain
ExecStart=/usr/local/bin/fairchaind \
  -network testnet \
  -datadir /var/lib/fairchain \
  -listen 0.0.0.0:19334 \
  -rpcbind 127.0.0.1 \
  -rpcport 19335 \
  -mine
Restart=on-failure
RestartSec=10

[Install]
WantedBy=multi-user.target
```

## Next Steps

- See [RPC Commands](rpc-commands.md) for the full API reference
- Run `./bin/fairchain-cli help` for a quick command listing
