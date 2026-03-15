package script

import (
	"testing"

	"github.com/bams-repo/fairchain/internal/crypto"
	"github.com/bams-repo/fairchain/internal/types"
)

func makeSignedTx(t *testing.T) (*types.Transaction, []byte, []byte) {
	t.Helper()
	privBytes, pubBytes, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	privKey, err := crypto.PrivKeyFromBytes(privBytes)
	if err != nil {
		t.Fatal(err)
	}

	pkScript := crypto.MakeP2PKHScriptFromPubKey(pubBytes)

	tx := &types.Transaction{
		Version: 1,
		Inputs: []types.TxInput{{
			PreviousOutPoint: types.OutPoint{
				Hash:  crypto.DoubleSHA256([]byte("prev-tx")),
				Index: 0,
			},
			Sequence: 0xFFFFFFFF,
		}},
		Outputs: []types.TxOutput{{
			Value:    50000,
			PkScript: pkScript,
		}},
		LockTime: 0,
	}

	sigScript, err := crypto.SignInput(tx, 0, pkScript, privKey)
	if err != nil {
		t.Fatal(err)
	}
	tx.Inputs[0].SignatureScript = sigScript

	return tx, pkScript, pubBytes
}

func TestVerifyP2PKHValid(t *testing.T) {
	tx, pkScript, _ := makeSignedTx(t)
	if err := Verify(tx.Inputs[0].SignatureScript, pkScript, tx, 0); err != nil {
		t.Fatalf("valid P2PKH spend should verify: %v", err)
	}
}

func TestVerifyP2PKHWrongKey(t *testing.T) {
	tx, _, _ := makeSignedTx(t)

	// Create a P2PKH script for a different key.
	_, otherPub, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	otherPkScript := crypto.MakeP2PKHScriptFromPubKey(otherPub)

	if err := Verify(tx.Inputs[0].SignatureScript, otherPkScript, tx, 0); err == nil {
		t.Fatal("spending with wrong key should fail")
	}
}

func TestVerifyP2PKHNoSignature(t *testing.T) {
	_, pubBytes, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	pkScript := crypto.MakeP2PKHScriptFromPubKey(pubBytes)

	tx := &types.Transaction{
		Version: 1,
		Inputs: []types.TxInput{{
			PreviousOutPoint: types.OutPoint{
				Hash:  crypto.DoubleSHA256([]byte("prev-tx")),
				Index: 0,
			},
			SignatureScript: []byte("STOLEN-no-signature-required"),
			Sequence:        0xFFFFFFFF,
		}},
		Outputs: []types.TxOutput{{
			Value:    50000,
			PkScript: pkScript,
		}},
		LockTime: 0,
	}

	if err := Verify(tx.Inputs[0].SignatureScript, pkScript, tx, 0); err == nil {
		t.Fatal("arbitrary bytes should not satisfy P2PKH script")
	}
}

func TestVerifyP2PKHTamperedOutput(t *testing.T) {
	tx, pkScript, _ := makeSignedTx(t)

	// Tamper with the output value after signing.
	tx.Outputs[0].Value = 99999

	if err := Verify(tx.Inputs[0].SignatureScript, pkScript, tx, 0); err == nil {
		t.Fatal("tampered transaction should fail verification")
	}
}

func TestVerifyOpReturnUnspendable(t *testing.T) {
	opReturnScript := crypto.MakeOpReturnScript([]byte("burn data"))

	tx := &types.Transaction{
		Version: 1,
		Inputs: []types.TxInput{{
			PreviousOutPoint: types.OutPoint{
				Hash:  crypto.DoubleSHA256([]byte("prev-tx")),
				Index: 0,
			},
			SignatureScript: []byte("anything"),
			Sequence:        0xFFFFFFFF,
		}},
		Outputs: []types.TxOutput{{Value: 1000, PkScript: []byte{0x00}}},
		LockTime: 0,
	}

	if err := Verify(tx.Inputs[0].SignatureScript, opReturnScript, tx, 0); err == nil {
		t.Fatal("OP_RETURN output should be unspendable")
	}
}

func TestVerifyBurnScriptUnspendable(t *testing.T) {
	burnScript := []byte("burn:testnet:premine:v1")

	tx := &types.Transaction{
		Version: 1,
		Inputs: []types.TxInput{{
			PreviousOutPoint: types.OutPoint{
				Hash:  crypto.DoubleSHA256([]byte("genesis-tx")),
				Index: 1,
			},
			SignatureScript: []byte("PREMINE-THEFT-no-script-validation"),
			Sequence:        0xFFFFFFFF,
		}},
		Outputs: []types.TxOutput{{Value: 1000, PkScript: []byte{0x00}}},
		LockTime: 0,
	}

	if err := Verify(tx.Inputs[0].SignatureScript, burnScript, tx, 0); err == nil {
		t.Fatal("burn script should be unspendable — script engine should reject non-standard opcodes")
	}
}

func TestVerifyEmptyPkScript(t *testing.T) {
	tx := &types.Transaction{
		Version: 1,
		Inputs: []types.TxInput{{
			PreviousOutPoint: types.OutPoint{
				Hash:  crypto.DoubleSHA256([]byte("prev-tx")),
				Index: 0,
			},
			SignatureScript: []byte("anything"),
			Sequence:        0xFFFFFFFF,
		}},
		Outputs: []types.TxOutput{{Value: 1000, PkScript: []byte{0x00}}},
		LockTime: 0,
	}

	if err := Verify(tx.Inputs[0].SignatureScript, []byte{}, tx, 0); err == nil {
		t.Fatal("empty pkScript should fail")
	}
}

func TestIsLegacyUnvalidatedScript(t *testing.T) {
	tests := []struct {
		name   string
		script []byte
	}{
		{"nil", nil},
		{"empty", []byte{}},
		{"single zero byte", []byte{0x00}},
		{"P2PKH", crypto.MakeP2PKHScript([20]byte{1, 2, 3})},
		{"OP_RETURN", []byte{0x6a, 0x04, 0xde, 0xad, 0xbe, 0xef}},
		{"burn marker", []byte("burn:testnet:premine:v1")},
		{"arbitrary", []byte("some random script")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if IsLegacyUnvalidatedScript(tt.script) {
				t.Errorf("IsLegacyUnvalidatedScript(%v) = true, want false for all inputs", tt.script)
			}
		})
	}
}

func TestIsStandardScript(t *testing.T) {
	p2pkh := crypto.MakeP2PKHScript([20]byte{1, 2, 3})
	opReturn := []byte{0x6a, 0x04, 0xde, 0xad}

	if !IsStandardScript(p2pkh) {
		t.Fatal("P2PKH should be standard")
	}
	if !IsStandardScript(opReturn) {
		t.Fatal("OP_RETURN should be standard")
	}
	if IsStandardScript([]byte{0x00}) {
		t.Fatal("single zero byte should not be standard")
	}
	if IsStandardScript([]byte("burn:testnet:premine:v1")) {
		t.Fatal("burn marker should not be standard")
	}
}

func TestStealUTXOAttackBlocked(t *testing.T) {
	privBytes, pubBytes, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	_ = privBytes

	pkScript := crypto.MakeP2PKHScriptFromPubKey(pubBytes)

	tx := &types.Transaction{
		Version: 1,
		Inputs: []types.TxInput{{
			PreviousOutPoint: types.OutPoint{
				Hash:  crypto.DoubleSHA256([]byte("miner-coinbase")),
				Index: 0,
			},
			SignatureScript: []byte("STOLEN-no-signature-required"),
			Sequence:        0xFFFFFFFF,
		}},
		Outputs: []types.TxOutput{{
			Value:    49999900,
			PkScript: []byte("attacker-wallet-addr"),
		}},
		LockTime: 0,
	}

	err = Verify(tx.Inputs[0].SignatureScript, pkScript, tx, 0)
	if err == nil {
		t.Fatal("steal-utxo attack should be blocked by script validation")
	}
	t.Logf("steal-utxo correctly rejected: %v", err)
}

func TestStealPremineAttackBlocked(t *testing.T) {
	burnScript := []byte("burn:testnet:premine:v1")

	tx := &types.Transaction{
		Version: 1,
		Inputs: []types.TxInput{{
			PreviousOutPoint: types.OutPoint{
				Hash:  crypto.DoubleSHA256([]byte("genesis-coinbase")),
				Index: 1,
			},
			SignatureScript: []byte("PREMINE-THEFT-no-script-validation"),
			Sequence:        0xFFFFFFFF,
		}},
		Outputs: []types.TxOutput{
			{Value: 209999999769000, PkScript: []byte("attacker-wallet-1")},
			{Value: 209999999769000, PkScript: []byte("attacker-wallet-2")},
		},
		LockTime: 0,
	}

	err := Verify(tx.Inputs[0].SignatureScript, burnScript, tx, 0)
	if err == nil {
		t.Fatal("steal-premine attack should be blocked by script validation")
	}
	t.Logf("steal-premine correctly rejected: %v", err)
}
