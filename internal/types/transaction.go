package types

import (
	"encoding/binary"
	"fmt"
	"io"
)

// OutPoint references a specific output of a previous transaction.
// Consensus-critical: serialized as PrevHash (32) + Index (4 LE).
type OutPoint struct {
	Hash  Hash   // Transaction hash containing the referenced output.
	Index uint32 // Zero-based index of the output within that transaction.
}

// CoinbaseOutPoint is the outpoint used in coinbase transaction inputs.
var CoinbaseOutPoint = OutPoint{Hash: ZeroHash, Index: 0xFFFFFFFF}

// TxInput represents a transaction input.
// For coinbase transactions: PreviousOutPoint == CoinbaseOutPoint, SignatureScript contains the coinbase data.
// For regular transactions: SignatureScript will eventually hold a signature/proof of spend authority.
type TxInput struct {
	PreviousOutPoint OutPoint
	SignatureScript  []byte // Coinbase data or future signature proof.
	Sequence         uint32 // Reserved for future use (e.g., relative timelocks).
}

// IsCoinbase returns true if this input is a coinbase input.
func (in *TxInput) IsCoinbase() bool {
	return in.PreviousOutPoint.Hash.IsZero() && in.PreviousOutPoint.Index == 0xFFFFFFFF
}

// TxOutput represents a transaction output.
// Value is in the smallest denomination (satoshi-equivalent).
// PkScript is a placeholder for the locking script / recipient identifier.
type TxOutput struct {
	Value    uint64
	PkScript []byte // Locking script or recipient address placeholder.
}

// Transaction represents a blockchain transaction.
// Version allows future format upgrades.
type Transaction struct {
	Version  uint32
	Inputs   []TxInput
	Outputs  []TxOutput
	LockTime uint32
}

// IsCoinbase returns true if this is a coinbase transaction.
func (tx *Transaction) IsCoinbase() bool {
	return len(tx.Inputs) == 1 && tx.Inputs[0].IsCoinbase()
}

// ---- Canonical Serialization ----
// All consensus-critical encoding uses explicit little-endian binary format.
// Variable-length fields are prefixed with a varint (Bitcoin-style compact size).

// WriteVarInt writes a variable-length integer to w.
func WriteVarInt(w io.Writer, val uint64) error {
	var buf [9]byte
	n := PutVarInt(buf[:], val)
	_, err := w.Write(buf[:n])
	return err
}

// PutVarInt encodes a variable-length integer into buf and returns bytes written.
func PutVarInt(buf []byte, val uint64) int {
	switch {
	case val < 0xFD:
		buf[0] = byte(val)
		return 1
	case val <= 0xFFFF:
		buf[0] = 0xFD
		binary.LittleEndian.PutUint16(buf[1:], uint16(val))
		return 3
	case val <= 0xFFFFFFFF:
		buf[0] = 0xFE
		binary.LittleEndian.PutUint32(buf[1:], uint32(val))
		return 5
	default:
		buf[0] = 0xFF
		binary.LittleEndian.PutUint64(buf[1:], val)
		return 9
	}
}

// ReadVarInt reads a variable-length integer from r.
// Rejects non-canonical (non-minimal) encodings to prevent malleability.
func ReadVarInt(r io.Reader) (uint64, error) {
	var discriminant [1]byte
	if _, err := io.ReadFull(r, discriminant[:]); err != nil {
		return 0, err
	}
	switch discriminant[0] {
	case 0xFD:
		var buf [2]byte
		if _, err := io.ReadFull(r, buf[:]); err != nil {
			return 0, err
		}
		val := uint64(binary.LittleEndian.Uint16(buf[:]))
		if val < 0xFD {
			return 0, fmt.Errorf("non-canonical varint: 0xFD used for value %d", val)
		}
		return val, nil
	case 0xFE:
		var buf [4]byte
		if _, err := io.ReadFull(r, buf[:]); err != nil {
			return 0, err
		}
		val := uint64(binary.LittleEndian.Uint32(buf[:]))
		if val <= 0xFFFF {
			return 0, fmt.Errorf("non-canonical varint: 0xFE used for value %d", val)
		}
		return val, nil
	case 0xFF:
		var buf [8]byte
		if _, err := io.ReadFull(r, buf[:]); err != nil {
			return 0, err
		}
		val := binary.LittleEndian.Uint64(buf[:])
		if val <= 0xFFFFFFFF {
			return 0, fmt.Errorf("non-canonical varint: 0xFF used for value %d", val)
		}
		return val, nil
	default:
		return uint64(discriminant[0]), nil
	}
}

// ReadVarIntFromBytes reads a varint from a byte slice without an io.Reader.
// Rejects non-canonical (non-minimal) encodings to prevent malleability.
func ReadVarIntFromBytes(data []byte) (uint64, error) {
	if len(data) == 0 {
		return 0, fmt.Errorf("empty data for varint")
	}
	switch data[0] {
	case 0xFD:
		if len(data) < 3 {
			return 0, fmt.Errorf("varint 0xFD needs 3 bytes, got %d", len(data))
		}
		val := uint64(binary.LittleEndian.Uint16(data[1:3]))
		if val < 0xFD {
			return 0, fmt.Errorf("non-canonical varint: 0xFD used for value %d", val)
		}
		return val, nil
	case 0xFE:
		if len(data) < 5 {
			return 0, fmt.Errorf("varint 0xFE needs 5 bytes, got %d", len(data))
		}
		val := uint64(binary.LittleEndian.Uint32(data[1:5]))
		if val <= 0xFFFF {
			return 0, fmt.Errorf("non-canonical varint: 0xFE used for value %d", val)
		}
		return val, nil
	case 0xFF:
		if len(data) < 9 {
			return 0, fmt.Errorf("varint 0xFF needs 9 bytes, got %d", len(data))
		}
		val := binary.LittleEndian.Uint64(data[1:9])
		if val <= 0xFFFFFFFF {
			return 0, fmt.Errorf("non-canonical varint: 0xFF used for value %d", val)
		}
		return val, nil
	default:
		return uint64(data[0]), nil
	}
}

// VarIntSize returns the encoded size of a varint value.
func VarIntSize(val uint64) int {
	switch {
	case val < 0xFD:
		return 1
	case val <= 0xFFFF:
		return 3
	case val <= 0xFFFFFFFF:
		return 5
	default:
		return 9
	}
}

// SerializeSize returns the canonical byte size of the transaction.
func (tx *Transaction) SerializeSize() int {
	// version(4) + varint(inputs) + inputs + varint(outputs) + outputs + locktime(4)
	n := 4 + VarIntSize(uint64(len(tx.Inputs)))
	for i := range tx.Inputs {
		n += tx.Inputs[i].SerializeSize()
	}
	n += VarIntSize(uint64(len(tx.Outputs)))
	for i := range tx.Outputs {
		n += tx.Outputs[i].SerializeSize()
	}
	n += 4
	return n
}

// SerializeSize returns the canonical byte size of a TxInput.
func (in *TxInput) SerializeSize() int {
	// outpoint(36) + varint(sigscript len) + sigscript + sequence(4)
	return 36 + VarIntSize(uint64(len(in.SignatureScript))) + len(in.SignatureScript) + 4
}

// SerializeSize returns the canonical byte size of a TxOutput.
func (out *TxOutput) SerializeSize() int {
	// value(8) + varint(pkscript len) + pkscript
	return 8 + VarIntSize(uint64(len(out.PkScript))) + len(out.PkScript)
}

// Serialize writes the transaction in canonical binary format to w.
func (tx *Transaction) Serialize(w io.Writer) error {
	var buf [8]byte

	binary.LittleEndian.PutUint32(buf[:4], tx.Version)
	if _, err := w.Write(buf[:4]); err != nil {
		return err
	}

	if err := WriteVarInt(w, uint64(len(tx.Inputs))); err != nil {
		return err
	}
	for i := range tx.Inputs {
		if err := tx.Inputs[i].Serialize(w); err != nil {
			return err
		}
	}

	if err := WriteVarInt(w, uint64(len(tx.Outputs))); err != nil {
		return err
	}
	for i := range tx.Outputs {
		if err := tx.Outputs[i].Serialize(w); err != nil {
			return err
		}
	}

	binary.LittleEndian.PutUint32(buf[:4], tx.LockTime)
	_, err := w.Write(buf[:4])
	return err
}

// Serialize writes a TxInput in canonical binary format.
func (in *TxInput) Serialize(w io.Writer) error {
	if _, err := w.Write(in.PreviousOutPoint.Hash[:]); err != nil {
		return err
	}
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], in.PreviousOutPoint.Index)
	if _, err := w.Write(buf[:]); err != nil {
		return err
	}
	if err := WriteVarInt(w, uint64(len(in.SignatureScript))); err != nil {
		return err
	}
	if _, err := w.Write(in.SignatureScript); err != nil {
		return err
	}
	binary.LittleEndian.PutUint32(buf[:], in.Sequence)
	_, err := w.Write(buf[:])
	return err
}

// Serialize writes a TxOutput in canonical binary format.
func (out *TxOutput) Serialize(w io.Writer) error {
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], out.Value)
	if _, err := w.Write(buf[:]); err != nil {
		return err
	}
	if err := WriteVarInt(w, uint64(len(out.PkScript))); err != nil {
		return err
	}
	_, err := w.Write(out.PkScript)
	return err
}

// SerializeToBytes returns the canonical byte representation of the transaction.
func (tx *Transaction) SerializeToBytes() ([]byte, error) {
	buf := make([]byte, 0, tx.SerializeSize())
	w := &sliceWriter{buf: buf}
	if err := tx.Serialize(w); err != nil {
		return nil, err
	}
	return w.buf, nil
}

// Deserialize reads a transaction from canonical binary format.
func (tx *Transaction) Deserialize(r io.Reader) error {
	var buf [8]byte

	if _, err := io.ReadFull(r, buf[:4]); err != nil {
		return fmt.Errorf("read tx version: %w", err)
	}
	tx.Version = binary.LittleEndian.Uint32(buf[:4])

	inCount, err := ReadVarInt(r)
	if err != nil {
		return fmt.Errorf("read input count: %w", err)
	}
	if inCount > MaxTxInputs {
		return fmt.Errorf("tx input count %d exceeds max %d", inCount, MaxTxInputs)
	}
	tx.Inputs = make([]TxInput, inCount)
	for i := uint64(0); i < inCount; i++ {
		if err := tx.Inputs[i].Deserialize(r); err != nil {
			return fmt.Errorf("read input %d: %w", i, err)
		}
	}

	outCount, err := ReadVarInt(r)
	if err != nil {
		return fmt.Errorf("read output count: %w", err)
	}
	if outCount > MaxTxOutputs {
		return fmt.Errorf("tx output count %d exceeds max %d", outCount, MaxTxOutputs)
	}
	tx.Outputs = make([]TxOutput, outCount)
	for i := uint64(0); i < outCount; i++ {
		if err := tx.Outputs[i].Deserialize(r); err != nil {
			return fmt.Errorf("read output %d: %w", i, err)
		}
	}

	if _, err := io.ReadFull(r, buf[:4]); err != nil {
		return fmt.Errorf("read locktime: %w", err)
	}
	tx.LockTime = binary.LittleEndian.Uint32(buf[:4])
	return nil
}

// Deserialize reads a TxInput from canonical binary format.
func (in *TxInput) Deserialize(r io.Reader) error {
	if _, err := io.ReadFull(r, in.PreviousOutPoint.Hash[:]); err != nil {
		return err
	}
	var buf [4]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return err
	}
	in.PreviousOutPoint.Index = binary.LittleEndian.Uint32(buf[:])

	scriptLen, err := ReadVarInt(r)
	if err != nil {
		return err
	}
	if scriptLen > MaxScriptSize {
		return fmt.Errorf("signature script size %d exceeds max %d", scriptLen, MaxScriptSize)
	}
	in.SignatureScript = make([]byte, scriptLen)
	if _, err := io.ReadFull(r, in.SignatureScript); err != nil {
		return err
	}

	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return err
	}
	in.Sequence = binary.LittleEndian.Uint32(buf[:])
	return nil
}

// Deserialize reads a TxOutput from canonical binary format.
func (out *TxOutput) Deserialize(r io.Reader) error {
	var buf [8]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return err
	}
	out.Value = binary.LittleEndian.Uint64(buf[:])

	scriptLen, err := ReadVarInt(r)
	if err != nil {
		return err
	}
	if scriptLen > MaxScriptSize {
		return fmt.Errorf("pk script size %d exceeds max %d", scriptLen, MaxScriptSize)
	}
	out.PkScript = make([]byte, scriptLen)
	_, err = io.ReadFull(r, out.PkScript)
	return err
}

// Limits to prevent resource exhaustion during deserialization.
const (
	MaxTxInputs   = 10000
	MaxTxOutputs  = 10000
	MaxScriptSize = 10000
)

// sliceWriter is a simple io.Writer backed by a growing byte slice.
type sliceWriter struct {
	buf []byte
}

func (sw *sliceWriter) Write(p []byte) (int, error) {
	sw.buf = append(sw.buf, p...)
	return len(p), nil
}
