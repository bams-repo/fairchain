# RPC Commands

Fairchain exposes an HTTP JSON API compatible with Bitcoin Core's RPC interface. Endpoints return JSON responses. State-changing endpoints (sending, signing, encryption, shutdown) require POST requests; read-only endpoints accept GET.

## Command Index

### Blockchain

| Command | Parameters | Description |
|---------|------------|-------------|
| [`getblockchaininfo`](#getblockchaininfo) | — | Full blockchain state |
| [`getblockcount`](#getblockcount) | — | Current block height |
| [`getbestblockhash`](#getbestblockhash) | — | Tip block hash |
| [`getblockhash`](#getblockhash) | `<height>` | Block hash at height |
| [`getblock`](#getblock) | `<hash>` | Block details by hash |
| [`getblockbyheight`](#getblockbyheight) | `<height>` | Block details by height |
| [`getdifficulty`](#getdifficulty) | — | Current PoW difficulty |

### Network

| Command | Parameters | Description |
|---------|------------|-------------|
| [`getnetworkinfo`](#getnetworkinfo) | — | Network state and version |
| [`getpeerinfo`](#getpeerinfo) | — | Connected peer details |
| [`getconnectioncount`](#getconnectioncount) | — | Total peer count |
| [`addnode`](#addnode) | `<ip:port>` | Connect to a peer |
| [`disconnectnode`](#disconnectnode) | `<address>` | Disconnect a peer |

### Mempool

| Command | Parameters | Description |
|---------|------------|-------------|
| [`getmempoolinfo`](#getmempoolinfo) | — | Mempool state |
| [`getrawmempool`](#getrawmempool) | `[verbose]` | List mempool transactions |
| [`getmempoolentry`](#getmempoolentry) | `<txid>` | Single mempool entry |

### UTXO

| Command | Parameters | Description |
|---------|------------|-------------|
| [`gettxout`](#gettxout) | `<txid> <n>` | Unspent output info |
| [`gettxoutsetinfo`](#gettxoutsetinfo) | — | UTXO set statistics |

### Mining

| Command | Parameters | Description |
|---------|------------|-------------|
| [`submitblock`](#submitblock) | POST body | Submit a mined block |

### Wallet

| Command | Parameters | Method | Description |
|---------|------------|--------|-------------|
| [`getnewaddress`](#getnewaddress) | — | GET | New receiving address |
| [`getbalance`](#getbalance) | `[minconf]` | GET | Wallet balance |
| [`listunspent`](#listunspent) | `[minconf] [maxconf]` | GET | Wallet UTXOs |
| [`sendtoaddress`](#sendtoaddress) | `<addr> <amount>` | POST | Send coins |
| [`listtransactions`](#listtransactions) | `[count]` | GET | Recent wallet transactions |
| [`gettransaction`](#gettransaction) | `<txid>` | GET | Transaction details |
| [`getwalletinfo`](#getwalletinfo) | — | GET | Wallet status |
| [`dumpprivkey`](#dumpprivkey) | `<address>` | GET | Export private key (WIF) |
| [`importprivkey`](#importprivkey) | `<key>` | POST | Import private key |
| [`validateaddress`](#validateaddress) | `<address>` | GET | Validate an address |
| [`getrawchangeaddress`](#getrawchangeaddress) | — | GET | New change address |
| [`settxfee`](#settxfee) | `<amount>` | POST | Set fee rate per byte |
| [`sendrawtransaction`](#sendrawtransaction) | `<hex>` | POST | Submit raw transaction |
| [`signrawtransactionwithwallet`](#signrawtransactionwithwallet) | `<hex>` | POST | Sign with wallet keys |
| [`getreceivedbyaddress`](#getreceivedbyaddress) | `<addr> [minconf]` | GET | Total received by address |
| [`listaddressgroupings`](#listaddressgroupings) | — | GET | Address groupings |
| [`backupwallet`](#backupwallet) | `<destination>` | POST | Backup wallet file |
| [`getaddressesbylabel`](#getaddressesbylabel) | — | GET | All wallet addresses |
| [`dumpwallet`](#dumpwallet) | — | GET | Dump mnemonic + addresses |
| [`encryptwallet`](#encryptwallet) | `<passphrase>` | POST | Encrypt the wallet |
| [`walletpassphrase`](#walletpassphrase) | `<pass> [timeout]` | POST | Unlock wallet |
| [`walletlock`](#walletlock) | — | GET | Lock wallet |

### Control

| Command | Parameters | Method | Description |
|---------|------------|--------|-------------|
| [`getinfo`](#getinfo) | — | GET | Node overview |
| [`stop`](#stop) | — | POST | Shutdown daemon |
| [`help`](#help) | — | CLI | List commands |

### Fairchain-Specific

| Command | Parameters | Description |
|---------|------------|-------------|
| [`getchainstatus`](#getchainstatus) | — | Chain status + retarget info |
| [`metrics`](#metrics) | — | Internal performance metrics |

---

## Using the CLI

The `fairchain-cli` tool is the primary way to interact with a running node:

```bash
fairchain-cli [options] <command> [params]
```

### CLI Options

| Flag | Description | Default |
|------|-------------|---------|
| `-rpcconnect` | RPC server host | `127.0.0.1` |
| `-rpcport` | RPC server port | `19445` |
| `-version` | Print version and exit | |

### Examples

```bash
# Local node (default)
fairchain-cli getblockchaininfo

# Remote node
fairchain-cli -rpcconnect=45.32.196.26 -rpcport=19335 getblockchaininfo

# Different local port
fairchain-cli -rpcport=19447 getblockcount
```

## Using curl

Every RPC endpoint is also accessible directly via HTTP. This is useful for scripting or when the CLI isn't available:

```bash
# Simple query (GET)
curl -s http://127.0.0.1:19445/getblockchaininfo | python3 -m json.tool

# With authentication
curl -s -u myuser:mypassword http://127.0.0.1:19445/getblockchaininfo

# With cookie auth
curl -s -u "$(cat ~/.fairchain/.cookie)" http://127.0.0.1:19445/getblockchaininfo

# Query with parameters (GET)
curl -s "http://127.0.0.1:19445/getblockhash?height=100"

# POST endpoint (state-changing)
curl -s -X POST "http://127.0.0.1:19445/sendtoaddress?address=1A1z...&amount=50000000"

# POST endpoint (submitblock — binary body)
curl -s -X POST --data-binary @block.bin http://127.0.0.1:19445/submitblock
```

---

## Blockchain Commands

### getblockchaininfo

Returns the current state of the blockchain.

```bash
fairchain-cli getblockchaininfo
```

**Response fields:**

| Field | Type | Description |
|-------|------|-------------|
| `chain` | string | Network name (`mainnet`, `testnet`, `regtest`) |
| `blocks` | number | Current block height |
| `headers` | number | Current header height (same as blocks) |
| `bestblockhash` | string | Hash of the tip block (hex, display order) |
| `bits` | string | Current compact difficulty target (hex) |
| `difficulty` | number | Current difficulty as a floating-point multiplier |
| `mediantime` | number | Median time of the last 11 blocks (unix timestamp) |
| `verificationprogress` | number | Chain verification progress (0.0 to 1.0) |
| `initialblockdownload` | boolean | Whether the node is still performing initial sync |
| `chainwork` | string | Total cumulative proof-of-work (hex, 256-bit) |
| `pruned` | boolean | Always `false` (pruning not implemented) |
| `warnings` | string | Any active network warnings |

**Example response:**

```json
{
  "chain": "testnet",
  "blocks": 1542,
  "headers": 1542,
  "bestblockhash": "000000034a1b...",
  "bits": "1d0fffff",
  "difficulty": 16.0001,
  "mediantime": 1773534200,
  "verificationprogress": 1,
  "initialblockdownload": false,
  "chainwork": "0000000000000000000000000000000000000000000000000000000000003086",
  "pruned": false,
  "warnings": ""
}
```

---

### getblockcount

Returns the height of the most-work fully-validated chain.

```bash
fairchain-cli getblockcount
```

**Response:** integer — the current block height.

```
1542
```

---

### getbestblockhash

Returns the hash of the best (tip) block.

```bash
fairchain-cli getbestblockhash
```

**Response:** string — block hash in display order (hex).

```
000000034a1b2c3d...
```

---

### getblockhash

Returns the block hash at a given height.

```bash
fairchain-cli getblockhash <height>
```

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `height` | integer | Yes | Block height |

**curl equivalent:**

```bash
curl -s "http://127.0.0.1:19445/getblockhash?height=100"
```

**Response:** string — block hash in display order (hex).

---

### getblock

Returns detailed information about a block by its hash.

```bash
fairchain-cli getblock <hash>
```

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `hash` | string | Yes | Block hash (hex, display order) |

**curl equivalent:**

```bash
curl -s "http://127.0.0.1:19445/getblock?hash=000000034a1b..."
```

**Response fields:**

| Field | Type | Description |
|-------|------|-------------|
| `hash` | string | Block hash |
| `confirmations` | number | Number of confirmations (-1 if not in main chain) |
| `height` | number | Block height |
| `version` | number | Block version |
| `merkleroot` | string | Merkle root of transactions |
| `tx` | array | List of transaction IDs (hex) |
| `time` | number | Block timestamp (unix) |
| `nonce` | number | Nonce used to solve the block |
| `bits` | string | Compact difficulty target (hex) |
| `previousblockhash` | string | Hash of the previous block |
| `nTx` | number | Number of transactions in the block |

---

### getblockbyheight

Returns detailed information about a block by its height. This is a convenience endpoint not present in Bitcoin Core.

```bash
fairchain-cli getblockbyheight <height>
```

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `height` | integer | Yes | Block height |

**curl equivalent:**

```bash
curl -s "http://127.0.0.1:19445/getblockbyheight?height=100"
```

**Response:** Same format as `getblock`.

---

### getdifficulty

Returns the current proof-of-work difficulty as a multiple of the minimum difficulty.

```bash
fairchain-cli getdifficulty
```

**Response:** number — difficulty value.

```
56.1411
```

---

## Network Commands

### getnetworkinfo

Returns information about the node's network state.

```bash
fairchain-cli getnetworkinfo
```

**Response fields:**

| Field | Type | Description |
|-------|------|-------------|
| `version` | number | Protocol version |
| `subversion` | string | User agent string (e.g., `/fairchain:0.3.0/`) |
| `protocolversion` | number | Protocol version |
| `connections` | number | Total peer connections |
| `connections_in` | number | Inbound connections |
| `connections_out` | number | Outbound connections |
| `networkactive` | boolean | Always `true` |
| `networks` | array | Supported network types |
| `warnings` | string | Any active warnings |

---

### getpeerinfo

Returns detailed information about each connected peer.

```bash
fairchain-cli getpeerinfo
```

**Response:** array of peer objects with connection details including address, version, latency, and sync state.

---

### getconnectioncount

Returns the total number of connected peers.

```bash
fairchain-cli getconnectioncount
```

**Response:** integer — peer count.

```
11
```

---

### addnode

Attempts to connect to a new peer.

```bash
fairchain-cli addnode <ip:port>
```

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `ip:port` | string | Yes | Peer address to connect to |

**curl equivalent:**

```bash
curl -s "http://127.0.0.1:19445/addnode?node=192.168.1.100:19334"
```

**Response:**

```json
{
  "added": "192.168.1.100:19334"
}
```

---

### disconnectnode

Disconnects from a connected peer.

```bash
fairchain-cli disconnectnode <address>
```

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `address` | string | Yes | Peer address to disconnect |

**curl equivalent:**

```bash
curl -s "http://127.0.0.1:19445/disconnectnode?address=192.168.1.100:19334"
```

**Response:**

```json
{
  "disconnected": "192.168.1.100:19334"
}
```

---

## Mempool Commands

### getmempoolinfo

Returns the current state of the transaction mempool.

```bash
fairchain-cli getmempoolinfo
```

**Response fields:**

| Field | Type | Description |
|-------|------|-------------|
| `loaded` | boolean | Whether the mempool is loaded |
| `size` | number | Number of transactions in the mempool |

---

### getrawmempool

Returns all transaction IDs in the mempool.

```bash
# Compact (txid list only)
fairchain-cli getrawmempool

# Verbose (includes fee info)
fairchain-cli getrawmempool true
```

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `verbose` | boolean | No | If `true`, returns detailed info per transaction |

**curl equivalent:**

```bash
# Compact
curl -s http://127.0.0.1:19445/getrawmempool

# Verbose
curl -s "http://127.0.0.1:19445/getrawmempool?verbose=true"
```

**Compact response:** array of transaction ID strings.

**Verbose response:** object keyed by txid with fee details:

```json
{
  "abc123...": {
    "size": 226,
    "fee": 10000,
    "fees": {
      "base": 10000
    },
    "feerate": 44.24
  }
}
```

---

### getmempoolentry

Returns mempool data for a specific transaction.

```bash
fairchain-cli getmempoolentry <txid>
```

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `txid` | string | Yes | Transaction ID (hex) |

**curl equivalent:**

```bash
curl -s "http://127.0.0.1:19445/getmempoolentry?txid=abc123..."
```

**Response fields:**

| Field | Type | Description |
|-------|------|-------------|
| `size` | number | Transaction size in bytes |
| `fee` | number | Transaction fee (smallest units) |
| `fees.base` | number | Base fee |
| `feerate` | number | Fee rate (fee per byte) |

---

## UTXO Commands

### gettxout

Returns information about an unspent transaction output.

```bash
fairchain-cli gettxout <txid> <n>
```

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `txid` | string | Yes | Transaction ID (hex) |
| `n` | integer | Yes | Output index (vout) |

**curl equivalent:**

```bash
curl -s "http://127.0.0.1:19445/gettxout?txid=abc123...&n=0"
```

**Response fields:**

| Field | Type | Description |
|-------|------|-------------|
| `bestblock` | string | Hash of the current tip block |
| `confirmations` | number | Number of confirmations |
| `value` | number | Output value (smallest units) |
| `scriptPubKey.hex` | string | Locking script (hex) |
| `coinbase` | boolean | Whether this output is from a coinbase transaction |

Returns `null` if the output doesn't exist or has been spent.

---

### gettxoutsetinfo

Returns statistics about the entire unspent transaction output set.

```bash
fairchain-cli gettxoutsetinfo
```

**Response fields:**

| Field | Type | Description |
|-------|------|-------------|
| `height` | number | Block height of the UTXO set |
| `bestblock` | string | Block hash of the UTXO set |
| `txouts` | number | Total number of unspent outputs |
| `total_amount` | number | Sum of all unspent output values (smallest units) |

**Example response:**

```json
{
  "height": 1542,
  "bestblock": "000000034a1b...",
  "txouts": 1543,
  "total_amount": 77150000000
}
```

---

## Mining Commands

### submitblock

Submits a new block to the network. Requires a POST request with the serialized block as the request body.

This endpoint is not accessible via `fairchain-cli` — use `curl` or a direct HTTP client.

```bash
curl -s -X POST --data-binary @block.bin http://127.0.0.1:19445/submitblock
```

**Response (accepted):**

```json
{
  "accepted": true,
  "hash": "000000034a1b...",
  "height": 1543
}
```

**Response (rejected):**

```json
{
  "error": "rejected: block validation failed: ..."
}
```

---

## Control Commands

### getinfo

Returns a summary overview of the node.

```bash
fairchain-cli getinfo
```

**Response fields:**

| Field | Type | Description |
|-------|------|-------------|
| `version` | number | Protocol version |
| `protocolversion` | number | Protocol version |
| `blocks` | number | Current block height |
| `bestblockhash` | string | Hash of the tip block |
| `difficulty` | number | Current difficulty |
| `connections` | number | Number of connected peers |
| `network` | string | Network name |
| `mempool_size` | number | Transactions in mempool |

---

### stop

Initiates a graceful shutdown of the daemon. Requires POST.

```bash
# Via curl (POST required)
curl -s -X POST http://127.0.0.1:19445/stop

# Via CLI (sends GET — use curl for proper POST)
fairchain-cli stop
```

**Response:**

```
"Fairchain server stopping"
```

---

### help

Displays available commands (CLI only, not an RPC endpoint).

```bash
fairchain-cli help
```

---

## Wallet Commands

### getnewaddress

Generates a new receiving address from the HD wallet keypool.

```bash
fairchain-cli getnewaddress
```

**Response:** string — new Base58Check address.

---

### getbalance

Returns the wallet's confirmed balance.

```bash
fairchain-cli getbalance [minconf]
```

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `minconf` | integer | No | Minimum confirmations (default: 1) |

**Response fields:**

| Field | Type | Description |
|-------|------|-------------|
| `balance` | number | Balance in smallest units |
| `balance_btc` | number | Balance in whole coins |

---

### listunspent

Returns all unspent transaction outputs belonging to the wallet.

```bash
fairchain-cli listunspent [minconf] [maxconf]
```

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `minconf` | integer | No | Minimum confirmations (default: 1) |
| `maxconf` | integer | No | Maximum confirmations (default: 9999999) |

**Response:** array of UTXO objects:

| Field | Type | Description |
|-------|------|-------------|
| `txid` | string | Transaction ID (hex, display order) |
| `vout` | number | Output index |
| `address` | string | Receiving address |
| `scriptPubKey` | string | Locking script (hex) |
| `amount` | number | Value in smallest units |
| `amount_btc` | number | Value in whole coins |
| `confirmations` | number | Number of confirmations |
| `spendable` | boolean | Whether the output is spendable |

---

### sendtoaddress

Sends coins to the specified address. Automatically selects UTXOs, calculates fees, and generates change. Requires POST. Wallet must be unlocked if encrypted.

```bash
# Via curl
curl -s -X POST "http://127.0.0.1:19445/sendtoaddress?address=<addr>&amount=<amount>"

# Via CLI
fairchain-cli sendtoaddress <address> <amount>
```

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `address` | string | Yes | Destination address |
| `amount` | integer | Yes | Amount in smallest units |

**Response:** string — transaction ID (hex, display order).

---

### listtransactions

Returns recent wallet transactions (UTXOs belonging to the wallet).

```bash
fairchain-cli listtransactions [count]
```

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `count` | integer | No | Maximum number of transactions to return (default: 10) |

**Response:** array of transaction objects:

| Field | Type | Description |
|-------|------|-------------|
| `txid` | string | Transaction ID (hex, display order) |
| `vout` | number | Output index |
| `address` | string | Address |
| `category` | string | `receive`, `generate` (mature coinbase), or `immature` (immature coinbase) |
| `amount` | number | Value in smallest units |
| `amount_btc` | number | Value in whole coins |
| `confirmations` | number | Number of confirmations |
| `blockheight` | number | Block height the transaction was included in |

---

### getwalletinfo

Returns wallet status information.

```bash
fairchain-cli getwalletinfo
```

**Response fields:**

| Field | Type | Description |
|-------|------|-------------|
| `walletname` | string | Wallet name (always `"default"`) |
| `walletversion` | number | Wallet format version |
| `balance` | number | Confirmed balance (smallest units) |
| `balance_btc` | number | Confirmed balance (whole coins) |
| `unconfirmed_balance` | number | Unconfirmed balance |
| `txcount` | number | Transaction count |
| `keypoolsize` | number | Number of derived keys |
| `paytxfee` | number | Current fee rate (per byte) |
| `hdseedid` | string | Default wallet address |
| `private_keys_enabled` | boolean | Always `true` |
| `unlocked_until` | number | 0 if locked, -1 if unlocked (only present when encrypted) |

---

### dumpprivkey

Returns the private key for an address in WIF (Wallet Import Format).

```bash
fairchain-cli dumpprivkey <address>
```

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `address` | string | Yes | Address to dump key for |

**Response:** string — WIF-encoded private key. Mainnet keys start with 'K' or 'L', testnet/regtest keys start with 'c'.

---

### importprivkey

Imports a private key into the wallet. Accepts WIF format (preferred) or raw hex (backward compatible). Requires POST.

```bash
# Via curl
curl -s -X POST "http://127.0.0.1:19445/importprivkey?privkey=<key>"

# Via CLI
fairchain-cli importprivkey <key>
```

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `key` | string | Yes | Private key in WIF or hex format |

**Response:**

```json
{
  "address": "1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa"
}
```

---

### validateaddress

Returns information about a given address.

```bash
fairchain-cli validateaddress <address>
```

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `address` | string | Yes | Address to validate |

**Response fields:**

| Field | Type | Description |
|-------|------|-------------|
| `isvalid` | boolean | Whether the address is valid |
| `address` | string | The address |
| `scriptPubKey` | string | P2PKH locking script (hex) |
| `ismine` | boolean | Whether the wallet owns this address |
| `iswatchonly` | boolean | Always `false` |
| `isscript` | boolean | Always `false` |
| `version` | number | Address version byte |

---

### getrawchangeaddress

Generates a new change address from the HD wallet.

```bash
fairchain-cli getrawchangeaddress
```

**Response:** string — new change address.

---

### settxfee

Sets the transaction fee rate (per byte) for wallet transactions. Requires POST. Maximum allowed value is 10,000 sat/byte.

```bash
# Via curl
curl -s -X POST "http://127.0.0.1:19445/settxfee?amount=<amount>"

# Via CLI
fairchain-cli settxfee <amount>
```

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `amount` | integer | Yes | Fee rate in smallest units per byte (max: 10,000) |

**Response:** `true` on success.

---

### sendrawtransaction

Submits a serialized transaction to the mempool and broadcasts it to the network. Requires POST.

```bash
# Via curl
curl -s -X POST "http://127.0.0.1:19445/sendrawtransaction?hexstring=<hex>"

# Via CLI
fairchain-cli sendrawtransaction <hexstring>
```

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `hexstring` | string | Yes | Hex-encoded serialized transaction |

**Response:** string — transaction ID (hex, display order).

---

### signrawtransactionwithwallet

Signs a raw transaction using wallet keys. Signs all inputs that correspond to wallet-owned UTXOs. Requires POST. Wallet must be unlocked if encrypted.

```bash
# Via curl
curl -s -X POST "http://127.0.0.1:19445/signrawtransactionwithwallet?hexstring=<hex>"

# Via CLI
fairchain-cli signrawtransactionwithwallet <hexstring>
```

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `hexstring` | string | Yes | Hex-encoded unsigned transaction |

**Response fields:**

| Field | Type | Description |
|-------|------|-------------|
| `hex` | string | Hex-encoded signed transaction |
| `complete` | boolean | Whether all inputs are signed |

---

### getreceivedbyaddress

Returns the total amount received by an address (sum of all UTXOs paying to it).

```bash
fairchain-cli getreceivedbyaddress <address> [minconf]
```

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `address` | string | Yes | Address to query |
| `minconf` | integer | No | Minimum confirmations (default: 1) |

**Response fields:**

| Field | Type | Description |
|-------|------|-------------|
| `amount` | number | Total received (smallest units) |
| `amount_btc` | number | Total received (whole coins) |

---

### listaddressgroupings

Returns all addresses in the wallet grouped with their balances.

```bash
fairchain-cli listaddressgroupings
```

**Response:** array of address groupings, each containing [address, balance, balance_btc].

---

### backupwallet

Creates a backup copy of the wallet file. The destination must be a relative path (no absolute paths or `..` traversal allowed). Requires POST.

```bash
# Via curl
curl -s -X POST "http://127.0.0.1:19445/backupwallet?destination=wallet-backup.dat"

# Via CLI
fairchain-cli backupwallet <destination>
```

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `destination` | string | Yes | Relative file path for the backup |

**Response:** `true` on success.

---

### getaddressesbylabel

Returns all addresses associated with the wallet.

```bash
fairchain-cli getaddressesbylabel
```

**Response:** object keyed by address with purpose information.

---

### dumpwallet

Returns the wallet mnemonic phrase and all derived addresses. Wallet must be unlocked if encrypted.

```bash
fairchain-cli dumpwallet
```

**Response fields:**

| Field | Type | Description |
|-------|------|-------------|
| `mnemonic` | string | BIP39 24-word seed phrase |
| `addresses` | array | All derived addresses |
| `keypoolsize` | number | Number of derived keys |

---

### gettransaction

Get details about a transaction by txid. Checks mempool first, then scans the UTXO set. Without a full transaction index, only transactions with unspent outputs are visible on-chain.

```bash
fairchain-cli gettransaction <txid>
```

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `txid` | string | Yes | Transaction hash (hex, display order) |

**Response fields (mempool transaction):**

| Field | Type | Description |
|-------|------|-------------|
| `txid` | string | Transaction hash |
| `confirmations` | number | Always 0 for mempool transactions |
| `hex` | string | Serialized transaction (hex) |
| `fee` | number | Transaction fee |

**Response fields (confirmed transaction):**

| Field | Type | Description |
|-------|------|-------------|
| `txid` | string | Transaction hash |
| `confirmations` | number | Number of confirmations |
| `blockheight` | number | Block height |
| `amount` | number | Total output value (smallest units) |
| `details` | array | Output details (address, vout, amount, category) |

---

### encryptwallet

Encrypts the wallet with a passphrase. After encryption, the wallet is locked and private key operations require `walletpassphrase` to unlock. Requires POST.

```bash
# Via curl
curl -s -X POST "http://127.0.0.1:19445/encryptwallet?passphrase=my+secure+passphrase"

# Via CLI
fairchain-cli encryptwallet "my secure passphrase"
```

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `passphrase` | string | Yes | Encryption passphrase |

**Response:**

```
"wallet encrypted successfully, wallet is now locked"
```

---

### walletpassphrase

Unlocks an encrypted wallet for the specified duration (in seconds). Required before any operation that needs private keys (sending, signing, dumping keys). Requires POST.

```bash
# Via curl
curl -s -X POST "http://127.0.0.1:19445/walletpassphrase?passphrase=my+secure+passphrase&timeout=300"

# Via CLI
fairchain-cli walletpassphrase "my secure passphrase" 300
```

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `passphrase` | string | Yes | Wallet passphrase |
| `timeout` | integer | No | Unlock duration in seconds (default: 300) |

**Response:** `true` on success.

---

### walletlock

Immediately locks the wallet, clearing the decryption key from memory.

```bash
fairchain-cli walletlock
```

**Response:** `true` on success.

---

## Fairchain-Specific Commands

These endpoints are not part of Bitcoin Core's RPC interface.

### getchainstatus

Returns Fairchain-specific chain status including difficulty retarget information.

```bash
fairchain-cli getchainstatus
```

**Response fields:**

| Field | Type | Description |
|-------|------|-------------|
| `blocks` | number | Current block height |
| `bestblockhash` | string | Hash of the tip block |
| `bits` | string | Current compact difficulty target (hex) |
| `difficulty` | number | Current difficulty |
| `peers` | number | Number of connected peers |
| `retarget_epoch` | number | Current retarget epoch number |
| `epoch_progress` | number | Blocks mined in the current epoch |
| `retarget_interval` | number | Blocks per retarget epoch |

**Example response:**

```json
{
  "blocks": 1542,
  "bestblockhash": "000000034a1b...",
  "bits": "1d0fffff",
  "difficulty": 16.0001,
  "peers": 11,
  "retarget_epoch": 77,
  "epoch_progress": 2,
  "retarget_interval": 20
}
```

---

### metrics

Returns internal performance metrics.

```bash
fairchain-cli metrics
```

**Response:** JSON object with counters for blocks mined, blocks received, transactions processed, and other internal metrics.

---

## HTTP Method Requirements

State-changing endpoints require POST to prevent CSRF attacks. Read-only endpoints accept GET.

| Endpoint | Method | Reason |
|----------|--------|--------|
| `stop` | POST | Shuts down the node |
| `sendtoaddress` | POST | Sends funds |
| `sendrawtransaction` | POST | Submits transaction |
| `importprivkey` | POST | Modifies wallet |
| `signrawtransactionwithwallet` | POST | Signs transaction |
| `encryptwallet` | POST | Encrypts wallet |
| `walletpassphrase` | POST | Unlocks wallet |
| `settxfee` | POST | Changes fee rate |
| `backupwallet` | POST | Writes backup file |
| `submitblock` | POST | Submits block (binary body) |

All other endpoints accept GET.

The CLI currently sends GET for all commands. For state-changing operations via the CLI, the server accepts the request. When using `curl` directly, use `-X POST`.

---

## Error Responses

All endpoints return errors in a consistent format:

```json
{
  "error": "description of the error"
}
```

Common HTTP status codes:

| Code | Meaning |
|------|---------|
| 200 | Success |
| 400 | Bad request (missing/invalid parameters) |
| 401 | Unauthorized (authentication required) |
| 403 | Forbidden (wallet locked, unlock required) |
| 404 | Not found (block, transaction, or peer not found) |
| 405 | Method not allowed (e.g., GET on a POST-only endpoint) |
| 503 | Service unavailable (wallet not loaded) |

---

## Quick Reference

| Command | Parameters | Method | Description |
|---------|------------|--------|-------------|
| `getblockchaininfo` | — | GET | Blockchain state |
| `getblockcount` | — | GET | Current height |
| `getbestblockhash` | — | GET | Tip block hash |
| `getblockhash` | `<height>` | GET | Hash at height |
| `getblock` | `<hash>` | GET | Block by hash |
| `getblockbyheight` | `<height>` | GET | Block by height |
| `getdifficulty` | — | GET | Current difficulty |
| `getnetworkinfo` | — | GET | Network state |
| `getpeerinfo` | — | GET | Peer details |
| `getconnectioncount` | — | GET | Peer count |
| `addnode` | `<ip:port>` | GET | Connect to peer |
| `disconnectnode` | `<address>` | GET | Disconnect peer |
| `getmempoolinfo` | — | GET | Mempool state |
| `getrawmempool` | `[true]` | GET | Mempool txids |
| `getmempoolentry` | `<txid>` | GET | Mempool tx details |
| `gettxout` | `<txid> <n>` | GET | Unspent output |
| `gettxoutsetinfo` | — | GET | UTXO set stats |
| `submitblock` | POST body | POST | Submit a block |
| `getnewaddress` | — | GET | New receiving address |
| `getbalance` | `[minconf]` | GET | Wallet balance |
| `listunspent` | `[minconf] [maxconf]` | GET | Wallet UTXOs |
| `sendtoaddress` | `<addr> <amount>` | POST | Send coins |
| `getwalletinfo` | — | GET | Wallet status |
| `dumpprivkey` | `<address>` | GET | Export private key (WIF) |
| `importprivkey` | `<key>` | POST | Import private key (WIF/hex) |
| `validateaddress` | `<address>` | GET | Validate address |
| `getrawchangeaddress` | — | GET | New change address |
| `settxfee` | `<amount>` | POST | Set fee rate (max 10,000) |
| `sendrawtransaction` | `<hex>` | POST | Submit raw transaction |
| `signrawtransactionwithwallet` | `<hex>` | POST | Sign raw transaction |
| `getreceivedbyaddress` | `<addr> [minconf]` | GET | Total received |
| `listaddressgroupings` | — | GET | Address groupings |
| `backupwallet` | `<destination>` | POST | Backup wallet file |
| `getaddressesbylabel` | — | GET | Addresses by label |
| `dumpwallet` | — | GET | Dump wallet data |
| `listtransactions` | `[count]` | GET | Transaction history |
| `gettransaction` | `<txid>` | GET | Transaction details |
| `encryptwallet` | `<passphrase>` | POST | Encrypt wallet |
| `walletpassphrase` | `<pass> [timeout]` | POST | Unlock wallet |
| `walletlock` | — | GET | Lock wallet |
| `getinfo` | — | GET | Node overview |
| `stop` | — | POST | Shutdown daemon |
| `getchainstatus` | — | GET | Chain + retarget info |
| `metrics` | — | GET | Internal metrics |
