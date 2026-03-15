package consensus

import (
	"testing"

	"github.com/bams-repo/fairchain/internal/crypto"
	"github.com/bams-repo/fairchain/internal/params"
	"github.com/bams-repo/fairchain/internal/types"
	"github.com/bams-repo/fairchain/internal/utxo"
)

func makeTestParams() *params.ChainParams {
	p := &params.ChainParams{}
	*p = *params.Regtest
	p.ActivationHeights = map[string]uint32{}
	return p
}

func makeCoinbaseTx(height uint32, value uint64) types.Transaction {
	// BIP34 format: [pushLen=0x04][height LE 4 bytes][tag]
	heightBytes := make([]byte, 4)
	types.PutUint32LE(heightBytes, height)
	scriptSig := make([]byte, 0, 1+4+len("test"))
	scriptSig = append(scriptSig, 0x04)
	scriptSig = append(scriptSig, heightBytes...)
	scriptSig = append(scriptSig, []byte("test")...)
	return types.Transaction{
		Version: 1,
		Inputs: []types.TxInput{{
			PreviousOutPoint: types.CoinbaseOutPoint,
			SignatureScript:  scriptSig,
			Sequence:         0xFFFFFFFF,
		}},
		Outputs: []types.TxOutput{{
			Value:    value,
			PkScript: []byte{0x00},
		}},
	}
}

func bip34ScriptSig(height uint32, tag string) []byte {
	heightBytes := make([]byte, 4)
	types.PutUint32LE(heightBytes, height)
	s := make([]byte, 0, 1+4+len(tag))
	s = append(s, 0x04)
	s = append(s, heightBytes...)
	s = append(s, []byte(tag)...)
	return s
}

// testKeyPair holds a keypair for use in tests that need script validation.
type testKeyPair struct {
	pkScript []byte
	privKey  interface{ Serialize() []byte }
}

// newTestKeyPair generates a fresh keypair and returns its P2PKH script.
func newTestKeyPair(t *testing.T) testKeyPair {
	t.Helper()
	privBytes, pubBytes, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	privKey, err := crypto.PrivKeyFromBytes(privBytes)
	if err != nil {
		t.Fatal(err)
	}
	return testKeyPair{
		pkScript: crypto.MakeP2PKHScriptFromPubKey(pubBytes),
		privKey:  privKey,
	}
}

// signTxInput signs a single input of a transaction using the test keypair.
func signTxInput(t *testing.T, tx *types.Transaction, inputIdx int, kp testKeyPair) {
	t.Helper()
	privKey, err := crypto.PrivKeyFromBytes(kp.privKey.Serialize())
	if err != nil {
		t.Fatal(err)
	}
	sigScript, err := crypto.SignInput(tx, inputIdx, kp.pkScript, privKey)
	if err != nil {
		t.Fatal(err)
	}
	tx.Inputs[inputIdx].SignatureScript = sigScript
}

func addUTXO(s *utxo.Set, hash types.Hash, index uint32, value uint64, height uint32, isCoinbase bool, pkScript []byte) {
	s.Add(hash, index, &utxo.UtxoEntry{
		Value:      value,
		PkScript:   pkScript,
		Height:     height,
		IsCoinbase: isCoinbase,
	})
}

func TestValidateTransactionInputs_ScriptValidation_RejectsStealUTXO(t *testing.T) {
	p := params.Regtest

	_, pubBytes, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	pkScript := crypto.MakeP2PKHScriptFromPubKey(pubBytes)

	prevTxHash := crypto.DoubleSHA256([]byte("miner-coinbase-tx"))
	utxoSet := utxo.NewSet()
	utxoSet.Add(prevTxHash, 0, &utxo.UtxoEntry{
		Value:      5_000_000_000,
		PkScript:   pkScript,
		Height:     1,
		IsCoinbase: true,
	})

	height := uint32(200)
	subsidy := p.CalcSubsidy(height)

	coinbase := types.Transaction{
		Version: 1,
		Inputs: []types.TxInput{{
			PreviousOutPoint: types.CoinbaseOutPoint,
			SignatureScript:  bip34ScriptSig(height, "test"),
			Sequence:         0xFFFFFFFF,
		}},
		Outputs: []types.TxOutput{{Value: subsidy + 100, PkScript: []byte{0x00}}},
	}

	// Attacker tries to spend the UTXO with arbitrary bytes (no valid signature).
	stealTx := types.Transaction{
		Version: 1,
		Inputs: []types.TxInput{{
			PreviousOutPoint: types.OutPoint{Hash: prevTxHash, Index: 0},
			SignatureScript:  []byte("STOLEN-no-signature-required"),
			Sequence:         0xFFFFFFFF,
		}},
		Outputs: []types.TxOutput{{Value: 4_999_999_900, PkScript: []byte("attacker")}},
	}

	block := &types.Block{
		Transactions: []types.Transaction{coinbase, stealTx},
	}

	_, err = ValidateTransactionInputs(block, utxoSet, height, p)
	if err == nil {
		t.Fatal("steal-utxo attack should be rejected by script validation in ValidateTransactionInputs")
	}
	t.Logf("steal-utxo correctly rejected at consensus level: %v", err)
}

func TestValidateTransactionInputs_ScriptValidation_AcceptsValidSig(t *testing.T) {
	p := params.Regtest

	privBytes, pubBytes, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	privKey, err := crypto.PrivKeyFromBytes(privBytes)
	if err != nil {
		t.Fatal(err)
	}
	pkScript := crypto.MakeP2PKHScriptFromPubKey(pubBytes)

	prevTxHash := crypto.DoubleSHA256([]byte("miner-coinbase-tx"))
	utxoSet := utxo.NewSet()
	utxoSet.Add(prevTxHash, 0, &utxo.UtxoEntry{
		Value:      5_000_000_000,
		PkScript:   pkScript,
		Height:     1,
		IsCoinbase: true,
	})

	height := uint32(200)
	subsidy := p.CalcSubsidy(height)
	fee := uint64(100)

	coinbase := types.Transaction{
		Version: 1,
		Inputs: []types.TxInput{{
			PreviousOutPoint: types.CoinbaseOutPoint,
			SignatureScript:  bip34ScriptSig(height, "test"),
			Sequence:         0xFFFFFFFF,
		}},
		Outputs: []types.TxOutput{{Value: subsidy + fee, PkScript: []byte{0x00}}},
	}

	spendTx := types.Transaction{
		Version: 1,
		Inputs: []types.TxInput{{
			PreviousOutPoint: types.OutPoint{Hash: prevTxHash, Index: 0},
			Sequence:         0xFFFFFFFF,
		}},
		Outputs: []types.TxOutput{{Value: 5_000_000_000 - fee, PkScript: pkScript}},
	}

	sigScript, err := crypto.SignInput(&spendTx, 0, pkScript, privKey)
	if err != nil {
		t.Fatalf("sign input: %v", err)
	}
	spendTx.Inputs[0].SignatureScript = sigScript

	block := &types.Block{
		Transactions: []types.Transaction{coinbase, spendTx},
	}

	fees, err := ValidateTransactionInputs(block, utxoSet, height, p)
	if err != nil {
		t.Fatalf("valid signed transaction should pass: %v", err)
	}
	if fees != fee {
		t.Fatalf("fees: got %d, want %d", fees, fee)
	}
}

func TestValidateTransactionInputs_ScriptValidation_RejectsBurnSpend(t *testing.T) {
	p := params.Testnet

	burnScript := []byte("burn:testnet:premine:v1")
	prevTxHash := crypto.DoubleSHA256([]byte("genesis-coinbase"))

	utxoSet := utxo.NewSet()
	utxoSet.Add(prevTxHash, 1, &utxo.UtxoEntry{
		Value:      419_999_999_538_000,
		PkScript:   burnScript,
		Height:     0,
		IsCoinbase: true,
	})

	height := uint32(100)
	subsidy := p.CalcSubsidy(height)

	coinbase := types.Transaction{
		Version: 1,
		Inputs: []types.TxInput{{
			PreviousOutPoint: types.CoinbaseOutPoint,
			SignatureScript:  bip34ScriptSig(height, "test"),
			Sequence:         0xFFFFFFFF,
		}},
		Outputs: []types.TxOutput{{Value: subsidy + 1000, PkScript: []byte{0x00}}},
	}

	stealTx := types.Transaction{
		Version: 1,
		Inputs: []types.TxInput{{
			PreviousOutPoint: types.OutPoint{Hash: prevTxHash, Index: 1},
			SignatureScript:  []byte("PREMINE-THEFT-no-script-validation"),
			Sequence:         0xFFFFFFFF,
		}},
		Outputs: []types.TxOutput{
			{Value: 209_999_999_769_000, PkScript: []byte("attacker-1")},
			{Value: 209_999_999_768_000, PkScript: []byte("attacker-2")},
		},
	}

	block := &types.Block{
		Transactions: []types.Transaction{coinbase, stealTx},
	}

	_, err := ValidateTransactionInputs(block, utxoSet, height, p)
	if err == nil {
		t.Fatal("steal-premine attack should be rejected by script validation")
	}
	t.Logf("steal-premine correctly rejected at consensus level: %v", err)
}

func TestValidateTransactionInputs_LegacyScriptRejected(t *testing.T) {
	p := params.Regtest

	legacyScript := []byte{0x00}
	prevTxHash := crypto.DoubleSHA256([]byte("genesis-coinbase"))

	utxoSet := utxo.NewSet()
	utxoSet.Add(prevTxHash, 0, &utxo.UtxoEntry{
		Value:      5_000_000_000,
		PkScript:   legacyScript,
		Height:     0,
		IsCoinbase: true,
	})

	height := uint32(10)
	subsidy := p.CalcSubsidy(height)

	coinbase := types.Transaction{
		Version: 1,
		Inputs: []types.TxInput{{
			PreviousOutPoint: types.CoinbaseOutPoint,
			SignatureScript:  bip34ScriptSig(height, "test"),
			Sequence:         0xFFFFFFFF,
		}},
		Outputs: []types.TxOutput{{Value: subsidy + 100, PkScript: []byte{0x00}}},
	}

	spendTx := types.Transaction{
		Version: 1,
		Inputs: []types.TxInput{{
			PreviousOutPoint: types.OutPoint{Hash: prevTxHash, Index: 0},
			SignatureScript:  []byte("anything-goes-for-legacy"),
			Sequence:         0xFFFFFFFF,
		}},
		Outputs: []types.TxOutput{{Value: 4_999_999_900, PkScript: []byte{0x00}}},
	}

	block := &types.Block{
		Transactions: []types.Transaction{coinbase, spendTx},
	}

	_, err := ValidateTransactionInputs(block, utxoSet, height, p)
	if err == nil {
		t.Fatal("legacy {0x00} script should now be rejected by script validation")
	}
	t.Logf("legacy script correctly rejected: %v", err)
}

func TestValidateSingleTransaction_ScriptValidation_RejectsSteal(t *testing.T) {
	p := params.Regtest

	_, pubBytes, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	pkScript := crypto.MakeP2PKHScriptFromPubKey(pubBytes)

	prevTxHash := crypto.DoubleSHA256([]byte("some-tx"))
	utxoSet := utxo.NewSet()
	utxoSet.Add(prevTxHash, 0, &utxo.UtxoEntry{
		Value:    1_000_000,
		PkScript: pkScript,
		Height:   1,
	})

	stealTx := &types.Transaction{
		Version: 1,
		Inputs: []types.TxInput{{
			PreviousOutPoint: types.OutPoint{Hash: prevTxHash, Index: 0},
			SignatureScript:  []byte("STOLEN"),
			Sequence:         0xFFFFFFFF,
		}},
		Outputs: []types.TxOutput{{Value: 999_000, PkScript: []byte("attacker")}},
	}

	_, err = ValidateSingleTransaction(stealTx, utxoSet, 100, p)
	if err == nil {
		t.Fatal("mempool should reject transaction with invalid script")
	}
	t.Logf("mempool steal correctly rejected: %v", err)
}

func TestValidateSingleTransaction_ScriptValidation_AcceptsValid(t *testing.T) {
	p := params.Regtest

	privBytes, pubBytes, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	privKey, err := crypto.PrivKeyFromBytes(privBytes)
	if err != nil {
		t.Fatal(err)
	}
	pkScript := crypto.MakeP2PKHScriptFromPubKey(pubBytes)

	prevTxHash := crypto.DoubleSHA256([]byte("some-tx"))
	utxoSet := utxo.NewSet()
	utxoSet.Add(prevTxHash, 0, &utxo.UtxoEntry{
		Value:    1_000_000,
		PkScript: pkScript,
		Height:   1,
	})

	spendTx := &types.Transaction{
		Version: 1,
		Inputs: []types.TxInput{{
			PreviousOutPoint: types.OutPoint{Hash: prevTxHash, Index: 0},
			Sequence:         0xFFFFFFFF,
		}},
		Outputs: []types.TxOutput{{Value: 999_000, PkScript: pkScript}},
	}

	sigScript, err := crypto.SignInput(spendTx, 0, pkScript, privKey)
	if err != nil {
		t.Fatal(err)
	}
	spendTx.Inputs[0].SignatureScript = sigScript

	fee, err := ValidateSingleTransaction(spendTx, utxoSet, 100, p)
	if err != nil {
		t.Fatalf("valid signed transaction should pass mempool validation: %v", err)
	}
	if fee != 1000 {
		t.Fatalf("fee: got %d, want 1000", fee)
	}
}

// --- Legacy validation tests (UTXO/value/structure rules, using legacy scripts) ---

func TestDoubleSpendWithinBlock(t *testing.T) {
	p := makeTestParams()
	s := utxo.NewSet()
	kp := newTestKeyPair(t)

	var txHash1 types.Hash
	txHash1[0] = 0x01
	addUTXO(s, txHash1, 0, 1000, 0, false, kp.pkScript)

	block := &types.Block{
		Transactions: []types.Transaction{
			makeCoinbaseTx(5, p.CalcSubsidy(5)),
			{
				Version: 1,
				Inputs: []types.TxInput{
					{PreviousOutPoint: types.OutPoint{Hash: txHash1, Index: 0}, Sequence: 0xFFFFFFFF},
				},
				Outputs: []types.TxOutput{{Value: 500, PkScript: []byte{0x01}}},
			},
			{
				Version: 1,
				Inputs: []types.TxInput{
					{PreviousOutPoint: types.OutPoint{Hash: txHash1, Index: 0}, Sequence: 0xFFFFFFFF},
				},
				Outputs: []types.TxOutput{{Value: 400, PkScript: []byte{0x02}}},
			},
		},
	}
	signTxInput(t, &block.Transactions[1], 0, kp)
	signTxInput(t, &block.Transactions[2], 0, kp)

	_, err := ValidateTransactionInputs(block, s, 5, p)
	if err == nil {
		t.Fatal("expected rejection for double-spend within block")
	}
}

func TestDuplicateInputsWithinTransaction(t *testing.T) {
	p := makeTestParams()
	s := utxo.NewSet()
	kp := newTestKeyPair(t)

	var txHash1 types.Hash
	txHash1[0] = 0x01
	addUTXO(s, txHash1, 0, 1000, 0, false, kp.pkScript)

	block := &types.Block{
		Transactions: []types.Transaction{
			makeCoinbaseTx(5, p.CalcSubsidy(5)),
			{
				Version: 1,
				Inputs: []types.TxInput{
					{PreviousOutPoint: types.OutPoint{Hash: txHash1, Index: 0}, Sequence: 0xFFFFFFFF},
					{PreviousOutPoint: types.OutPoint{Hash: txHash1, Index: 0}, Sequence: 0xFFFFFFFF},
				},
				Outputs: []types.TxOutput{{Value: 500, PkScript: []byte{0x01}}},
			},
		},
	}
	signTxInput(t, &block.Transactions[1], 0, kp)
	signTxInput(t, &block.Transactions[1], 1, kp)

	_, err := ValidateTransactionInputs(block, s, 5, p)
	if err == nil {
		t.Fatal("expected rejection for duplicate inputs within a single transaction")
	}
}

func TestOverspendTransaction(t *testing.T) {
	p := makeTestParams()
	s := utxo.NewSet()
	kp := newTestKeyPair(t)

	var txHash1 types.Hash
	txHash1[0] = 0x01
	addUTXO(s, txHash1, 0, 1000, 0, false, kp.pkScript)

	block := &types.Block{
		Transactions: []types.Transaction{
			makeCoinbaseTx(5, p.CalcSubsidy(5)),
			{
				Version: 1,
				Inputs: []types.TxInput{
					{PreviousOutPoint: types.OutPoint{Hash: txHash1, Index: 0}, Sequence: 0xFFFFFFFF},
				},
				Outputs: []types.TxOutput{{Value: 9999, PkScript: []byte{0x01}}},
			},
		},
	}
	signTxInput(t, &block.Transactions[1], 0, kp)

	_, err := ValidateTransactionInputs(block, s, 5, p)
	if err == nil {
		t.Fatal("expected rejection for overspend (output > input)")
	}
}

func TestImmatureCoinbaseSpend(t *testing.T) {
	p := makeTestParams()
	p.CoinbaseMaturity = 10
	kp := newTestKeyPair(t)

	s := utxo.NewSet()

	var cbHash types.Hash
	cbHash[0] = 0xCB
	addUTXO(s, cbHash, 0, 5000000000, 5, true, kp.pkScript)

	block := &types.Block{
		Transactions: []types.Transaction{
			makeCoinbaseTx(8, p.CalcSubsidy(8)),
			{
				Version: 1,
				Inputs: []types.TxInput{
					{PreviousOutPoint: types.OutPoint{Hash: cbHash, Index: 0}, Sequence: 0xFFFFFFFF},
				},
				Outputs: []types.TxOutput{{Value: 1000, PkScript: []byte{0x01}}},
			},
		},
	}
	signTxInput(t, &block.Transactions[1], 0, kp)

	_, err := ValidateTransactionInputs(block, s, 8, p)
	if err == nil {
		t.Fatal("expected rejection for immature coinbase spend (height 5, spending at height 8, maturity 10)")
	}
}

func TestMatureCoinbaseSpendAccepted(t *testing.T) {
	p := makeTestParams()
	p.CoinbaseMaturity = 10
	kp := newTestKeyPair(t)

	s := utxo.NewSet()

	var cbHash types.Hash
	cbHash[0] = 0xCB
	addUTXO(s, cbHash, 0, 5000000000, 5, true, kp.pkScript)

	block := &types.Block{
		Transactions: []types.Transaction{
			makeCoinbaseTx(20, p.CalcSubsidy(20)),
			{
				Version: 1,
				Inputs: []types.TxInput{
					{PreviousOutPoint: types.OutPoint{Hash: cbHash, Index: 0}, Sequence: 0xFFFFFFFF},
				},
				Outputs: []types.TxOutput{{Value: 1000, PkScript: []byte{0x01}}},
			},
		},
	}
	signTxInput(t, &block.Transactions[1], 0, kp)

	_, err := ValidateTransactionInputs(block, s, 20, p)
	if err != nil {
		t.Fatalf("expected mature coinbase spend to be accepted: %v", err)
	}
}

func TestInvalidCoinbaseReward(t *testing.T) {
	p := makeTestParams()
	s := utxo.NewSet()

	subsidy := p.CalcSubsidy(5)

	block := &types.Block{
		Transactions: []types.Transaction{
			makeCoinbaseTx(5, subsidy+1),
		},
	}

	_, err := ValidateTransactionInputs(block, s, 5, p)
	if err == nil {
		t.Fatal("expected rejection for coinbase reward exceeding subsidy (no fees)")
	}
}

func TestCoinbaseRewardWithFees(t *testing.T) {
	p := makeTestParams()
	s := utxo.NewSet()
	kp := newTestKeyPair(t)

	var txHash1 types.Hash
	txHash1[0] = 0x01
	addUTXO(s, txHash1, 0, 1000, 0, false, kp.pkScript)

	subsidy := p.CalcSubsidy(5)
	fee := uint64(100)

	block := &types.Block{
		Transactions: []types.Transaction{
			makeCoinbaseTx(5, subsidy+fee),
			{
				Version: 1,
				Inputs: []types.TxInput{
					{PreviousOutPoint: types.OutPoint{Hash: txHash1, Index: 0}, Sequence: 0xFFFFFFFF},
				},
				Outputs: []types.TxOutput{{Value: 1000 - fee, PkScript: []byte{0x01}}},
			},
		},
	}
	signTxInput(t, &block.Transactions[1], 0, kp)

	_, err := ValidateTransactionInputs(block, s, 5, p)
	if err != nil {
		t.Fatalf("expected valid block with coinbase = subsidy + fees: %v", err)
	}
}

func TestCoinbaseExceedingSubsidyPlusFees(t *testing.T) {
	p := makeTestParams()
	s := utxo.NewSet()
	kp := newTestKeyPair(t)

	var txHash1 types.Hash
	txHash1[0] = 0x01
	addUTXO(s, txHash1, 0, 1000, 0, false, kp.pkScript)

	subsidy := p.CalcSubsidy(5)
	fee := uint64(100)

	block := &types.Block{
		Transactions: []types.Transaction{
			makeCoinbaseTx(5, subsidy+fee+1),
			{
				Version: 1,
				Inputs: []types.TxInput{
					{PreviousOutPoint: types.OutPoint{Hash: txHash1, Index: 0}, Sequence: 0xFFFFFFFF},
				},
				Outputs: []types.TxOutput{{Value: 1000 - fee, PkScript: []byte{0x01}}},
			},
		},
	}
	signTxInput(t, &block.Transactions[1], 0, kp)

	_, err := ValidateTransactionInputs(block, s, 5, p)
	if err == nil {
		t.Fatal("expected rejection for coinbase exceeding subsidy + fees")
	}
}

func TestNonexistentUTXOReference(t *testing.T) {
	p := makeTestParams()
	s := utxo.NewSet()

	var fakeTxHash types.Hash
	fakeTxHash[0] = 0xFF

	block := &types.Block{
		Transactions: []types.Transaction{
			makeCoinbaseTx(5, p.CalcSubsidy(5)),
			{
				Version: 1,
				Inputs: []types.TxInput{
					{PreviousOutPoint: types.OutPoint{Hash: fakeTxHash, Index: 0}, Sequence: 0xFFFFFFFF},
				},
				Outputs: []types.TxOutput{{Value: 100, PkScript: []byte{0x01}}},
			},
		},
	}

	_, err := ValidateTransactionInputs(block, s, 5, p)
	if err == nil {
		t.Fatal("expected rejection for reference to nonexistent UTXO")
	}
}

func TestBlockWithConflictingTransactions(t *testing.T) {
	p := makeTestParams()
	s := utxo.NewSet()
	kp := newTestKeyPair(t)

	var txHash1 types.Hash
	txHash1[0] = 0x01
	addUTXO(s, txHash1, 0, 1000, 0, false, kp.pkScript)

	var txHash2 types.Hash
	txHash2[0] = 0x02
	addUTXO(s, txHash2, 0, 2000, 0, false, kp.pkScript)

	block := &types.Block{
		Transactions: []types.Transaction{
			makeCoinbaseTx(5, p.CalcSubsidy(5)),
			{
				Version: 1,
				Inputs: []types.TxInput{
					{PreviousOutPoint: types.OutPoint{Hash: txHash1, Index: 0}, Sequence: 0xFFFFFFFF},
					{PreviousOutPoint: types.OutPoint{Hash: txHash2, Index: 0}, Sequence: 0xFFFFFFFF},
				},
				Outputs: []types.TxOutput{{Value: 2500, PkScript: []byte{0x01}}},
			},
			{
				Version: 1,
				Inputs: []types.TxInput{
					{PreviousOutPoint: types.OutPoint{Hash: txHash1, Index: 0}, Sequence: 0xFFFFFFFF},
				},
				Outputs: []types.TxOutput{{Value: 500, PkScript: []byte{0x02}}},
			},
		},
	}
	signTxInput(t, &block.Transactions[1], 0, kp)
	signTxInput(t, &block.Transactions[1], 1, kp)
	signTxInput(t, &block.Transactions[2], 0, kp)

	_, err := ValidateTransactionInputs(block, s, 5, p)
	if err == nil {
		t.Fatal("expected rejection for conflicting transactions (tx2 spends same UTXO as tx1)")
	}
}

func TestZeroValueOutputRejected(t *testing.T) {
	p := makeTestParams()
	s := utxo.NewSet()
	kp := newTestKeyPair(t)

	var txHash1 types.Hash
	txHash1[0] = 0x01
	addUTXO(s, txHash1, 0, 1000, 0, false, kp.pkScript)

	block := &types.Block{
		Transactions: []types.Transaction{
			makeCoinbaseTx(5, p.CalcSubsidy(5)),
			{
				Version: 1,
				Inputs: []types.TxInput{
					{PreviousOutPoint: types.OutPoint{Hash: txHash1, Index: 0}, Sequence: 0xFFFFFFFF},
				},
				Outputs: []types.TxOutput{
					{Value: 0, PkScript: []byte{0x01}},
				},
			},
		},
	}
	signTxInput(t, &block.Transactions[1], 0, kp)

	_, err := ValidateTransactionInputs(block, s, 5, p)
	if err == nil {
		t.Fatal("expected rejection for zero-value output")
	}
}

func TestZeroValueCoinbaseOutputRejected(t *testing.T) {
	p := makeTestParams()
	s := utxo.NewSet()

	block := &types.Block{
		Transactions: []types.Transaction{
			{
				Version: 1,
				Inputs: []types.TxInput{{
					PreviousOutPoint: types.CoinbaseOutPoint,
					SignatureScript:  bip34ScriptSig(5, "test"),
					Sequence:         0xFFFFFFFF,
				}},
				Outputs: []types.TxOutput{
					{Value: 0, PkScript: []byte{0x00}},
				},
			},
		},
	}

	_, err := ValidateTransactionInputs(block, s, 5, p)
	if err == nil {
		t.Fatal("expected rejection for zero-value coinbase output")
	}
}

func TestNoInputsNonCoinbaseRejected(t *testing.T) {
	p := makeTestParams()
	s := utxo.NewSet()

	block := &types.Block{
		Transactions: []types.Transaction{
			makeCoinbaseTx(5, p.CalcSubsidy(5)),
			{
				Version: 1,
				Inputs:  []types.TxInput{},
				Outputs: []types.TxOutput{{Value: 100, PkScript: []byte{0x01}}},
			},
		},
	}

	_, err := ValidateTransactionInputs(block, s, 5, p)
	if err == nil {
		t.Fatal("expected rejection for non-coinbase tx with no inputs")
	}
}

func TestNoOutputsNonCoinbaseRejected(t *testing.T) {
	p := makeTestParams()
	s := utxo.NewSet()
	kp := newTestKeyPair(t)

	var txHash1 types.Hash
	txHash1[0] = 0x01
	addUTXO(s, txHash1, 0, 1000, 0, false, kp.pkScript)

	block := &types.Block{
		Transactions: []types.Transaction{
			makeCoinbaseTx(5, p.CalcSubsidy(5)),
			{
				Version: 1,
				Inputs: []types.TxInput{
					{PreviousOutPoint: types.OutPoint{Hash: txHash1, Index: 0}, Sequence: 0xFFFFFFFF},
				},
				Outputs: []types.TxOutput{},
			},
		},
	}
	signTxInput(t, &block.Transactions[1], 0, kp)

	_, err := ValidateTransactionInputs(block, s, 5, p)
	if err == nil {
		t.Fatal("expected rejection for non-coinbase tx with no outputs")
	}
}

func TestValidBlockAccepted(t *testing.T) {
	p := makeTestParams()
	s := utxo.NewSet()
	kp := newTestKeyPair(t)

	var txHash1 types.Hash
	txHash1[0] = 0x01
	addUTXO(s, txHash1, 0, 5000, 0, false, kp.pkScript)

	subsidy := p.CalcSubsidy(5)
	fee := uint64(500)

	block := &types.Block{
		Transactions: []types.Transaction{
			makeCoinbaseTx(5, subsidy+fee),
			{
				Version: 1,
				Inputs: []types.TxInput{
					{PreviousOutPoint: types.OutPoint{Hash: txHash1, Index: 0}, Sequence: 0xFFFFFFFF},
				},
				Outputs: []types.TxOutput{
					{Value: 3000, PkScript: []byte{0x01}},
					{Value: 1500, PkScript: []byte{0x02}},
				},
			},
		},
	}
	signTxInput(t, &block.Transactions[1], 0, kp)

	fees, err := ValidateTransactionInputs(block, s, 5, p)
	if err != nil {
		t.Fatalf("expected valid block to be accepted: %v", err)
	}
	if fees != fee {
		t.Fatalf("expected fees=%d, got %d", fee, fees)
	}
}

func TestSingleTxDuplicateInputRejected(t *testing.T) {
	p := makeTestParams()
	s := utxo.NewSet()
	kp := newTestKeyPair(t)

	var txHash1 types.Hash
	txHash1[0] = 0x01
	addUTXO(s, txHash1, 0, 1000, 0, false, kp.pkScript)

	tx := &types.Transaction{
		Version: 1,
		Inputs: []types.TxInput{
			{PreviousOutPoint: types.OutPoint{Hash: txHash1, Index: 0}, Sequence: 0xFFFFFFFF},
			{PreviousOutPoint: types.OutPoint{Hash: txHash1, Index: 0}, Sequence: 0xFFFFFFFF},
		},
		Outputs: []types.TxOutput{{Value: 500, PkScript: []byte{0x01}}},
	}
	signTxInput(t, tx, 0, kp)
	signTxInput(t, tx, 1, kp)

	_, err := ValidateSingleTransaction(tx, s, 4, p)
	if err == nil {
		t.Fatal("expected mempool rejection for duplicate inputs in single tx")
	}
}

func TestSingleTxZeroValueOutputRejected(t *testing.T) {
	p := makeTestParams()
	s := utxo.NewSet()
	kp := newTestKeyPair(t)

	var txHash1 types.Hash
	txHash1[0] = 0x01
	addUTXO(s, txHash1, 0, 1000, 0, false, kp.pkScript)

	tx := &types.Transaction{
		Version: 1,
		Inputs: []types.TxInput{
			{PreviousOutPoint: types.OutPoint{Hash: txHash1, Index: 0}, Sequence: 0xFFFFFFFF},
		},
		Outputs: []types.TxOutput{{Value: 0, PkScript: []byte{0x01}}},
	}
	signTxInput(t, tx, 0, kp)

	_, err := ValidateSingleTransaction(tx, s, 4, p)
	if err == nil {
		t.Fatal("expected mempool rejection for zero-value output")
	}
}

func TestSingleTxOverspendRejected(t *testing.T) {
	p := makeTestParams()
	s := utxo.NewSet()
	kp := newTestKeyPair(t)

	var txHash1 types.Hash
	txHash1[0] = 0x01
	addUTXO(s, txHash1, 0, 1000, 0, false, kp.pkScript)

	tx := &types.Transaction{
		Version: 1,
		Inputs: []types.TxInput{
			{PreviousOutPoint: types.OutPoint{Hash: txHash1, Index: 0}, Sequence: 0xFFFFFFFF},
		},
		Outputs: []types.TxOutput{{Value: 9999, PkScript: []byte{0x01}}},
	}
	signTxInput(t, tx, 0, kp)

	_, err := ValidateSingleTransaction(tx, s, 4, p)
	if err == nil {
		t.Fatal("expected mempool rejection for overspend")
	}
}

func TestSingleTxMissingUTXORejected(t *testing.T) {
	p := makeTestParams()
	s := utxo.NewSet()

	var fakeTxHash types.Hash
	fakeTxHash[0] = 0xFF

	tx := &types.Transaction{
		Version: 1,
		Inputs: []types.TxInput{
			{PreviousOutPoint: types.OutPoint{Hash: fakeTxHash, Index: 0}, Sequence: 0xFFFFFFFF},
		},
		Outputs: []types.TxOutput{{Value: 100, PkScript: []byte{0x01}}},
	}

	_, err := ValidateSingleTransaction(tx, s, 4, p)
	if err == nil {
		t.Fatal("expected mempool rejection for missing UTXO")
	}
}

func TestSingleTxImmatureCoinbaseRejected(t *testing.T) {
	p := makeTestParams()
	p.CoinbaseMaturity = 10
	kp := newTestKeyPair(t)

	s := utxo.NewSet()

	var cbHash types.Hash
	cbHash[0] = 0xCB
	addUTXO(s, cbHash, 0, 5000000000, 5, true, kp.pkScript)

	tx := &types.Transaction{
		Version: 1,
		Inputs: []types.TxInput{
			{PreviousOutPoint: types.OutPoint{Hash: cbHash, Index: 0}, Sequence: 0xFFFFFFFF},
		},
		Outputs: []types.TxOutput{{Value: 1000, PkScript: []byte{0x01}}},
	}
	signTxInput(t, tx, 0, kp)

	_, err := ValidateSingleTransaction(tx, s, 7, p)
	if err == nil {
		t.Fatal("expected mempool rejection for immature coinbase spend")
	}
}

func TestSingleTxValidAccepted(t *testing.T) {
	p := makeTestParams()
	s := utxo.NewSet()
	kp := newTestKeyPair(t)

	var txHash1 types.Hash
	txHash1[0] = 0x01
	addUTXO(s, txHash1, 0, 5000, 0, false, kp.pkScript)

	tx := &types.Transaction{
		Version: 1,
		Inputs: []types.TxInput{
			{PreviousOutPoint: types.OutPoint{Hash: txHash1, Index: 0}, Sequence: 0xFFFFFFFF},
		},
		Outputs: []types.TxOutput{
			{Value: 3000, PkScript: []byte{0x01}},
			{Value: 1500, PkScript: []byte{0x02}},
		},
	}
	signTxInput(t, tx, 0, kp)

	fee, err := ValidateSingleTransaction(tx, s, 4, p)
	if err != nil {
		t.Fatalf("expected valid tx to be accepted: %v", err)
	}
	if fee != 500 {
		t.Fatalf("expected fee=500, got %d", fee)
	}
}
