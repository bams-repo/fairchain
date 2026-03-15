package store

import (
	"encoding/binary"
	"fmt"

	"github.com/bams-repo/fairchain/internal/types"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/util"
)

// Chainstate key prefixes matching Bitcoin Core conventions.
var (
	prefixUTXO     = byte('C') // 'C' + txid(32) + index(4 LE) -> serialized UtxoEntry
	keyBestBlock   = []byte("B") // -> best block hash (32 bytes)
)

// ChainstateDB wraps a LevelDB instance for the persistent UTXO set.
type ChainstateDB struct {
	db *leveldb.DB
}

// NewChainstateDB opens or creates a LevelDB chainstate database.
func NewChainstateDB(path string) (*ChainstateDB, error) {
	db, err := leveldb.OpenFile(path, &opt.Options{
		BlockCacheCapacity:     16 * 1024 * 1024, // 16 MiB cache.
		WriteBuffer:           8 * 1024 * 1024,   // 8 MiB write buffer.
		CompactionTableSize:   4 * 1024 * 1024,
	})
	if err != nil {
		return nil, fmt.Errorf("open chainstate: %w", err)
	}
	return &ChainstateDB{db: db}, nil
}

func utxoKey(txHash types.Hash, index uint32) []byte {
	key := make([]byte, 1+types.HashSize+4)
	key[0] = prefixUTXO
	copy(key[1:1+types.HashSize], txHash[:])
	binary.LittleEndian.PutUint32(key[1+types.HashSize:], index)
	return key
}

// PutUtxo stores a UTXO entry.
func (cs *ChainstateDB) PutUtxo(txHash types.Hash, index uint32, data []byte) error {
	return cs.db.Put(utxoKey(txHash, index), data, nil)
}

// GetUtxo retrieves a UTXO entry.
func (cs *ChainstateDB) GetUtxo(txHash types.Hash, index uint32) ([]byte, error) {
	data, err := cs.db.Get(utxoKey(txHash, index), nil)
	if err != nil {
		return nil, err
	}
	result := make([]byte, len(data))
	copy(result, data)
	return result, nil
}

// DeleteUtxo removes a UTXO entry.
func (cs *ChainstateDB) DeleteUtxo(txHash types.Hash, index uint32) error {
	return cs.db.Delete(utxoKey(txHash, index), nil)
}

// HasUtxo checks if a UTXO exists.
func (cs *ChainstateDB) HasUtxo(txHash types.Hash, index uint32) (bool, error) {
	return cs.db.Has(utxoKey(txHash, index), nil)
}

// GetBestBlock returns the best block hash stored in chainstate.
func (cs *ChainstateDB) GetBestBlock() (types.Hash, error) {
	data, err := cs.db.Get(keyBestBlock, nil)
	if err != nil {
		return types.ZeroHash, fmt.Errorf("get best block: %w", err)
	}
	if len(data) != types.HashSize {
		return types.ZeroHash, fmt.Errorf("corrupt best block data: %d bytes", len(data))
	}
	var hash types.Hash
	copy(hash[:], data)
	return hash, nil
}

// PutBestBlock stores the best block hash.
func (cs *ChainstateDB) PutBestBlock(hash types.Hash) error {
	return cs.db.Put(keyBestBlock, hash[:], nil)
}

// ChainstateWriteBatch accumulates UTXO changes for atomic application.
type ChainstateWriteBatch struct {
	batch *leveldb.Batch
}

// NewWriteBatch creates a new write batch.
func (cs *ChainstateDB) NewWriteBatch() *ChainstateWriteBatch {
	return &ChainstateWriteBatch{batch: new(leveldb.Batch)}
}

// PutUtxo adds a UTXO put operation to the batch.
func (wb *ChainstateWriteBatch) PutUtxo(txHash types.Hash, index uint32, data []byte) {
	wb.batch.Put(utxoKey(txHash, index), data)
}

// DeleteUtxo adds a UTXO delete operation to the batch.
func (wb *ChainstateWriteBatch) DeleteUtxo(txHash types.Hash, index uint32) {
	wb.batch.Delete(utxoKey(txHash, index))
}

// PutBestBlock adds a best-block update to the batch.
func (wb *ChainstateWriteBatch) PutBestBlock(hash types.Hash) {
	wb.batch.Put(keyBestBlock, hash[:])
}

// Flush atomically applies all batched operations.
func (cs *ChainstateDB) Flush(wb *ChainstateWriteBatch) error {
	return cs.db.Write(wb.batch, &opt.WriteOptions{Sync: true})
}

// ForEachUtxo iterates over all UTXO entries in the chainstate, calling fn
// for each one. Used to populate the in-memory UTXO set on startup.
func (cs *ChainstateDB) ForEachUtxo(fn func(txHash types.Hash, index uint32, data []byte) error) error {
	iter := cs.db.NewIterator(util.BytesPrefix([]byte{prefixUTXO}), nil)
	defer iter.Release()
	for iter.Next() {
		key := iter.Key()
		if len(key) != 1+types.HashSize+4 {
			continue
		}
		var txHash types.Hash
		copy(txHash[:], key[1:1+types.HashSize])
		idx := binary.LittleEndian.Uint32(key[1+types.HashSize:])
		val := make([]byte, len(iter.Value()))
		copy(val, iter.Value())
		if err := fn(txHash, idx, val); err != nil {
			return err
		}
	}
	return iter.Error()
}

// Count returns the number of UTXO entries (for diagnostics).
func (cs *ChainstateDB) Count() (int, error) {
	count := 0
	iter := cs.db.NewIterator(nil, nil)
	defer iter.Release()
	for iter.Next() {
		if len(iter.Key()) > 0 && iter.Key()[0] == prefixUTXO {
			count++
		}
	}
	return count, iter.Error()
}

// Close closes the chainstate database.
func (cs *ChainstateDB) Close() error {
	return cs.db.Close()
}
