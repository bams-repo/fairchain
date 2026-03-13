package store

import (
	"math/big"

	"github.com/bams-repo/fairchain/internal/crypto"
	"github.com/bams-repo/fairchain/internal/types"
)

// BlockStore abstracts persistent storage for blocks, headers, chain state, and UTXOs.
// Implementations must be safe for concurrent read access.
// Write operations are expected to be serialized by the caller (chain manager).
type BlockStore interface {
	// Block data (flat files).
	HasBlock(hash types.Hash) (bool, error)
	WriteBlock(hash types.Hash, block *types.Block) (fileNum, offset, size uint32, err error)
	ReadBlock(fileNum, offset, size uint32) (*types.Block, error)
	WriteUndo(fileNum uint32, data []byte) (offset, size uint32, err error)
	ReadUndo(fileNum, offset, size uint32) ([]byte, error)

	// Block index (LevelDB).
	PutBlockIndex(hash types.Hash, rec *DiskBlockIndex) error
	GetBlockIndex(hash types.Hash) (*DiskBlockIndex, error)
	ForEachBlockIndex(fn func(hash types.Hash, rec *DiskBlockIndex) error) error

	// Chain tip (stored in block index).
	GetChainTip() (types.Hash, uint32, error)
	PutChainTip(hash types.Hash, height uint32) error

	// Chainstate / UTXO (LevelDB).
	PutUtxo(txHash types.Hash, index uint32, data []byte) error
	GetUtxo(txHash types.Hash, index uint32) ([]byte, error)
	DeleteUtxo(txHash types.Hash, index uint32) error
	HasUtxo(txHash types.Hash, index uint32) (bool, error)
	NewUtxoWriteBatch() *ChainstateWriteBatch
	FlushUtxoBatch(wb *ChainstateWriteBatch) error
	GetBestBlock() (types.Hash, error)
	PutBestBlock(hash types.Hash) error
	UtxoCount() (int, error)

	// Legacy compatibility: read a full block by hash (uses index + flat file).
	GetBlock(hash types.Hash) (*types.Block, error)
	GetHeader(hash types.Hash) (*types.BlockHeader, error)

	Close() error
}

// PeerStore abstracts persistent storage for peer addresses.
type PeerStore interface {
	// GetPeers returns known peer addresses.
	GetPeers() ([]string, error)

	// PutPeer stores a peer address.
	PutPeer(addr string) error

	// RemovePeer removes a peer address.
	RemovePeer(addr string) error

	// Close releases storage resources.
	Close() error
}

// CalcWork delegates to the canonical crypto.CalcWork implementation.
func CalcWork(bits uint32) *big.Int {
	return crypto.CalcWork(bits)
}
