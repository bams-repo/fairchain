package mempool

import (
	"bytes"
	"fmt"
	"sort"
	"sync"

	"github.com/bams-repo/fairchain/internal/consensus"
	"github.com/bams-repo/fairchain/internal/crypto"
	"github.com/bams-repo/fairchain/internal/params"
	"github.com/bams-repo/fairchain/internal/types"
	"github.com/bams-repo/fairchain/internal/utxo"
)

// TxEntry wraps a transaction with its computed metadata.
type TxEntry struct {
	Tx      *types.Transaction
	Hash    types.Hash
	Fee     uint64
	FeeRate uint64 // Fee per byte of serialized transaction size.
	Size    int
}

// Mempool holds unconfirmed transactions awaiting inclusion in a block.
// Thread-safe for concurrent access from P2P and RPC handlers.
type Mempool struct {
	mu   sync.RWMutex
	txs  map[types.Hash]*TxEntry
	p    *params.ChainParams

	// Track which outpoints are spent by mempool transactions for double-spend detection.
	spentOutpoints map[[36]byte]types.Hash // outpoint key -> spending tx hash

	utxoSet   *utxo.Set
	tipHeight uint32
}

// New creates a new empty mempool.
func New(p *params.ChainParams, utxoSet *utxo.Set) *Mempool {
	return &Mempool{
		txs:            make(map[types.Hash]*TxEntry),
		p:              p,
		spentOutpoints: make(map[[36]byte]types.Hash),
		utxoSet:        utxoSet,
	}
}

// SetTipHeight updates the current chain tip height for maturity checks.
func (m *Mempool) SetTipHeight(height uint32) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tipHeight = height
}

// AddTx validates and adds a transaction to the mempool.
// Returns the transaction hash and fee if accepted.
func (m *Mempool) AddTx(tx *types.Transaction) (types.Hash, error) {
	if tx.IsCoinbase() {
		return types.ZeroHash, fmt.Errorf("coinbase transactions cannot enter mempool")
	}

	if len(tx.Inputs) == 0 {
		return types.ZeroHash, fmt.Errorf("transaction has no inputs")
	}
	if len(tx.Outputs) == 0 {
		return types.ZeroHash, fmt.Errorf("transaction has no outputs")
	}

	txHash, err := crypto.HashTransaction(tx)
	if err != nil {
		return types.ZeroHash, fmt.Errorf("hash transaction: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.txs[txHash]; exists {
		return txHash, fmt.Errorf("transaction %s already in mempool", txHash)
	}

	if uint32(len(m.txs)) >= m.p.MaxMempoolSize {
		return types.ZeroHash, fmt.Errorf("mempool full (%d transactions)", len(m.txs))
	}

	// Check for double-spends against other mempool transactions.
	for _, in := range tx.Inputs {
		key := utxo.OutpointKey(in.PreviousOutPoint.Hash, in.PreviousOutPoint.Index)
		if conflictTx, exists := m.spentOutpoints[key]; exists {
			return types.ZeroHash, fmt.Errorf("tx %s double-spends outpoint %s:%d (conflicts with %s)",
				txHash, in.PreviousOutPoint.Hash, in.PreviousOutPoint.Index, conflictTx)
		}
	}

	// Validate against the UTXO set (input existence, maturity, value).
	fee, err := consensus.ValidateSingleTransaction(tx, m.utxoSet, m.tipHeight, m.p)
	if err != nil {
		return types.ZeroHash, fmt.Errorf("validation failed: %w", err)
	}

	txSize := tx.SerializeSize()
	if txSize == 0 {
		txSize = 1
	}

	// Enforce minimum relay fee.
	if m.p.MinRelayTxFee > 0 && fee < m.p.MinRelayTxFee {
		return types.ZeroHash, fmt.Errorf("fee %d below minimum relay fee %d", fee, m.p.MinRelayTxFee)
	}

	entry := &TxEntry{
		Tx:      tx,
		Hash:    txHash,
		Fee:     fee,
		FeeRate: fee / uint64(txSize),
		Size:    txSize,
	}

	m.txs[txHash] = entry

	for _, in := range tx.Inputs {
		key := utxo.OutpointKey(in.PreviousOutPoint.Hash, in.PreviousOutPoint.Index)
		m.spentOutpoints[key] = txHash
	}

	return txHash, nil
}

// GetTx retrieves a transaction by hash.
func (m *Mempool) GetTx(hash types.Hash) (*types.Transaction, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	entry, ok := m.txs[hash]
	if !ok {
		return nil, false
	}
	return entry.Tx, true
}

// GetTxEntry retrieves a mempool entry with metadata by hash.
func (m *Mempool) GetTxEntry(hash types.Hash) (*TxEntry, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	entry, ok := m.txs[hash]
	return entry, ok
}

// HasTx checks if a transaction is in the mempool.
func (m *Mempool) HasTx(hash types.Hash) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.txs[hash]
	return ok
}

// RemoveTx removes a transaction from the mempool (e.g., after block inclusion).
func (m *Mempool) RemoveTx(hash types.Hash) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.removeTxUnsafe(hash)
}

// RemoveTxs removes multiple transactions (e.g., all txs in a newly accepted block).
func (m *Mempool) RemoveTxs(hashes []types.Hash) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, h := range hashes {
		m.removeTxUnsafe(h)
	}
}

func (m *Mempool) removeTxUnsafe(hash types.Hash) {
	entry, ok := m.txs[hash]
	if !ok {
		return
	}
	for _, in := range entry.Tx.Inputs {
		key := utxo.OutpointKey(in.PreviousOutPoint.Hash, in.PreviousOutPoint.Index)
		delete(m.spentOutpoints, key)
	}
	delete(m.txs, hash)
}

// BlockTemplateResult holds an atomic snapshot of mempool transactions and their
// aggregate fees, suitable for block template construction. This mirrors Bitcoin
// Core's BlockAssembler which snapshots the mempool under a single lock.
type BlockTemplateResult struct {
	Transactions []*types.Transaction
	TotalFees    uint64
}

// BlockTemplate returns an atomic snapshot of all mempool transactions and their
// total fees under a single lock acquisition. This prevents the TOCTOU race
// where transactions could be added between separate GetAll/TotalFees calls,
// which would cause the coinbase to claim fees for transactions not in the block.
func (m *Mempool) BlockTemplate() *BlockTemplateResult {
	m.mu.RLock()
	defer m.mu.RUnlock()

	entries := make([]*TxEntry, 0, len(m.txs))
	for _, e := range m.txs {
		entries = append(entries, e)
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].FeeRate != entries[j].FeeRate {
			return entries[i].FeeRate > entries[j].FeeRate
		}
		return hashLess(entries[i].Hash, entries[j].Hash)
	})

	var totalFees uint64
	txs := make([]*types.Transaction, len(entries))
	for i, e := range entries {
		txs[i] = e.Tx
		prev := totalFees
		totalFees += e.Fee
		if totalFees < prev {
			totalFees = prev
			break
		}
	}
	return &BlockTemplateResult{Transactions: txs, TotalFees: totalFees}
}

// GetAll returns all transactions ordered by fee rate (highest first) for block template building.
func (m *Mempool) GetAll() []*types.Transaction {
	return m.BlockTemplate().Transactions
}

// GetAllEntries returns all mempool entries with metadata, ordered by fee rate.
func (m *Mempool) GetAllEntries() []*TxEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()

	entries := make([]*TxEntry, 0, len(m.txs))
	for _, e := range m.txs {
		entries = append(entries, e)
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].FeeRate != entries[j].FeeRate {
			return entries[i].FeeRate > entries[j].FeeRate
		}
		return hashLess(entries[i].Hash, entries[j].Hash)
	})

	return entries
}

// GetTxHashes returns all transaction hashes in the mempool.
func (m *Mempool) GetTxHashes() []types.Hash {
	m.mu.RLock()
	defer m.mu.RUnlock()
	hashes := make([]types.Hash, 0, len(m.txs))
	for h := range m.txs {
		hashes = append(hashes, h)
	}
	sort.Slice(hashes, func(i, j int) bool {
		return hashLess(hashes[i], hashes[j])
	})
	return hashes
}

// TotalFees returns the sum of all fees in the mempool.
func (m *Mempool) TotalFees() uint64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var total uint64
	for _, e := range m.txs {
		total += e.Fee
	}
	return total
}

// TotalSize returns the total serialized size of all mempool transactions.
func (m *Mempool) TotalSize() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var total int
	for _, e := range m.txs {
		total += e.Size
	}
	return total
}

// Count returns the number of transactions in the mempool.
func (m *Mempool) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.txs)
}

// EvictLowestFeeRate removes the transaction with the lowest fee rate.
// Returns true if a transaction was evicted.
func (m *Mempool) EvictLowestFeeRate() bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.txs) == 0 {
		return false
	}

	var lowestHash types.Hash
	var lowestRate uint64
	first := true
	for _, e := range m.txs {
		if first || e.FeeRate < lowestRate || (e.FeeRate == lowestRate && hashLess(e.Hash, lowestHash)) {
			lowestHash = e.Hash
			lowestRate = e.FeeRate
			first = false
		}
	}

	m.removeTxUnsafe(lowestHash)
	return true
}

// IsOutpointSpent checks if an outpoint is already spent by a mempool transaction.
func (m *Mempool) IsOutpointSpent(txHash types.Hash, index uint32) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	key := utxo.OutpointKey(txHash, index)
	_, exists := m.spentOutpoints[key]
	return exists
}

// DumpToBytes serializes all mempool transactions for persistence.
// Format: varint(count) + [tx1_bytes][tx2_bytes]...
func (m *Mempool) DumpToBytes() []byte {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if len(m.txs) == 0 {
		return nil
	}

	var buf []byte
	countBuf := make([]byte, 9)
	n := types.PutVarInt(countBuf, uint64(len(m.txs)))
	buf = append(buf, countBuf[:n]...)

	for _, entry := range m.txs {
		txBytes, err := entry.Tx.SerializeToBytes()
		if err != nil {
			continue
		}
		buf = append(buf, txBytes...)
	}

	return buf
}

// LoadFromBytes deserializes transactions from a mempool.dat dump and re-validates them.
// Returns the number of transactions successfully loaded.
func (m *Mempool) LoadFromBytes(data []byte) int {
	if len(data) == 0 {
		return 0
	}

	count, err := types.ReadVarIntFromBytes(data)
	if err != nil {
		return 0
	}
	offset := types.VarIntSize(count)

	loaded := 0
	for i := uint64(0); i < count; i++ {
		if offset >= len(data) {
			break
		}
		var tx types.Transaction
		reader := bytes.NewReader(data[offset:])
		if err := tx.Deserialize(reader); err != nil {
			break
		}
		consumed := len(data) - offset - reader.Len()
		offset += consumed

		if _, err := m.AddTx(&tx); err == nil {
			loaded++
		}
	}

	return loaded
}

func hashLess(a, b types.Hash) bool {
	for i := types.HashSize - 1; i >= 0; i-- {
		if a[i] < b[i] {
			return true
		}
		if a[i] > b[i] {
			return false
		}
	}
	return false
}
