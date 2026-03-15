package utxo

import (
	"encoding/binary"
	"fmt"
	"sync"

	"github.com/bams-repo/fairchain/internal/crypto"
	"github.com/bams-repo/fairchain/internal/types"
)

// UtxoEntry represents a single unspent transaction output in the UTXO set.
type UtxoEntry struct {
	Value      uint64
	PkScript   []byte
	Height     uint32 // Block height where this output was created.
	IsCoinbase bool
}

// Serialize encodes a UtxoEntry into bytes for persistent storage.
// Format: value(8 LE) + height(4 LE) + flags(1) + varint(pkscript len) + pkscript
func (e *UtxoEntry) Serialize() []byte {
	flags := byte(0)
	if e.IsCoinbase {
		flags |= 0x01
	}
	size := 8 + 4 + 1 + types.VarIntSize(uint64(len(e.PkScript))) + len(e.PkScript)
	buf := make([]byte, size)
	binary.LittleEndian.PutUint64(buf[0:8], e.Value)
	binary.LittleEndian.PutUint32(buf[8:12], e.Height)
	buf[12] = flags
	n := types.PutVarInt(buf[13:], uint64(len(e.PkScript)))
	copy(buf[13+n:], e.PkScript)
	return buf
}

// DeserializeUtxoEntry decodes a UtxoEntry from bytes.
func DeserializeUtxoEntry(data []byte) (*UtxoEntry, error) {
	if len(data) < 13 {
		return nil, fmt.Errorf("utxo entry too short: %d bytes", len(data))
	}
	e := &UtxoEntry{
		Value:      binary.LittleEndian.Uint64(data[0:8]),
		Height:     binary.LittleEndian.Uint32(data[8:12]),
		IsCoinbase: data[12]&0x01 != 0,
	}
	rest := data[13:]
	scriptLen, err := types.ReadVarIntFromBytes(rest)
	if err != nil {
		return nil, fmt.Errorf("read pkscript length: %w", err)
	}
	viSize := types.VarIntSize(scriptLen)
	if len(rest) < viSize+int(scriptLen) {
		return nil, fmt.Errorf("utxo entry truncated")
	}
	e.PkScript = make([]byte, scriptLen)
	copy(e.PkScript, rest[viSize:viSize+int(scriptLen)])
	return e, nil
}

// OutpointKey creates a unique 36-byte key from a tx hash and output index,
// suitable for use as a map key or bbolt key.
func OutpointKey(txHash types.Hash, index uint32) [36]byte {
	var key [36]byte
	copy(key[:32], txHash[:])
	binary.LittleEndian.PutUint32(key[32:], index)
	return key
}

// SpentOutput records a UTXO that was spent in a block, for disconnect/rollback.
type SpentOutput struct {
	OutPoint types.OutPoint
	Entry    UtxoEntry
}

// BlockUndoData holds the information needed to disconnect a block from the UTXO set.
type BlockUndoData struct {
	SpentOutputs []SpentOutput
}

// SerializeUndoData encodes BlockUndoData for persistent storage.
// Format: varint(count) + for each: hash(32) + index(4 LE) + entry bytes
func SerializeUndoData(undo *BlockUndoData) []byte {
	size := types.VarIntSize(uint64(len(undo.SpentOutputs)))
	for _, so := range undo.SpentOutputs {
		entryBytes := so.Entry.Serialize()
		size += 32 + 4 + types.VarIntSize(uint64(len(entryBytes))) + len(entryBytes)
	}
	buf := make([]byte, 0, size)
	viBuf := make([]byte, 9)
	n := types.PutVarInt(viBuf, uint64(len(undo.SpentOutputs)))
	buf = append(buf, viBuf[:n]...)
	for _, so := range undo.SpentOutputs {
		buf = append(buf, so.OutPoint.Hash[:]...)
		idxBuf := make([]byte, 4)
		binary.LittleEndian.PutUint32(idxBuf, so.OutPoint.Index)
		buf = append(buf, idxBuf...)
		entryBytes := so.Entry.Serialize()
		n = types.PutVarInt(viBuf, uint64(len(entryBytes)))
		buf = append(buf, viBuf[:n]...)
		buf = append(buf, entryBytes...)
	}
	return buf
}

// DeserializeUndoData decodes BlockUndoData from bytes.
func DeserializeUndoData(data []byte) (*BlockUndoData, error) {
	if len(data) == 0 {
		return &BlockUndoData{}, nil
	}
	count, err := types.ReadVarIntFromBytes(data)
	if err != nil {
		return nil, fmt.Errorf("read undo count: %w", err)
	}
	offset := types.VarIntSize(count)
	undo := &BlockUndoData{SpentOutputs: make([]SpentOutput, 0, count)}
	for i := uint64(0); i < count; i++ {
		if offset+36 > len(data) {
			return nil, fmt.Errorf("undo data truncated at entry %d", i)
		}
		var so SpentOutput
		copy(so.OutPoint.Hash[:], data[offset:offset+32])
		so.OutPoint.Index = binary.LittleEndian.Uint32(data[offset+32 : offset+36])
		offset += 36

		entryLen, err := types.ReadVarIntFromBytes(data[offset:])
		if err != nil {
			return nil, fmt.Errorf("read entry length at %d: %w", i, err)
		}
		viSize := types.VarIntSize(entryLen)
		offset += viSize
		if offset+int(entryLen) > len(data) {
			return nil, fmt.Errorf("undo entry %d truncated", i)
		}
		entry, err := DeserializeUtxoEntry(data[offset : offset+int(entryLen)])
		if err != nil {
			return nil, fmt.Errorf("deserialize undo entry %d: %w", i, err)
		}
		so.Entry = *entry
		offset += int(entryLen)
		undo.SpentOutputs = append(undo.SpentOutputs, so)
	}
	return undo, nil
}

// Set is an in-memory UTXO set with methods to connect and disconnect blocks.
// Thread-safe for concurrent reads; writes must be serialized by the caller.
type Set struct {
	mu      sync.RWMutex
	entries map[[36]byte]*UtxoEntry
}

// NewSet creates a new empty UTXO set.
func NewSet() *Set {
	return &Set{
		entries: make(map[[36]byte]*UtxoEntry),
	}
}

// Get returns the UTXO entry for the given outpoint, or nil if not found.
func (s *Set) Get(txHash types.Hash, index uint32) *UtxoEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	key := OutpointKey(txHash, index)
	return s.entries[key]
}

// Has returns true if the outpoint exists in the UTXO set.
func (s *Set) Has(txHash types.Hash, index uint32) bool {
	return s.Get(txHash, index) != nil
}

// Add inserts a UTXO entry. Used during block connect and UTXO set rebuild.
func (s *Set) Add(txHash types.Hash, index uint32, entry *UtxoEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := OutpointKey(txHash, index)
	s.entries[key] = entry
}

// Remove deletes a UTXO entry. Returns the removed entry or nil.
func (s *Set) Remove(txHash types.Hash, index uint32) *UtxoEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := OutpointKey(txHash, index)
	entry := s.entries[key]
	delete(s.entries, key)
	return entry
}

// Count returns the number of UTXOs in the set.
func (s *Set) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.entries)
}

// TotalValue returns the sum of all UTXO values. Returns math.MaxUint64 if
// the sum would overflow, indicating a corrupted or inflated UTXO set.
func (s *Set) TotalValue() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var total uint64
	for _, e := range s.entries {
		if total+e.Value < total {
			return ^uint64(0) // overflow sentinel
		}
		total += e.Value
	}
	return total
}

// ConnectBlock applies a block's transactions to the UTXO set atomically.
// All changes are computed first; the UTXO set is only mutated if every
// transaction validates successfully. Returns undo data for disconnect.
func (s *Set) ConnectBlock(block *types.Block, height uint32) (*BlockUndoData, error) {
	undo := &BlockUndoData{}

	// Collect all mutations before applying any of them.
	type addEntry struct {
		key   [36]byte
		entry *UtxoEntry
	}
	var toRemove [][36]byte
	var toAdd []addEntry

	// Track outpoints consumed within this block so that a later tx can spend
	// an output created by an earlier tx in the same block, and so that
	// double-spends within the block are caught.
	spentInBlock := make(map[[36]byte]struct{})
	createdInBlock := make(map[[36]byte]*UtxoEntry)

	for txIdx := range block.Transactions {
		tx := &block.Transactions[txIdx]
		txHash, err := crypto.HashTransaction(tx)
		if err != nil {
			return nil, fmt.Errorf("hash tx %d: %w", txIdx, err)
		}

		if !tx.IsCoinbase() {
			seenInTx := make(map[[36]byte]struct{}, len(tx.Inputs))
			for _, in := range tx.Inputs {
				key := OutpointKey(in.PreviousOutPoint.Hash, in.PreviousOutPoint.Index)

				if _, dup := seenInTx[key]; dup {
					return nil, fmt.Errorf("tx %s has duplicate input %s:%d",
						txHash, in.PreviousOutPoint.Hash, in.PreviousOutPoint.Index)
				}
				seenInTx[key] = struct{}{}

				if _, already := spentInBlock[key]; already {
					return nil, fmt.Errorf("tx %s input references already-spent outpoint %s:%d within block",
						txHash, in.PreviousOutPoint.Hash, in.PreviousOutPoint.Index)
				}

				// Check in-block created outputs first, then the persistent set.
				var entry *UtxoEntry
				if e, ok := createdInBlock[key]; ok {
					entry = e
				} else {
					s.mu.RLock()
					entry = s.entries[key]
					s.mu.RUnlock()
				}

				if entry == nil {
					return nil, fmt.Errorf("tx %s input references missing UTXO %s:%d",
						txHash, in.PreviousOutPoint.Hash, in.PreviousOutPoint.Index)
				}

				undo.SpentOutputs = append(undo.SpentOutputs, SpentOutput{
					OutPoint: in.PreviousOutPoint,
					Entry:    *entry,
				})
				spentInBlock[key] = struct{}{}
				toRemove = append(toRemove, key)
				delete(createdInBlock, key)
			}
		}

		for outIdx, out := range tx.Outputs {
			key := OutpointKey(txHash, uint32(outIdx))
			entry := &UtxoEntry{
				Value:      out.Value,
				PkScript:   out.PkScript,
				Height:     height,
				IsCoinbase: tx.IsCoinbase(),
			}
			createdInBlock[key] = entry
			toAdd = append(toAdd, addEntry{key: key, entry: entry})
		}
	}

	// All transactions validated — apply mutations atomically.
	// Outputs spent within the same block must not appear in the final UTXO set.
	s.mu.Lock()
	for _, key := range toRemove {
		delete(s.entries, key)
	}
	for _, a := range toAdd {
		if _, spent := spentInBlock[a.key]; !spent {
			s.entries[a.key] = a.entry
		}
	}
	s.mu.Unlock()

	return undo, nil
}

// DisconnectBlock reverses a block's effect on the UTXO set using undo data.
// All mutations are computed first and applied atomically under a single lock,
// ensuring no partial state is visible to concurrent readers on error.
func (s *Set) DisconnectBlock(block *types.Block, undo *BlockUndoData) error {
	// Phase 1: compute all keys to remove (no mutation yet).
	var toRemove [][36]byte
	for txIdx := len(block.Transactions) - 1; txIdx >= 0; txIdx-- {
		tx := &block.Transactions[txIdx]
		txHash, err := crypto.HashTransaction(tx)
		if err != nil {
			return fmt.Errorf("hash tx %d: %w", txIdx, err)
		}
		for outIdx := range tx.Outputs {
			toRemove = append(toRemove, OutpointKey(txHash, uint32(outIdx)))
		}
	}

	// Phase 2: apply all mutations atomically.
	s.mu.Lock()
	for _, key := range toRemove {
		delete(s.entries, key)
	}
	for _, spent := range undo.SpentOutputs {
		entryCopy := spent.Entry
		entryCopy.PkScript = make([]byte, len(spent.Entry.PkScript))
		copy(entryCopy.PkScript, spent.Entry.PkScript)
		key := OutpointKey(spent.OutPoint.Hash, spent.OutPoint.Index)
		s.entries[key] = &entryCopy
	}
	s.mu.Unlock()

	return nil
}

// ConnectGenesis applies the genesis block to an empty UTXO set.
// Genesis has no inputs to validate, only outputs to add.
// Outputs with legacy placeholder scripts (empty or single-byte {0x00}) are
// excluded from the UTXO set entirely, matching Bitcoin Core's behavior of
// never inserting the genesis coinbase into the UTXO set.
func (s *Set) ConnectGenesis(block *types.Block) error {
	for txIdx := range block.Transactions {
		tx := &block.Transactions[txIdx]
		txHash, err := crypto.HashTransaction(tx)
		if err != nil {
			return fmt.Errorf("hash genesis tx %d: %w", txIdx, err)
		}
		for outIdx, out := range tx.Outputs {
			if isLegacyPlaceholder(out.PkScript) {
				continue
			}
			s.Add(txHash, uint32(outIdx), &UtxoEntry{
				Value:      out.Value,
				PkScript:   out.PkScript,
				Height:     0,
				IsCoinbase: tx.IsCoinbase(),
			})
		}
	}
	return nil
}

func isLegacyPlaceholder(pk []byte) bool {
	return len(pk) == 0 || (len(pk) == 1 && pk[0] == 0x00)
}

// Entries returns a deep-copy snapshot of all UTXO entries (for persistence).
// Callers may freely mutate the returned entries without affecting the live set.
func (s *Set) Entries() map[[36]byte]*UtxoEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snapshot := make(map[[36]byte]*UtxoEntry, len(s.entries))
	for k, v := range s.entries {
		entryCopy := *v
		entryCopy.PkScript = make([]byte, len(v.PkScript))
		copy(entryCopy.PkScript, v.PkScript)
		snapshot[k] = &entryCopy
	}
	return snapshot
}

// ForEach iterates over all UTXO entries, calling fn with the tx hash, output
// index, and a deep copy of the entry. Callers cannot corrupt the live set.
func (s *Set) ForEach(fn func(txHash types.Hash, index uint32, entry *UtxoEntry)) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for key, entry := range s.entries {
		var txHash types.Hash
		copy(txHash[:], key[:32])
		index := binary.LittleEndian.Uint32(key[32:])
		entryCopy := *entry
		entryCopy.PkScript = make([]byte, len(entry.PkScript))
		copy(entryCopy.PkScript, entry.PkScript)
		fn(txHash, index, &entryCopy)
	}
}
