package params

import (
	"time"

	"github.com/bams-repo/fairchain/internal/types"
)

const (
	// Base-unit representation of ~20,999,999.9769 coin cap.
	baseMaxSupply = 2_099_999_997_690_000

	// 20% premine on top of mined supply for testnet.
	testnetPremineAmount = baseMaxSupply / 5
)

var (
	// Hardcoded burn marker script for trackable burns/premine accounting.
	// NOTE: Script spend rules are not enforced yet in this codebase.
	testnetBurnScript = []byte("burn:testnet:premine:v1")
)

// Mainnet is the primary fairchain network.
// Economic parameters are aligned with Bitcoin mainnet.
var Mainnet = &ChainParams{
	Name:         "mainnet",
	NetworkMagic: [4]byte{0xFA, 0x1C, 0xC0, 0x01},
	DefaultPort:  19333,
	AddressPrefix: 0x00,

	// Pre-mined genesis block.
	// Coinbase: "fairchain mainnet genesis"
	// Timestamp: 1773212462 (2026-03-08T23:41:02Z)
	// Display hash: 00000db0edab82e820ef5c8c7a12ceb8ec6639e3110457a1cee156361fb87054
	GenesisBlock: types.Block{
		Header: types.BlockHeader{
			Version:   1,
			PrevBlock: types.ZeroHash,
			MerkleRoot: types.Hash{
				0x1a, 0x43, 0xdf, 0x3e, 0xf8, 0x14, 0x0d, 0xbe,
				0x47, 0xad, 0xea, 0xdb, 0x14, 0x1b, 0xd4, 0xbb,
				0x74, 0xee, 0x7d, 0x6f, 0x81, 0x44, 0x1c, 0x4d,
				0xc0, 0x41, 0x16, 0xf1, 0xb5, 0x01, 0xdc, 0xb5,
			},
			Timestamp: 1773212462,
			Bits:      0x1e0fffff,
			Nonce:     433076,
		},
		Transactions: []types.Transaction{{
			Version: 1,
			Inputs: []types.TxInput{{
				PreviousOutPoint: types.CoinbaseOutPoint,
				SignatureScript:  []byte("fairchain mainnet genesis"),
				Sequence:         0xFFFFFFFF,
			}},
			Outputs: []types.TxOutput{{
				Value:    50_0000_0000,
				PkScript: []byte{0x00},
			}},
			LockTime: 0,
		}},
	},
	GenesisHash: types.Hash{
		0x54, 0x70, 0xb8, 0x1f, 0x36, 0x56, 0xe1, 0xce,
		0xa1, 0x57, 0x04, 0x11, 0xe3, 0x39, 0x66, 0xec,
		0xb8, 0xce, 0x12, 0x7a, 0x8c, 0x5c, 0xef, 0x20,
		0xe8, 0x82, 0xab, 0xed, 0xb0, 0x0d, 0x00, 0x00,
	},

	TargetBlockSpacing:  10 * time.Minute,
	RetargetInterval:    144,
	TargetTimespan:      144 * 10 * time.Minute,
	MaxTimeFutureDrift:  2 * time.Hour,
	MinTimestampRule:    "median-11",

	InitialBits:      0x1e0fffff,
	MinBits:          0x1e0fffff,
	NoRetarget:       false,

	MaxBlockSize:     1_000_000,
	MaxBlockTxCount:  10_000,

	InitialSubsidy:          50_0000_0000,
	SubsidyHalvingInterval:  210_000,

	CoinbaseMaturity: 100,

	MaxMempoolSize: 5000,
	MinRelayTxFee:  1000,

	SeedNodes: []string{},

	ActivationHeights: map[string]uint32{},
}

// Testnet is the public test network with easier difficulty.
var Testnet = &ChainParams{
	Name:         "testnet",
	NetworkMagic: [4]byte{0xFA, 0x1C, 0xC0, 0x02},
	DefaultPort:  19334,
	AddressPrefix: 0x6F,

	// Pre-mined genesis block.
	// Coinbase: "fairchain testnet genesis"
	// Timestamp: 1773212867 (2026-03-11T07:07:47Z)
	// Display hash: 000008559ecc2476f6f26b3367f2935d9976f58a6a6cbcff62df26f1f2861097
	GenesisBlock: types.Block{
		Header: types.BlockHeader{
			Version:   1,
			PrevBlock: types.ZeroHash,
			MerkleRoot: types.Hash{
				0xb5, 0x8a, 0xb7, 0x94, 0xe8, 0x13, 0x5d, 0x55,
				0xf9, 0x7b, 0x93, 0x7f, 0xbb, 0x19, 0xca, 0xa3,
				0xe4, 0x3b, 0xd0, 0x3f, 0xe6, 0x0b, 0x4e, 0x08,
				0x19, 0x0a, 0xf5, 0x44, 0x52, 0xce, 0xd2, 0x3a,
			},
			Timestamp: 1773212867,
			Bits:      0x1e0fffff,
			Nonce:     912710,
		},
		Transactions: []types.Transaction{{
			Version: 1,
			Inputs: []types.TxInput{{
				PreviousOutPoint: types.CoinbaseOutPoint,
				SignatureScript:  []byte("fairchain testnet genesis"),
				Sequence:         0xFFFFFFFF,
			}},
			Outputs: []types.TxOutput{
				{
					Value:    50_0000_00,
					PkScript: []byte{0x00},
				},
				{
					Value:    testnetPremineAmount,
					PkScript: testnetBurnScript,
				},
			},
			LockTime: 0,
		}},
	},
	GenesisHash: types.Hash{
		0x97, 0x10, 0x86, 0xf2, 0xf1, 0x26, 0xdf, 0x62,
		0xff, 0xbc, 0x6c, 0x6a, 0x8a, 0xf5, 0x76, 0x99,
		0x5d, 0x93, 0xf2, 0x67, 0x33, 0x6b, 0xf2, 0xf6,
		0x76, 0x24, 0xcc, 0x9e, 0x55, 0x08, 0x00, 0x00,
	},

	TargetBlockSpacing:  5 * time.Second,
	RetargetInterval:    20,
	TargetTimespan:      20 * 5 * time.Second, // 100s
	MaxTimeFutureDrift:  2 * time.Minute,
	MinTimestampRule:    "median-11",

	InitialBits:      0x1e0fffff,
	MinBits:          0x1e0fffff,
	NoRetarget:       false,

	MaxBlockSize:     2_000_000,
	MaxBlockTxCount:  10_000,

	// Economic scaling: testnet is 100x block-height accelerated relative to
	// mainnet for issuance comparisons (e.g., testnet 100,000 ~= mainnet 1,000).
	// To keep monetary state aligned by that mapping:
	//   - per-block subsidy is 1/100 of mainnet
	//   - halving interval is 100x mainnet
	InitialSubsidy:          50_0000_00,
	SubsidyHalvingInterval:  21_000_000,

	CoinbaseMaturity: 10,

	MaxMempoolSize: 5000,
	MinRelayTxFee:  100,

	SeedNodes: []string{
		"45.32.196.26:19334",  // main_web
		"207.148.9.169:19334", // mining_pool
	},

	ActivationHeights: map[string]uint32{},
}

// Regtest is a local regression-test network with trivial difficulty and no retarget.
var Regtest = &ChainParams{
	Name:         "regtest",
	NetworkMagic: [4]byte{0xFA, 0x1C, 0xC0, 0xFF},
	DefaultPort:  19444,
	AddressPrefix: 0x6F,

	TargetBlockSpacing:  1 * time.Second,
	RetargetInterval:    1,
	TargetTimespan:      1 * time.Second,
	MaxTimeFutureDrift:  10 * time.Minute,
	MinTimestampRule:    "prev+1",

	// Very easy difficulty: top byte 0x20 = exponent 32, mantissa 0x0fffff.
	InitialBits:      0x207fffff,
	MinBits:          0x207fffff,
	NoRetarget:       true,

	MaxBlockSize:     4_000_000,
	MaxBlockTxCount:  50_000,

	InitialSubsidy:          50_0000_0000,
	SubsidyHalvingInterval:  150,

	CoinbaseMaturity: 1,

	MaxMempoolSize: 10000,
	MinRelayTxFee:  0,

	SeedNodes: []string{},

	ActivationHeights: map[string]uint32{},
}

// NetworkByName returns chain params by network name.
func NetworkByName(name string) *ChainParams {
	switch name {
	case "mainnet":
		return Mainnet
	case "testnet":
		return Testnet
	case "regtest":
		return Regtest
	default:
		return nil
	}
}

// InitGenesis computes and sets the genesis block and hash for the given params.
// This should be called after the genesis block has been mined (nonce found).
func InitGenesis(p *ChainParams, genesisBlock types.Block, genesisHash types.Hash) {
	p.GenesisBlock = genesisBlock
	p.GenesisHash = genesisHash
}
