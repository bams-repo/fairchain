package store

import (
	"encoding/binary"
	"fmt"

	"github.com/bams-repo/fairchain/internal/types"
	"github.com/syndtr/goleveldb/leveldb/opt"
)

// Chain tip keys stored in the block index LevelDB.
var (
	keyChainTip = []byte("T") // -> hash(32) + height(4 BE)
)

// FileStore implements BlockStore using flat files + LevelDB.
// It composes BlockFileManager (blk/rev files), BlockIndex (LevelDB), and ChainstateDB (LevelDB).
type FileStore struct {
	files      *BlockFileManager
	index      *BlockIndex
	chainstate *ChainstateDB
}

// NewFileStore creates a composite BlockStore backed by flat files and LevelDB.
func NewFileStore(blocksDir, blockIndexDir, chainstateDir string, magic [4]byte) (*FileStore, error) {
	files, err := NewBlockFileManager(blocksDir, magic)
	if err != nil {
		return nil, fmt.Errorf("init block files: %w", err)
	}

	index, err := NewBlockIndex(blockIndexDir)
	if err != nil {
		return nil, fmt.Errorf("init block index: %w", err)
	}

	chainstate, err := NewChainstateDB(chainstateDir)
	if err != nil {
		index.Close()
		return nil, fmt.Errorf("init chainstate: %w", err)
	}

	return &FileStore{
		files:      files,
		index:      index,
		chainstate: chainstate,
	}, nil
}

// HasBlock checks if a block exists in the index.
func (fs *FileStore) HasBlock(hash types.Hash) (bool, error) {
	return fs.index.HasBlock(hash)
}

// WriteBlock writes a block to flat files and returns its location.
func (fs *FileStore) WriteBlock(hash types.Hash, block *types.Block) (fileNum, offset, size uint32, err error) {
	return fs.files.WriteBlock(block)
}

// ReadBlock reads a block from flat files.
func (fs *FileStore) ReadBlock(fileNum, offset, size uint32) (*types.Block, error) {
	return fs.files.ReadBlock(fileNum, offset, size)
}

// WriteUndo writes undo data to the rev*.dat file for the given block file.
func (fs *FileStore) WriteUndo(fileNum uint32, data []byte) (offset, size uint32, err error) {
	return fs.files.WriteUndo(fileNum, data)
}

// ReadUndo reads undo data from a rev*.dat file.
func (fs *FileStore) ReadUndo(fileNum, offset, size uint32) ([]byte, error) {
	return fs.files.ReadUndo(fileNum, offset, size)
}

// PutBlockIndex stores a DiskBlockIndex record.
func (fs *FileStore) PutBlockIndex(hash types.Hash, rec *DiskBlockIndex) error {
	return fs.index.PutBlockIndex(hash, rec)
}

// GetBlockIndex retrieves a DiskBlockIndex record.
func (fs *FileStore) GetBlockIndex(hash types.Hash) (*DiskBlockIndex, error) {
	return fs.index.GetBlockIndex(hash)
}

// ForEachBlockIndex iterates over all block index entries.
func (fs *FileStore) ForEachBlockIndex(fn func(hash types.Hash, rec *DiskBlockIndex) error) error {
	return fs.index.ForEachBlock(fn)
}

// GetChainTip returns the stored chain tip hash and height.
func (fs *FileStore) GetChainTip() (types.Hash, uint32, error) {
	data, err := fs.index.db.Get(keyChainTip, nil)
	if err != nil {
		return types.ZeroHash, 0, fmt.Errorf("chain tip not set: %w", err)
	}
	if len(data) != types.HashSize+4 {
		return types.ZeroHash, 0, fmt.Errorf("corrupt chain tip: %d bytes", len(data))
	}
	var hash types.Hash
	copy(hash[:], data[:types.HashSize])
	height := binary.BigEndian.Uint32(data[types.HashSize:])
	return hash, height, nil
}

// PutChainTip stores the chain tip hash and height with synchronous writes.
func (fs *FileStore) PutChainTip(hash types.Hash, height uint32) error {
	data := make([]byte, types.HashSize+4)
	copy(data[:types.HashSize], hash[:])
	binary.BigEndian.PutUint32(data[types.HashSize:], height)
	return fs.index.db.Put(keyChainTip, data, &opt.WriteOptions{Sync: true})
}

// PutUtxo stores a UTXO entry in chainstate.
func (fs *FileStore) PutUtxo(txHash types.Hash, index uint32, data []byte) error {
	return fs.chainstate.PutUtxo(txHash, index, data)
}

// GetUtxo retrieves a UTXO entry from chainstate.
func (fs *FileStore) GetUtxo(txHash types.Hash, index uint32) ([]byte, error) {
	return fs.chainstate.GetUtxo(txHash, index)
}

// DeleteUtxo removes a UTXO entry from chainstate.
func (fs *FileStore) DeleteUtxo(txHash types.Hash, index uint32) error {
	return fs.chainstate.DeleteUtxo(txHash, index)
}

// HasUtxo checks if a UTXO exists in chainstate.
func (fs *FileStore) HasUtxo(txHash types.Hash, index uint32) (bool, error) {
	return fs.chainstate.HasUtxo(txHash, index)
}

// NewUtxoWriteBatch creates a new write batch for atomic UTXO updates.
func (fs *FileStore) NewUtxoWriteBatch() *ChainstateWriteBatch {
	return fs.chainstate.NewWriteBatch()
}

// FlushUtxoBatch atomically applies a batch of UTXO changes.
func (fs *FileStore) FlushUtxoBatch(wb *ChainstateWriteBatch) error {
	return fs.chainstate.Flush(wb)
}

// GetBestBlock returns the best block hash from chainstate.
func (fs *FileStore) GetBestBlock() (types.Hash, error) {
	return fs.chainstate.GetBestBlock()
}

// PutBestBlock stores the best block hash in chainstate.
func (fs *FileStore) PutBestBlock(hash types.Hash) error {
	return fs.chainstate.PutBestBlock(hash)
}

// UtxoCount returns the number of UTXO entries.
func (fs *FileStore) UtxoCount() (int, error) {
	return fs.chainstate.Count()
}

// ForEachUtxo iterates over all UTXO entries in the chainstate.
func (fs *FileStore) ForEachUtxo(fn func(txHash types.Hash, index uint32, data []byte) error) error {
	return fs.chainstate.ForEachUtxo(fn)
}

// GetBlock retrieves a full block by hash using the index + flat files.
func (fs *FileStore) GetBlock(hash types.Hash) (*types.Block, error) {
	rec, err := fs.index.GetBlockIndex(hash)
	if err != nil {
		return nil, fmt.Errorf("block %s not found in index: %w", hash, err)
	}
	if rec.Status&StatusHaveData == 0 {
		return nil, fmt.Errorf("block %s has no data on disk", hash)
	}
	return fs.files.ReadBlock(rec.FileNum, rec.DataPos, rec.DataSize)
}

// GetHeader retrieves a block header by hash from the index.
func (fs *FileStore) GetHeader(hash types.Hash) (*types.BlockHeader, error) {
	rec, err := fs.index.GetBlockIndex(hash)
	if err != nil {
		return nil, fmt.Errorf("header %s not found: %w", hash, err)
	}
	h := rec.Header
	return &h, nil
}

// Close releases all storage resources.
func (fs *FileStore) Close() error {
	var firstErr error
	if err := fs.chainstate.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	if err := fs.index.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}
