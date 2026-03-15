package utxo

import (
	"bytes"
	"testing"

	"github.com/bams-repo/fairchain/internal/crypto"
	"github.com/bams-repo/fairchain/internal/types"
)

func TestUtxoEntrySerializeRoundtrip(t *testing.T) {
	entry := &UtxoEntry{
		Value:      5000000000,
		PkScript:   []byte{0x76, 0xa9, 0x14},
		Height:     42,
		IsCoinbase: true,
	}

	data := entry.Serialize()
	got, err := DeserializeUtxoEntry(data)
	if err != nil {
		t.Fatalf("DeserializeUtxoEntry: %v", err)
	}

	if got.Value != entry.Value {
		t.Errorf("Value = %d, want %d", got.Value, entry.Value)
	}
	if got.Height != entry.Height {
		t.Errorf("Height = %d, want %d", got.Height, entry.Height)
	}
	if got.IsCoinbase != entry.IsCoinbase {
		t.Errorf("IsCoinbase = %v, want %v", got.IsCoinbase, entry.IsCoinbase)
	}
	if !bytes.Equal(got.PkScript, entry.PkScript) {
		t.Errorf("PkScript mismatch")
	}
}

func TestUtxoEntryNonCoinbase(t *testing.T) {
	entry := &UtxoEntry{
		Value:      100,
		PkScript:   []byte{0x01},
		Height:     10,
		IsCoinbase: false,
	}

	data := entry.Serialize()
	got, err := DeserializeUtxoEntry(data)
	if err != nil {
		t.Fatalf("DeserializeUtxoEntry: %v", err)
	}
	if got.IsCoinbase {
		t.Error("expected IsCoinbase=false")
	}
}

func TestOutpointKey(t *testing.T) {
	var hash types.Hash
	hash[0] = 0xAB
	hash[31] = 0xCD

	key := OutpointKey(hash, 7)
	if key[0] != 0xAB || key[31] != 0xCD {
		t.Error("hash bytes not preserved in key")
	}
	if key[32] != 7 || key[33] != 0 || key[34] != 0 || key[35] != 0 {
		t.Error("index not correctly encoded in key")
	}
}

func TestSetAddGetRemove(t *testing.T) {
	s := NewSet()

	var txHash types.Hash
	txHash[0] = 0x01

	entry := &UtxoEntry{Value: 1000, Height: 5}
	s.Add(txHash, 0, entry)

	if !s.Has(txHash, 0) {
		t.Fatal("expected UTXO to exist")
	}
	if s.Has(txHash, 1) {
		t.Fatal("expected UTXO at index 1 to not exist")
	}

	got := s.Get(txHash, 0)
	if got == nil || got.Value != 1000 {
		t.Fatal("Get returned wrong entry")
	}

	if s.Count() != 1 {
		t.Fatalf("Count = %d, want 1", s.Count())
	}

	removed := s.Remove(txHash, 0)
	if removed == nil || removed.Value != 1000 {
		t.Fatal("Remove returned wrong entry")
	}
	if s.Has(txHash, 0) {
		t.Fatal("UTXO should be removed")
	}
	if s.Count() != 0 {
		t.Fatalf("Count = %d, want 0", s.Count())
	}
}

func TestSetTotalValue(t *testing.T) {
	s := NewSet()

	var h1, h2 types.Hash
	h1[0] = 0x01
	h2[0] = 0x02

	s.Add(h1, 0, &UtxoEntry{Value: 100})
	s.Add(h2, 0, &UtxoEntry{Value: 200})

	if s.TotalValue() != 300 {
		t.Fatalf("TotalValue = %d, want 300", s.TotalValue())
	}
}

func TestUndoDataSerializeRoundtrip(t *testing.T) {
	var h1 types.Hash
	h1[0] = 0xAA

	undo := &BlockUndoData{
		SpentOutputs: []SpentOutput{
			{
				OutPoint: types.OutPoint{Hash: h1, Index: 3},
				Entry: UtxoEntry{
					Value:      999,
					PkScript:   []byte{0x01, 0x02},
					Height:     10,
					IsCoinbase: true,
				},
			},
		},
	}

	data := SerializeUndoData(undo)
	got, err := DeserializeUndoData(data)
	if err != nil {
		t.Fatalf("DeserializeUndoData: %v", err)
	}

	if len(got.SpentOutputs) != 1 {
		t.Fatalf("SpentOutputs len = %d, want 1", len(got.SpentOutputs))
	}
	so := got.SpentOutputs[0]
	if so.OutPoint.Hash != h1 || so.OutPoint.Index != 3 {
		t.Error("outpoint mismatch")
	}
	if so.Entry.Value != 999 || so.Entry.Height != 10 || !so.Entry.IsCoinbase {
		t.Error("entry mismatch")
	}
	if !bytes.Equal(so.Entry.PkScript, []byte{0x01, 0x02}) {
		t.Error("pkscript mismatch")
	}
}

func TestEmptyUndoData(t *testing.T) {
	undo := &BlockUndoData{}
	data := SerializeUndoData(undo)
	got, err := DeserializeUndoData(data)
	if err != nil {
		t.Fatalf("DeserializeUndoData: %v", err)
	}
	if len(got.SpentOutputs) != 0 {
		t.Fatalf("expected empty SpentOutputs, got %d", len(got.SpentOutputs))
	}
}

func TestConnectGenesisBlock(t *testing.T) {
	t.Run("legacy placeholder excluded", func(t *testing.T) {
		s := NewSet()
		genesis := &types.Block{
			Transactions: []types.Transaction{
				{
					Version: 1,
					Inputs: []types.TxInput{
						{
							PreviousOutPoint: types.CoinbaseOutPoint,
							SignatureScript:  []byte("genesis"),
							Sequence:         0xFFFFFFFF,
						},
					},
					Outputs: []types.TxOutput{
						{Value: 5000000000, PkScript: []byte{0x00}},
					},
				},
			},
		}
		if err := s.ConnectGenesis(genesis); err != nil {
			t.Fatalf("ConnectGenesis: %v", err)
		}
		if s.Count() != 0 {
			t.Fatalf("Count = %d, want 0 (legacy {0x00} should be excluded)", s.Count())
		}
	})

	t.Run("real script included", func(t *testing.T) {
		s := NewSet()
		genesis := &types.Block{
			Transactions: []types.Transaction{
				{
					Version: 1,
					Inputs: []types.TxInput{
						{
							PreviousOutPoint: types.CoinbaseOutPoint,
							SignatureScript:  []byte("genesis"),
							Sequence:         0xFFFFFFFF,
						},
					},
					Outputs: []types.TxOutput{
						{Value: 5000000000, PkScript: []byte{0x76, 0xa9, 0x14, 0x01, 0x02, 0x03}},
					},
				},
			},
		}
		if err := s.ConnectGenesis(genesis); err != nil {
			t.Fatalf("ConnectGenesis: %v", err)
		}
		if s.Count() != 1 {
			t.Fatalf("Count = %d, want 1", s.Count())
		}
		if s.TotalValue() != 5000000000 {
			t.Fatalf("TotalValue = %d, want 5000000000", s.TotalValue())
		}
	})
}

func TestConnectBlockAtomicOnFailure(t *testing.T) {
	s := NewSet()

	var txHash1 types.Hash
	txHash1[0] = 0x01
	s.Add(txHash1, 0, &UtxoEntry{Value: 1000, PkScript: []byte{0x00}, Height: 0})

	// Block with two txs: first spends txHash1:0 (valid), second references
	// a nonexistent UTXO. ConnectBlock should fail and leave the set unchanged.
	var fakeTxHash types.Hash
	fakeTxHash[0] = 0xFF

	block := &types.Block{
		Transactions: []types.Transaction{
			{
				Version: 1,
				Inputs: []types.TxInput{
					{PreviousOutPoint: types.CoinbaseOutPoint, SignatureScript: []byte("cb"), Sequence: 0xFFFFFFFF},
				},
				Outputs: []types.TxOutput{
					{Value: 5000000000, PkScript: []byte{0x00}},
				},
			},
			{
				Version: 1,
				Inputs: []types.TxInput{
					{PreviousOutPoint: types.OutPoint{Hash: txHash1, Index: 0}, Sequence: 0xFFFFFFFF},
				},
				Outputs: []types.TxOutput{
					{Value: 900, PkScript: []byte{0x01}},
				},
			},
			{
				Version: 1,
				Inputs: []types.TxInput{
					{PreviousOutPoint: types.OutPoint{Hash: fakeTxHash, Index: 0}, Sequence: 0xFFFFFFFF},
				},
				Outputs: []types.TxOutput{
					{Value: 100, PkScript: []byte{0x02}},
				},
			},
		},
	}

	countBefore := s.Count()
	totalBefore := s.TotalValue()

	_, err := s.ConnectBlock(block, 1)
	if err == nil {
		t.Fatal("expected ConnectBlock to fail on missing UTXO")
	}

	// UTXO set must be unchanged after failed ConnectBlock (atomic guarantee).
	if s.Count() != countBefore {
		t.Fatalf("UTXO count changed after failed ConnectBlock: got %d, want %d", s.Count(), countBefore)
	}
	if s.TotalValue() != totalBefore {
		t.Fatalf("UTXO total value changed after failed ConnectBlock: got %d, want %d", s.TotalValue(), totalBefore)
	}
	if !s.Has(txHash1, 0) {
		t.Fatal("txHash1:0 should still exist after failed ConnectBlock")
	}
}

func TestConnectBlockIntraBlockSpendNotLeaked(t *testing.T) {
	s := NewSet()

	var fundHash types.Hash
	fundHash[0] = 0x01
	s.Add(fundHash, 0, &UtxoEntry{Value: 1000, PkScript: []byte{0x00}, Height: 0})

	// Tx1 (coinbase) creates output. Tx2 spends fundHash:0 and creates an
	// output. Tx3 spends Tx2's output within the same block. After
	// ConnectBlock, Tx2's output must NOT appear in the UTXO set because it
	// was consumed by Tx3.
	block := &types.Block{
		Transactions: []types.Transaction{
			{
				Version: 1,
				Inputs: []types.TxInput{
					{PreviousOutPoint: types.CoinbaseOutPoint, SignatureScript: []byte("cb"), Sequence: 0xFFFFFFFF},
				},
				Outputs: []types.TxOutput{
					{Value: 5000000000, PkScript: []byte{0x00}},
				},
			},
			{
				Version: 1,
				Inputs: []types.TxInput{
					{PreviousOutPoint: types.OutPoint{Hash: fundHash, Index: 0}, Sequence: 0xFFFFFFFF},
				},
				Outputs: []types.TxOutput{
					{Value: 900, PkScript: []byte{0x00}},
				},
			},
		},
	}

	undo, err := s.ConnectBlock(block, 1)
	if err != nil {
		t.Fatalf("ConnectBlock: %v", err)
	}

	// Get tx2's hash so we can build a block that spends it in-block.
	tx2Hash := hashTx(&block.Transactions[1])

	// Now build a second block where tx1 creates output, tx2 spends the
	// previous block's tx2 output, and tx3 spends tx2's output in-block.
	block2 := &types.Block{
		Transactions: []types.Transaction{
			{
				Version: 1,
				Inputs: []types.TxInput{
					{PreviousOutPoint: types.CoinbaseOutPoint, SignatureScript: []byte("cb2"), Sequence: 0xFFFFFFFF},
				},
				Outputs: []types.TxOutput{
					{Value: 5000000000, PkScript: []byte{0x00}},
				},
			},
			{
				Version: 1,
				Inputs: []types.TxInput{
					{PreviousOutPoint: types.OutPoint{Hash: tx2Hash, Index: 0}, Sequence: 0xFFFFFFFF},
				},
				Outputs: []types.TxOutput{
					{Value: 800, PkScript: []byte{0x00}},
				},
			},
		},
	}

	// Get the hash of block2's tx[1] so we can spend it in the same block.
	b2Tx1Hash := hashTx(&block2.Transactions[1])

	// Add tx3 that spends tx2's output within the same block.
	block2.Transactions = append(block2.Transactions, types.Transaction{
		Version: 1,
		Inputs: []types.TxInput{
			{PreviousOutPoint: types.OutPoint{Hash: b2Tx1Hash, Index: 0}, Sequence: 0xFFFFFFFF},
		},
		Outputs: []types.TxOutput{
			{Value: 700, PkScript: []byte{0x00}},
		},
	})

	_, err = s.ConnectBlock(block2, 2)
	if err != nil {
		t.Fatalf("ConnectBlock block2: %v", err)
	}

	// The intermediate output (b2Tx1Hash:0) was spent in-block and must NOT
	// be in the UTXO set.
	if s.Has(b2Tx1Hash, 0) {
		t.Fatal("in-block spent output should not appear in UTXO set")
	}

	// The final output (tx3's output) should be in the set.
	b2Tx2Hash := hashTx(&block2.Transactions[2])
	if !s.Has(b2Tx2Hash, 0) {
		t.Fatal("final output of in-block chain should be in UTXO set")
	}

	_ = undo
}

func hashTx(tx *types.Transaction) types.Hash {
	h, _ := crypto.HashTransaction(tx)
	return h
}

func TestConnectBlockIntraBlockDoubleSpend(t *testing.T) {
	s := NewSet()

	var txHash1 types.Hash
	txHash1[0] = 0x01
	s.Add(txHash1, 0, &UtxoEntry{Value: 1000, PkScript: []byte{0x00}, Height: 0})

	// Two transactions in the same block both spending txHash1:0.
	block := &types.Block{
		Transactions: []types.Transaction{
			{
				Version: 1,
				Inputs: []types.TxInput{
					{PreviousOutPoint: types.CoinbaseOutPoint, SignatureScript: []byte("cb"), Sequence: 0xFFFFFFFF},
				},
				Outputs: []types.TxOutput{
					{Value: 5000000000, PkScript: []byte{0x00}},
				},
			},
			{
				Version: 1,
				Inputs: []types.TxInput{
					{PreviousOutPoint: types.OutPoint{Hash: txHash1, Index: 0}, Sequence: 0xFFFFFFFF},
				},
				Outputs: []types.TxOutput{
					{Value: 500, PkScript: []byte{0x01}},
				},
			},
			{
				Version: 1,
				Inputs: []types.TxInput{
					{PreviousOutPoint: types.OutPoint{Hash: txHash1, Index: 0}, Sequence: 0xFFFFFFFF},
				},
				Outputs: []types.TxOutput{
					{Value: 400, PkScript: []byte{0x02}},
				},
			},
		},
	}

	_, err := s.ConnectBlock(block, 1)
	if err == nil {
		t.Fatal("expected ConnectBlock to reject intra-block double spend")
	}

	// UTXO set must be unchanged.
	if !s.Has(txHash1, 0) {
		t.Fatal("txHash1:0 should still exist after rejected double-spend block")
	}
}
