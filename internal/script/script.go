package script

import (
	"fmt"

	"github.com/bams-repo/fairchain/internal/crypto"
	"github.com/bams-repo/fairchain/internal/types"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
)

// Opcodes supported by the script engine.
const (
	OpFalse       = 0x00
	OpPushData1   = 0x4c
	OpPushData2   = 0x4d
	Op1           = 0x51
	OpDup         = 0x76
	OpHash160     = 0xa9
	OpEqual       = 0x87
	OpEqualVerify = 0x88
	OpCheckSig    = 0xac
	OpReturn      = 0x6a
)

const (
	maxStackSize      = 1000
	maxScriptSize     = 10000 // Bitcoin consensus limit.
	maxOpsPerScript   = 201   // Bitcoin limit on non-push opcodes per script.
	maxStackElemSize  = 520   // Bitcoin limit on individual stack element size.
)

// Verify executes the combined script (sigScript + pkScript) and returns nil
// if the spend is authorized. This is the consensus-critical entry point.
//
// For P2PKH, the combined execution is:
//  1. Push signature (from sigScript)
//  2. Push pubkey (from sigScript)
//  3. OP_DUP
//  4. OP_HASH160
//  5. Push expected pubkey hash (from pkScript)
//  6. OP_EQUALVERIFY
//  7. OP_CHECKSIG
//
// The tx and inputIdx are needed for CHECKSIG to compute the sighash.
func Verify(sigScript, pkScript []byte, tx *types.Transaction, inputIdx int) error {
	if len(pkScript) == 0 {
		return fmt.Errorf("empty pkScript")
	}

	// OP_RETURN scripts are provably unspendable.
	if pkScript[0] == OpReturn {
		return fmt.Errorf("OP_RETURN output is unspendable")
	}

	vm := &engine{
		tx:       tx,
		inputIdx: inputIdx,
		pkScript: pkScript,
	}

	if err := vm.execute(sigScript); err != nil {
		return fmt.Errorf("sigScript execution: %w", err)
	}

	if err := vm.execute(pkScript); err != nil {
		return fmt.Errorf("pkScript execution: %w", err)
	}

	if vm.stack.size() == 0 {
		return fmt.Errorf("script evaluated to empty stack")
	}

	top := vm.stack.peek()
	if !isTrue(top) {
		return fmt.Errorf("script evaluated to false")
	}

	// Cleanstack: exactly one element must remain after execution.
	// This prevents non-canonical transaction forms and matches Bitcoin's
	// SCRIPT_VERIFY_CLEANSTACK behavior.
	if vm.stack.size() != 1 {
		return fmt.Errorf("cleanstack violation: %d items remain on stack (expected 1)", vm.stack.size())
	}

	return nil
}

type engine struct {
	stack    stack
	tx       *types.Transaction
	inputIdx int
	pkScript []byte
}

func (e *engine) execute(script []byte) error {
	if len(script) > maxScriptSize {
		return fmt.Errorf("script size %d exceeds maximum %d", len(script), maxScriptSize)
	}

	var numOps int
	pos := 0
	for pos < len(script) {
		op := script[pos]
		pos++

		// Count non-push opcodes toward the per-script limit.
		if op > OpPushData2 {
			numOps++
			if numOps > maxOpsPerScript {
				return fmt.Errorf("exceeded maximum opcode count (%d)", maxOpsPerScript)
			}
		}

		switch {
		case op == OpFalse:
			e.stack.push([]byte{})

		case op >= 0x01 && op <= 0x4b:
			n := int(op)
			if pos+n > len(script) {
				return fmt.Errorf("push %d bytes: script too short at offset %d", n, pos)
			}
			if n > maxStackElemSize {
				return fmt.Errorf("push size %d exceeds maximum element size %d", n, maxStackElemSize)
			}
			data := make([]byte, n)
			copy(data, script[pos:pos+n])
			e.stack.push(data)
			pos += n

		case op == OpPushData1:
			if pos >= len(script) {
				return fmt.Errorf("OP_PUSHDATA1: missing length byte")
			}
			n := int(script[pos])
			pos++
			if pos+n > len(script) {
				return fmt.Errorf("OP_PUSHDATA1: script too short")
			}
			if n > maxStackElemSize {
				return fmt.Errorf("OP_PUSHDATA1: push size %d exceeds maximum element size %d", n, maxStackElemSize)
			}
			data := make([]byte, n)
			copy(data, script[pos:pos+n])
			e.stack.push(data)
			pos += n

		case op == OpPushData2:
			if pos+2 > len(script) {
				return fmt.Errorf("OP_PUSHDATA2: missing length bytes")
			}
			n := int(script[pos]) | int(script[pos+1])<<8
			pos += 2
			if pos+n > len(script) {
				return fmt.Errorf("OP_PUSHDATA2: script too short")
			}
			if n > maxStackElemSize {
				return fmt.Errorf("OP_PUSHDATA2: push size %d exceeds maximum element size %d", n, maxStackElemSize)
			}
			data := make([]byte, n)
			copy(data, script[pos:pos+n])
			e.stack.push(data)
			pos += n

		case op == Op1:
			e.stack.push([]byte{1})

		case op == OpDup:
			if e.stack.size() < 1 {
				return fmt.Errorf("OP_DUP: stack underflow")
			}
			top := e.stack.peek()
			dup := make([]byte, len(top))
			copy(dup, top)
			e.stack.push(dup)

		case op == OpHash160:
			if e.stack.size() < 1 {
				return fmt.Errorf("OP_HASH160: stack underflow")
			}
			data := e.stack.pop()
			hash := crypto.Hash160(data)
			e.stack.push(hash[:])

		case op == OpEqual:
			if e.stack.size() < 2 {
				return fmt.Errorf("OP_EQUAL: stack underflow")
			}
			b := e.stack.pop()
			a := e.stack.pop()
			if bytesEqual(a, b) {
				e.stack.push([]byte{1})
			} else {
				e.stack.push([]byte{})
			}

		case op == OpEqualVerify:
			if e.stack.size() < 2 {
				return fmt.Errorf("OP_EQUALVERIFY: stack underflow")
			}
			b := e.stack.pop()
			a := e.stack.pop()
			if !bytesEqual(a, b) {
				return fmt.Errorf("OP_EQUALVERIFY: values not equal")
			}

		case op == OpCheckSig:
			if e.stack.size() < 2 {
				return fmt.Errorf("OP_CHECKSIG: stack underflow")
			}
			pubKeyBytes := e.stack.pop()
			sigBytes := e.stack.pop()

			if len(sigBytes) < 2 {
				e.stack.push([]byte{})
				continue
			}

			hashType := sigBytes[len(sigBytes)-1]
			if hashType != crypto.SigHashAll {
				e.stack.push([]byte{})
				continue
			}

			derSig := sigBytes[:len(sigBytes)-1]
			sig, err := ecdsa.ParseDERSignature(derSig)
			if err != nil {
				e.stack.push([]byte{})
				continue
			}

			pubKey, err := secp256k1.ParsePubKey(pubKeyBytes)
			if err != nil {
				e.stack.push([]byte{})
				continue
			}

			sigHash, err := crypto.ComputeSigHash(e.tx, e.inputIdx, e.pkScript)
			if err != nil {
				e.stack.push([]byte{})
				continue
			}

			if sig.Verify(sigHash[:], pubKey) {
				e.stack.push([]byte{1})
			} else {
				e.stack.push([]byte{})
			}

		case op == OpReturn:
			return fmt.Errorf("OP_RETURN: script is unspendable")

		default:
			return fmt.Errorf("unsupported opcode 0x%02x at offset %d", op, pos-1)
		}

		if e.stack.size() > maxStackSize {
			return fmt.Errorf("stack overflow: %d items", e.stack.size())
		}
	}

	return nil
}

type stack struct {
	items [][]byte
}

func (s *stack) push(data []byte) {
	s.items = append(s.items, data)
}

func (s *stack) pop() []byte {
	if len(s.items) == 0 {
		return nil
	}
	top := s.items[len(s.items)-1]
	s.items = s.items[:len(s.items)-1]
	return top
}

func (s *stack) peek() []byte {
	if len(s.items) == 0 {
		return nil
	}
	return s.items[len(s.items)-1]
}

func (s *stack) size() int {
	return len(s.items)
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

// isTrue implements Bitcoin's CastToBool: a byte vector is false if it is
// empty, all zeros, or negative zero (0x80 in the last byte with all other
// bytes zero). Everything else is true.
func isTrue(data []byte) bool {
	for i, b := range data {
		if b != 0 {
			// Negative zero: last byte is 0x80, all preceding bytes are 0x00.
			if i == len(data)-1 && b == 0x80 {
				return false
			}
			return true
		}
	}
	return false
}

// IsStandardScript returns true if the pkScript is a recognized standard type.
// Currently only P2PKH and OP_RETURN are standard.
func IsStandardScript(pkScript []byte) bool {
	if crypto.IsP2PKHScript(pkScript) {
		return true
	}
	if len(pkScript) > 0 && pkScript[0] == OpReturn {
		return true
	}
	return false
}

// IsLegacyUnvalidatedScript always returns false. Legacy placeholder scripts
// ({0x00}) are excluded from the UTXO set at genesis insertion time, so they
// should never appear. If one is encountered, the script engine will reject
// it (OP_FALSE leaves false on stack), making it unspendable by design.
func IsLegacyUnvalidatedScript(pkScript []byte) bool {
	return false
}
