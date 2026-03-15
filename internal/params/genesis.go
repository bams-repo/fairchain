package params

import (
	"github.com/bams-repo/fairchain/internal/types"
)

// GenesisConfig holds the inputs needed to construct a genesis block.
// This is separate from ChainParams so the genesis mining tool can
// operate on a config before the full params are finalized.
type GenesisConfig struct {
	NetworkName     string
	CoinbaseMessage []byte   // Arbitrary data embedded in the coinbase (e.g., headline).
	Timestamp       uint32   // Unix timestamp for the genesis block.
	Bits            uint32   // Initial difficulty target in compact form.
	Version         uint32   // Block version.
	Reward          uint64   // Coinbase reward value.
	RewardScript    []byte   // PkScript for the coinbase output (recipient placeholder).
	ExtraOutputs    []types.TxOutput // Additional coinbase outputs (e.g., premine burn).
}

// BuildGenesisBlock constructs a genesis block from config.
// The nonce is set to 0; the caller must mine it to find a valid nonce.
func BuildGenesisBlock(cfg GenesisConfig) types.Block {
	outputs := []types.TxOutput{
		{
			Value:    cfg.Reward,
			PkScript: cfg.RewardScript,
		},
	}
	outputs = append(outputs, cfg.ExtraOutputs...)

	coinbaseTx := types.Transaction{
		Version: 1,
		Inputs: []types.TxInput{
			{
				PreviousOutPoint: types.CoinbaseOutPoint,
				SignatureScript:  cfg.CoinbaseMessage,
				Sequence:         0xFFFFFFFF,
			},
		},
		Outputs:  outputs,
		LockTime: 0,
	}

	return types.Block{
		Header: types.BlockHeader{
			Version:    cfg.Version,
			PrevBlock:  types.ZeroHash,
			MerkleRoot: types.ZeroHash, // Must be computed after tx hashing.
			Timestamp:  cfg.Timestamp,
			Bits:       cfg.Bits,
			Nonce:      0,
		},
		Transactions: []types.Transaction{coinbaseTx},
	}
}
