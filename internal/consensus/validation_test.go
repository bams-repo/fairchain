package consensus

import (
	"testing"

	"github.com/bams-repo/fairchain/internal/crypto"
	"github.com/bams-repo/fairchain/internal/params"
	"github.com/bams-repo/fairchain/internal/types"
)

func makeTestBlock(height uint32, p *params.ChainParams) types.Block {
	subsidy := p.CalcSubsidy(height)
	// BIP34 format: [pushLen=0x04][height LE 4 bytes][tag]
	heightBytes := make([]byte, 4)
	types.PutUint32LE(heightBytes, height)
	scriptSig := make([]byte, 0, 1+4+len("test"))
	scriptSig = append(scriptSig, 0x04)
	scriptSig = append(scriptSig, heightBytes...)
	scriptSig = append(scriptSig, []byte("test")...)

	coinbase := types.Transaction{
		Version: 1,
		Inputs: []types.TxInput{
			{
				PreviousOutPoint: types.CoinbaseOutPoint,
				SignatureScript:  scriptSig,
				Sequence:         0xFFFFFFFF,
			},
		},
		Outputs: []types.TxOutput{
			{Value: subsidy, PkScript: []byte{0x00}},
		},
	}

	merkle, _ := crypto.ComputeMerkleRoot([]types.Transaction{coinbase})

	return types.Block{
		Header: types.BlockHeader{
			Version:    1,
			MerkleRoot: merkle,
			Timestamp:  1700000000 + height,
			Bits:       p.InitialBits,
		},
		Transactions: []types.Transaction{coinbase},
	}
}

func TestValidateBlockStructure(t *testing.T) {
	p := params.Regtest
	block := makeTestBlock(1, p)

	if err := ValidateBlockStructure(&block, 1, p); err != nil {
		t.Fatalf("valid block rejected: %v", err)
	}
}

func TestValidateBlockStructureNoCoinbase(t *testing.T) {
	p := params.Regtest
	block := types.Block{
		Header: types.BlockHeader{Version: 1, Bits: p.InitialBits},
		Transactions: []types.Transaction{
			{
				Version: 1,
				Inputs: []types.TxInput{
					{PreviousOutPoint: types.OutPoint{Hash: types.Hash{1}, Index: 0}, SignatureScript: []byte("sig"), Sequence: 0xFFFFFFFF},
				},
				Outputs: []types.TxOutput{{Value: 100, PkScript: []byte{0x00}}},
			},
		},
	}

	if err := ValidateBlockStructure(&block, 1, p); err == nil {
		t.Fatal("should reject block without coinbase")
	}
}

func TestValidateBlockStructureExcessiveSubsidy(t *testing.T) {
	// Coinbase value is now validated in ValidateTransactionInputs (where fees are known).
	// ValidateBlockStructure only checks for coinbase output value overflow.
	// This test verifies that a coinbase with value slightly above subsidy passes
	// structural validation (it would be caught by tx input validation with fee context).
	p := params.Regtest
	block := makeTestBlock(1, p)
	block.Transactions[0].Outputs[0].Value = p.InitialSubsidy + 1

	merkle, _ := crypto.ComputeMerkleRoot(block.Transactions)
	block.Header.MerkleRoot = merkle

	if err := ValidateBlockStructure(&block, 1, p); err != nil {
		t.Fatalf("structural validation should pass (coinbase cap enforced in tx validation): %v", err)
	}
}

func TestValidateBlockStructureBadMerkle(t *testing.T) {
	p := params.Regtest
	block := makeTestBlock(1, p)
	block.Header.MerkleRoot = types.Hash{0xFF}

	if err := ValidateBlockStructure(&block, 1, p); err == nil {
		t.Fatal("should reject block with bad merkle root")
	}
}

func TestValidateBlockStructureDuplicateTx(t *testing.T) {
	p := params.Regtest
	block := makeTestBlock(1, p)
	// Add a duplicate of the coinbase (which would have the same txid).
	block.Transactions = append(block.Transactions, block.Transactions[0])
	merkle, _ := crypto.ComputeMerkleRoot(block.Transactions)
	block.Header.MerkleRoot = merkle

	if err := ValidateBlockStructure(&block, 1, p); err == nil {
		t.Fatal("should reject block with duplicate txids")
	}
}

func TestValidateBlockStructureEmpty(t *testing.T) {
	p := params.Regtest
	block := types.Block{
		Header:       types.BlockHeader{Version: 1, Bits: p.InitialBits},
		Transactions: nil,
	}

	if err := ValidateBlockStructure(&block, 0, p); err == nil {
		t.Fatal("should reject empty block")
	}
}

func TestCalcMedianTimePast(t *testing.T) {
	headers := make(map[uint32]*types.BlockHeader)
	for i := uint32(0); i < 15; i++ {
		headers[i] = &types.BlockHeader{Timestamp: 1700000000 + i*60}
	}
	getAncestor := func(h uint32) *types.BlockHeader {
		return headers[h]
	}

	median := calcMedianTimePast(14, getAncestor)
	// Median of timestamps at heights 4..14 (11 values).
	// Timestamps: 1700000240, ..., 1700000840. Median = 1700000540 (height 9).
	expected := uint32(1700000000 + 9*60)
	if median != expected {
		t.Fatalf("median time past = %d, want %d", median, expected)
	}
}

func TestSubsidySchedule(t *testing.T) {
	p := params.Regtest

	s0 := p.CalcSubsidy(0)
	if s0 != p.InitialSubsidy {
		t.Fatalf("subsidy at height 0 = %d, want %d", s0, p.InitialSubsidy)
	}

	s150 := p.CalcSubsidy(150) // First halving for regtest.
	if s150 != p.InitialSubsidy/2 {
		t.Fatalf("subsidy at height 150 = %d, want %d", s150, p.InitialSubsidy/2)
	}

	s300 := p.CalcSubsidy(300)
	if s300 != p.InitialSubsidy/4 {
		t.Fatalf("subsidy at height 300 = %d, want %d", s300, p.InitialSubsidy/4)
	}
}
