package wallet

import (
	"testing"

	"github.com/bams-repo/fairchain/internal/crypto"
	"github.com/bams-repo/fairchain/internal/script"
	"github.com/bams-repo/fairchain/internal/types"
)

func makeTestWallet(t *testing.T) *HDWallet {
	t.Helper()
	dir := t.TempDir()
	w, err := NewHDWallet(dir, 0x00)
	if err != nil {
		t.Fatalf("NewHDWallet: %v", err)
	}
	return w
}

func makeTestUTXOs(w *HDWallet, values []uint64, height uint32) []UnspentOutput {
	script := w.GetDefaultP2PKHScript()
	addr := w.GetDefaultAddress()
	var utxos []UnspentOutput
	for i, v := range values {
		var txHash [32]byte
		txHash[0] = byte(i + 1)
		utxos = append(utxos, UnspentOutput{
			TxHash:        txHash,
			Index:         0,
			Value:         v,
			Height:        height,
			Confirmations: 10,
			Address:       addr,
			PkScript:      script,
			IsCoinbase:    false,
		})
	}
	return utxos
}

func TestBuildTransactionBasic(t *testing.T) {
	w := makeTestWallet(t)
	utxos := makeTestUTXOs(w, []uint64{100_000_000}, 1) // 1 FAIR

	// Create a second wallet to get a destination address.
	dest := makeTestWallet(t)
	destAddr := dest.GetDefaultAddress()

	tx, err := w.BuildTransaction(
		SendRequest{ToAddress: destAddr, Amount: 50_000_000},
		1, // 1 sat/byte
		utxos,
		100, // coinbase maturity
		100, // tip height
	)
	if err != nil {
		t.Fatalf("BuildTransaction: %v", err)
	}

	if len(tx.Inputs) != 1 {
		t.Fatalf("expected 1 input, got %d", len(tx.Inputs))
	}
	if len(tx.Outputs) < 1 || len(tx.Outputs) > 2 {
		t.Fatalf("expected 1-2 outputs, got %d", len(tx.Outputs))
	}

	// First output should be the destination.
	if tx.Outputs[0].Value != 50_000_000 {
		t.Fatalf("destination output value: got %d, want 50000000", tx.Outputs[0].Value)
	}

	// Verify the destination script pays to the right address.
	_, destPKH, _ := crypto.AddressToPubKeyHash(destAddr)
	expectedScript := crypto.MakeP2PKHScript(destPKH)
	if !bytesEqual(tx.Outputs[0].PkScript, expectedScript) {
		t.Fatal("destination script mismatch")
	}

	// If there's a change output, verify it's ours.
	if len(tx.Outputs) == 2 {
		if !w.IsOurScript(tx.Outputs[1].PkScript) {
			t.Fatal("change output should belong to our wallet")
		}
	}

	// Verify total output + fee <= total input.
	var totalOut uint64
	for _, out := range tx.Outputs {
		totalOut += out.Value
	}
	if totalOut > utxos[0].Value {
		t.Fatalf("total output %d exceeds input %d", totalOut, utxos[0].Value)
	}
	fee := utxos[0].Value - totalOut
	if fee == 0 {
		t.Fatal("fee should be non-zero")
	}
}

func TestBuildTransactionSignatureVerifies(t *testing.T) {
	w := makeTestWallet(t)
	utxos := makeTestUTXOs(w, []uint64{500_000_000}, 1)

	dest := makeTestWallet(t)
	destAddr := dest.GetDefaultAddress()

	tx, err := w.BuildTransaction(
		SendRequest{ToAddress: destAddr, Amount: 100_000_000},
		1,
		utxos,
		100,
		100,
	)
	if err != nil {
		t.Fatalf("BuildTransaction: %v", err)
	}

	// Verify each input's signature using the script engine.
	for i := range tx.Inputs {
		prevScript := utxos[i].PkScript
		if err := script.Verify(tx.Inputs[i].SignatureScript, prevScript, tx, i); err != nil {
			t.Fatalf("script verification failed for input %d: %v", i, err)
		}
	}
}

func TestBuildTransactionMultipleInputs(t *testing.T) {
	w := makeTestWallet(t)
	utxos := makeTestUTXOs(w, []uint64{30_000_000, 40_000_000, 50_000_000}, 1)

	dest := makeTestWallet(t)
	destAddr := dest.GetDefaultAddress()

	tx, err := w.BuildTransaction(
		SendRequest{ToAddress: destAddr, Amount: 100_000_000},
		1,
		utxos,
		100,
		100,
	)
	if err != nil {
		t.Fatalf("BuildTransaction: %v", err)
	}

	// Should need all 3 inputs (30+40+50 = 120M, need 100M + fee).
	if len(tx.Inputs) < 2 {
		t.Fatalf("expected multiple inputs, got %d", len(tx.Inputs))
	}

	// Verify all signatures.
	for i := range tx.Inputs {
		for _, u := range utxos {
			if u.TxHash == tx.Inputs[i].PreviousOutPoint.Hash {
				if err := script.Verify(tx.Inputs[i].SignatureScript, u.PkScript, tx, i); err != nil {
					t.Fatalf("script verification failed for input %d: %v", i, err)
				}
				break
			}
		}
	}
}

func TestBuildTransactionInsufficientFunds(t *testing.T) {
	w := makeTestWallet(t)
	utxos := makeTestUTXOs(w, []uint64{1000}, 1)

	dest := makeTestWallet(t)
	destAddr := dest.GetDefaultAddress()

	_, err := w.BuildTransaction(
		SendRequest{ToAddress: destAddr, Amount: 100_000_000},
		1,
		utxos,
		100,
		100,
	)
	if err == nil {
		t.Fatal("expected insufficient funds error")
	}
}

func TestBuildTransactionZeroAmount(t *testing.T) {
	w := makeTestWallet(t)
	utxos := makeTestUTXOs(w, []uint64{100_000_000}, 1)

	dest := makeTestWallet(t)
	destAddr := dest.GetDefaultAddress()

	_, err := w.BuildTransaction(
		SendRequest{ToAddress: destAddr, Amount: 0},
		1,
		utxos,
		100,
		100,
	)
	if err == nil {
		t.Fatal("expected error for zero amount")
	}
}

func TestBuildTransactionInvalidAddress(t *testing.T) {
	w := makeTestWallet(t)
	utxos := makeTestUTXOs(w, []uint64{100_000_000}, 1)

	_, err := w.BuildTransaction(
		SendRequest{ToAddress: "notanaddress", Amount: 50_000_000},
		1,
		utxos,
		100,
		100,
	)
	if err == nil {
		t.Fatal("expected error for invalid address")
	}
}

func TestBuildTransactionDustChange(t *testing.T) {
	w := makeTestWallet(t)

	// Craft a UTXO where the change would be dust.
	// 1 input (148 bytes) + 1 output (34 bytes) + overhead (10 bytes) = 192 bytes
	// Fee at 1 sat/byte = 192 sats
	// If we send (value - 192 - 500) where 500 < DustThreshold, change should be dropped.
	value := uint64(100_000)
	fee1out := uint64(192) // estimated fee with 1 output
	sendAmount := value - fee1out - 100 // 100 sats change (below dust threshold of 546)
	utxos := makeTestUTXOs(w, []uint64{value}, 1)

	dest := makeTestWallet(t)
	destAddr := dest.GetDefaultAddress()

	tx, err := w.BuildTransaction(
		SendRequest{ToAddress: destAddr, Amount: sendAmount},
		1,
		utxos,
		100,
		100,
	)
	if err != nil {
		t.Fatalf("BuildTransaction: %v", err)
	}

	// With dust change, there should be only 1 output (dust absorbed into fee).
	if len(tx.Outputs) != 1 {
		t.Fatalf("expected 1 output (dust change absorbed), got %d", len(tx.Outputs))
	}
}

func TestBuildTransactionCoinbaseMaturity(t *testing.T) {
	w := makeTestWallet(t)

	// Create a coinbase UTXO that is immature.
	script := w.GetDefaultP2PKHScript()
	addr := w.GetDefaultAddress()
	utxos := []UnspentOutput{{
		TxHash:        [32]byte{1},
		Index:         0,
		Value:         500_000_000_0,
		Height:        90,
		Confirmations: 11,
		Address:       addr,
		PkScript:      script,
		IsCoinbase:    true,
	}}

	dest := makeTestWallet(t)
	destAddr := dest.GetDefaultAddress()

	// With coinbase maturity of 100, this UTXO (11 confirmations) should not be spendable.
	_, err := w.BuildTransaction(
		SendRequest{ToAddress: destAddr, Amount: 100_000_000},
		1,
		utxos,
		100, // coinbase maturity
		100, // tip height
	)
	if err == nil {
		t.Fatal("expected error for immature coinbase")
	}
}

func TestBuildTransactionMatureCoinbase(t *testing.T) {
	w := makeTestWallet(t)

	script := w.GetDefaultP2PKHScript()
	addr := w.GetDefaultAddress()
	utxos := []UnspentOutput{{
		TxHash:        [32]byte{1},
		Index:         0,
		Value:         500_000_000_0,
		Height:        1,
		Confirmations: 200,
		Address:       addr,
		PkScript:      script,
		IsCoinbase:    true,
	}}

	dest := makeTestWallet(t)
	destAddr := dest.GetDefaultAddress()

	tx, err := w.BuildTransaction(
		SendRequest{ToAddress: destAddr, Amount: 100_000_000},
		1,
		utxos,
		100,
		200,
	)
	if err != nil {
		t.Fatalf("BuildTransaction: %v", err)
	}
	if len(tx.Inputs) != 1 {
		t.Fatalf("expected 1 input, got %d", len(tx.Inputs))
	}
}

func TestEstimateFee(t *testing.T) {
	fee := EstimateFee(1, 2, 1)
	expected := uint64(10 + 148 + 2*34) // overhead + 1 input + 2 outputs = 226
	if fee != expected {
		t.Fatalf("EstimateFee: got %d, want %d", fee, expected)
	}

	fee10 := EstimateFee(1, 2, 10)
	if fee10 != expected*10 {
		t.Fatalf("EstimateFee at 10 sat/byte: got %d, want %d", fee10, expected*10)
	}
}

func TestSignRawTransaction(t *testing.T) {
	w := makeTestWallet(t)
	pkScript := w.GetDefaultP2PKHScript()

	// Build a simple unsigned transaction.
	var prevHash [32]byte
	prevHash[0] = 0x01
	tx := &types.Transaction{
		Version: 1,
		Inputs: []types.TxInput{{
			PreviousOutPoint: types.OutPoint{Hash: prevHash, Index: 0},
			Sequence:         0xFFFFFFFF,
		}},
		Outputs: []types.TxOutput{{
			Value:    50_000_000,
			PkScript: pkScript,
		}},
		LockTime: 0,
	}

	getPrevScript := func(txHash [32]byte, index uint32) []byte {
		if txHash == prevHash && index == 0 {
			return pkScript
		}
		return nil
	}

	signed, complete, err := w.SignRawTransaction(tx, getPrevScript)
	if err != nil {
		t.Fatalf("SignRawTransaction: %v", err)
	}
	if signed != 1 {
		t.Fatalf("expected 1 signed input, got %d", signed)
	}
	if !complete {
		t.Fatal("expected transaction to be complete")
	}

	// Verify the signature.
	if err := script.Verify(tx.Inputs[0].SignatureScript, pkScript, tx, 0); err != nil {
		t.Fatalf("script verification failed: %v", err)
	}
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
