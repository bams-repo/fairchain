package consensus

import (
	"fmt"
	"time"

	"github.com/bams-repo/fairchain/internal/crypto"
	"github.com/bams-repo/fairchain/internal/params"
	"github.com/bams-repo/fairchain/internal/types"
)

// ValidateBlockStructure performs consensus checks on block structure that are
// independent of the specific consensus engine:
//   - block must have at least one transaction
//   - first transaction must be coinbase
//   - no other transaction may be coinbase
//   - coinbase scriptSig encodes correct block height (BIP34)
//   - coinbase scriptSig size within [2, 100] bytes
//   - merkle root must match
//   - block size within limits
//   - no duplicate transaction IDs
//   - coinbase output value <= subsidy
func ValidateBlockStructure(block *types.Block, height uint32, p *params.ChainParams) error {
	if len(block.Transactions) == 0 {
		return fmt.Errorf("block has no transactions")
	}

	if !block.Transactions[0].IsCoinbase() {
		return fmt.Errorf("first transaction is not coinbase")
	}

	// BIP34: coinbase scriptSig must encode the block height as the first
	// serialized integer, and be between 2 and 100 bytes total. This
	// prevents duplicate coinbase TXIDs across different heights.
	if height > 0 {
		if err := validateCoinbaseScriptSig(block.Transactions[0].Inputs[0].SignatureScript, height); err != nil {
			return fmt.Errorf("coinbase scriptSig: %w", err)
		}
	}

	for i := 1; i < len(block.Transactions); i++ {
		if block.Transactions[i].IsCoinbase() {
			return fmt.Errorf("transaction %d is an unexpected coinbase", i)
		}
	}

	if uint32(len(block.Transactions)) > p.MaxBlockTxCount {
		return fmt.Errorf("block has %d transactions, max %d", len(block.Transactions), p.MaxBlockTxCount)
	}

	// Check for duplicate transaction IDs BEFORE merkle root validation.
	// This ordering is critical: CVE-2012-2459 exploits the merkle tree's
	// last-element duplication to create two different tx lists with the same
	// root. By rejecting duplicates first, we prevent an attacker from
	// poisoning the "invalid block" cache with a mutated block that shares
	// the same merkle root as a legitimate block.
	txIDs := make(map[types.Hash]struct{}, len(block.Transactions))
	for i := range block.Transactions {
		txID, err := crypto.HashTransaction(&block.Transactions[i])
		if err != nil {
			return fmt.Errorf("hash tx %d: %w", i, err)
		}
		if _, exists := txIDs[txID]; exists {
			return fmt.Errorf("duplicate transaction ID %s at index %d", txID, i)
		}
		txIDs[txID] = struct{}{}
	}

	// Verify merkle root.
	merkle, err := crypto.ComputeMerkleRoot(block.Transactions)
	if err != nil {
		return fmt.Errorf("compute merkle root: %w", err)
	}
	if merkle != block.Header.MerkleRoot {
		return fmt.Errorf("merkle root mismatch: header=%s computed=%s", block.Header.MerkleRoot, merkle)
	}

	// Check block serialized size.
	blockBytes, err := block.SerializeToBytes()
	if err != nil {
		return fmt.Errorf("serialize block for size check: %w", err)
	}
	if uint32(len(blockBytes)) > p.MaxBlockSize {
		return fmt.Errorf("block size %d exceeds max %d", len(blockBytes), p.MaxBlockSize)
	}

	// Coinbase value is validated in ValidateTransactionInputs where fees are known.
	// Structural validation only checks that coinbase outputs don't overflow.
	var coinbaseValue uint64
	for _, out := range block.Transactions[0].Outputs {
		if coinbaseValue+out.Value < coinbaseValue {
			return fmt.Errorf("coinbase output value overflow")
		}
		coinbaseValue += out.Value
	}

	return nil
}

// ValidateHeaderTimestamp checks the block timestamp against basic rules.
// For "prev+1": timestamp must be strictly greater than parent.
// For "median-11": timestamp must be greater than the median of the last 11 blocks.
// In both cases, timestamp must not be more than MaxTimeFutureDrift ahead of the provided "now".
func ValidateHeaderTimestamp(header *types.BlockHeader, parent *types.BlockHeader, nowUnix uint32, getAncestor func(height uint32) *types.BlockHeader, tipHeight uint32, p *params.ChainParams) error {
	maxFuture := nowUnix + uint32(p.MaxTimeFutureDrift/time.Second)
	if header.Timestamp > maxFuture {
		return fmt.Errorf("block timestamp %d too far in future (max %d)", header.Timestamp, maxFuture)
	}

	switch p.MinTimestampRule {
	case "prev+1":
		if header.Timestamp <= parent.Timestamp {
			return fmt.Errorf("block timestamp %d must be > parent %d", header.Timestamp, parent.Timestamp)
		}
	case "median-11":
		median := calcMedianTimePast(tipHeight, getAncestor)
		if header.Timestamp <= median {
			return fmt.Errorf("block timestamp %d must be > median time past %d", header.Timestamp, median)
		}
	default:
		return fmt.Errorf("unknown timestamp rule: %s", p.MinTimestampRule)
	}

	return nil
}

// validateCoinbaseScriptSig enforces BIP34: the coinbase scriptSig must begin
// with a serialized block height and be between 2 and 100 bytes total.
//
// The height is encoded as a CScript number: first byte is the number of bytes
// that follow, then the height in little-endian. For heights 0-16 Bitcoin uses
// OP_0..OP_16, but fairchain always uses the explicit push encoding for
// simplicity (matching the miner's buildCoinbase format).
func validateCoinbaseScriptSig(scriptSig []byte, height uint32) error {
	if len(scriptSig) < 2 {
		return fmt.Errorf("too short: %d bytes (minimum 2)", len(scriptSig))
	}
	if len(scriptSig) > 100 {
		return fmt.Errorf("too long: %d bytes (maximum 100)", len(scriptSig))
	}

	// The miner encodes height as a 4-byte LE push: [0x04][h0][h1][h2][h3].
	// We accept the minimal encoding: the push length byte tells us how many
	// bytes of height follow, and we decode them as LE uint32.
	pushLen := int(scriptSig[0])
	if pushLen < 1 || pushLen > 4 {
		return fmt.Errorf("height push length %d out of range [1,4]", pushLen)
	}
	if len(scriptSig) < 1+pushLen {
		return fmt.Errorf("scriptSig too short for height push (need %d, have %d)", 1+pushLen, len(scriptSig))
	}

	var encodedHeight uint32
	for i := 0; i < pushLen; i++ {
		encodedHeight |= uint32(scriptSig[1+i]) << (8 * uint(i))
	}

	if encodedHeight != height {
		return fmt.Errorf("encoded height %d does not match block height %d", encodedHeight, height)
	}

	return nil
}

// calcMedianTimePast computes the median of the last 11 block timestamps.
func calcMedianTimePast(tipHeight uint32, getAncestor func(height uint32) *types.BlockHeader) uint32 {
	const medianCount = 11
	timestamps := make([]uint32, 0, medianCount)

	for i := uint32(0); i < medianCount && tipHeight >= i; i++ {
		h := getAncestor(tipHeight - i)
		if h == nil {
			break
		}
		timestamps = append(timestamps, h.Timestamp)
	}

	if len(timestamps) == 0 {
		return 0
	}

	// Sort timestamps (insertion sort on small slice).
	for i := 1; i < len(timestamps); i++ {
		key := timestamps[i]
		j := i - 1
		for j >= 0 && timestamps[j] > key {
			timestamps[j+1] = timestamps[j]
			j--
		}
		timestamps[j+1] = key
	}

	return timestamps[len(timestamps)/2]
}
