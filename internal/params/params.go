package params

import (
	"time"

	"github.com/bams-repo/fairchain/internal/types"
)

// ChainParams defines the complete set of parameters for a blockchain network.
// All consensus-critical values are here so they can be audited in one place.
type ChainParams struct {
	// Network identity
	Name         string     // Human-readable network name.
	DataDirName  string     // Subdirectory under the root data dir (e.g. "testnet2"). Empty = root.
	NetworkMagic [4]byte    // Wire protocol magic bytes for message framing.
	DefaultPort  uint16     // Default TCP listen port.
	AddressPrefix byte      // Future: address version byte for base58/bech32.

	// Genesis
	GenesisBlock types.Block // The fully-defined genesis block.
	GenesisHash  types.Hash  // Precomputed hash of the genesis block header.

	// Block timing
	TargetBlockSpacing  time.Duration // Desired time between blocks.
	RetargetInterval    uint32        // Number of blocks between difficulty adjustments.
	TargetTimespan      time.Duration // RetargetInterval * TargetBlockSpacing.
	MaxTimeFutureDrift  time.Duration // Maximum allowed block timestamp ahead of network time.
	MinTimestampRule    string        // "median-11" or "prev+1" — determines minimum allowed timestamp.

	// Difficulty
	InitialBits      uint32 // Compact target for the genesis block and initial difficulty.
	MinBits          uint32 // Minimum difficulty (maximum target) allowed.
	NoRetarget       bool   // If true, difficulty never changes (regtest mode).

	// Block limits
	MaxBlockSize     uint32 // Maximum serialized block size in bytes.
	MaxBlockTxCount  uint32 // Maximum number of transactions per block.

	// Subsidy
	InitialSubsidy   uint64 // Coinbase reward for the first era (in smallest units).
	SubsidyHalvingInterval uint32 // Blocks between subsidy halvings.

	// Coinbase
	CoinbaseMaturity uint32 // Blocks before coinbase outputs are spendable.

	// Reorg safety
	MaxReorgDepth uint32 // Maximum number of blocks that can be disconnected in a single reorg. 0 = unlimited.

	// Mempool policy (non-consensus, but parameterized per network)
	MaxMempoolSize   uint32 // Maximum number of transactions in mempool.
	MinRelayTxFee    uint64 // Minimum fee for mempool admission (smallest units).

	// Seed nodes
	SeedNodes []string // DNS seeds or static IP:port addresses for peer discovery.

	// Future consensus upgrade activation heights (placeholders).
	// Map from feature name to activation block height.
	ActivationHeights map[string]uint32
}

// CalcSubsidy returns the block subsidy at the given height.
// Uses integer halving: subsidy = InitialSubsidy >> (height / SubsidyHalvingInterval).
func (p *ChainParams) CalcSubsidy(height uint32) uint64 {
	if p.SubsidyHalvingInterval == 0 {
		return p.InitialSubsidy
	}
	halvings := height / p.SubsidyHalvingInterval
	if halvings >= 64 {
		return 0
	}
	return p.InitialSubsidy >> halvings
}
