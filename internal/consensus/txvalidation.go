package consensus

import (
	"fmt"

	"github.com/bams-repo/fairchain/internal/crypto"
	"github.com/bams-repo/fairchain/internal/params"
	"github.com/bams-repo/fairchain/internal/script"
	"github.com/bams-repo/fairchain/internal/types"
	"github.com/bams-repo/fairchain/internal/utxo"
)

// ValidateTransactionInputs checks that every non-coinbase transaction in a block
// has valid inputs against the UTXO set:
//   - each non-coinbase tx must have at least one input and one output
//   - each input references an existing, unspent output
//   - no duplicate inputs within a single transaction
//   - no duplicate spends across transactions within the block
//   - coinbase maturity is enforced
//   - no zero-value outputs
//   - input value accumulation checked for overflow
//   - total input value >= total output value (no value creation)
//   - coinbase value <= subsidy + total fees (with overflow protection)
//
// Returns the total fees collected by all non-coinbase transactions.
func ValidateTransactionInputs(block *types.Block, utxoSet *utxo.Set, height uint32, p *params.ChainParams) (uint64, error) {
	var totalFees uint64

	// Track outpoints spent within this block to detect intra-block double spends.
	spentInBlock := make(map[[36]byte]struct{})

	for txIdx := range block.Transactions {
		tx := &block.Transactions[txIdx]

		if tx.IsCoinbase() {
			for outIdx, out := range tx.Outputs {
				if out.Value == 0 {
					return 0, fmt.Errorf("coinbase output %d: zero value not allowed", outIdx)
				}
			}
			continue
		}

		if len(tx.Inputs) == 0 {
			return 0, fmt.Errorf("tx %d: non-coinbase transaction has no inputs", txIdx)
		}
		if len(tx.Outputs) == 0 {
			return 0, fmt.Errorf("tx %d: transaction has no outputs", txIdx)
		}

		txHash, err := crypto.HashTransaction(tx)
		if err != nil {
			return 0, fmt.Errorf("hash tx %d: %w", txIdx, err)
		}

		// Detect duplicate inputs within this single transaction.
		seenInputs := make(map[[36]byte]struct{}, len(tx.Inputs))

		var totalIn uint64
		for inIdx, in := range tx.Inputs {
			opKey := utxo.OutpointKey(in.PreviousOutPoint.Hash, in.PreviousOutPoint.Index)

			if _, dup := seenInputs[opKey]; dup {
				return 0, fmt.Errorf("tx %s input %d: duplicate input within transaction (outpoint %s:%d)",
					txHash, inIdx, in.PreviousOutPoint.Hash, in.PreviousOutPoint.Index)
			}
			seenInputs[opKey] = struct{}{}

			if _, alreadySpent := spentInBlock[opKey]; alreadySpent {
				return 0, fmt.Errorf("tx %s input %d: double-spend within block (outpoint %s:%d)",
					txHash, inIdx, in.PreviousOutPoint.Hash, in.PreviousOutPoint.Index)
			}

			entry := utxoSet.Get(in.PreviousOutPoint.Hash, in.PreviousOutPoint.Index)
			if entry == nil {
				return 0, fmt.Errorf("tx %s input %d: references missing UTXO %s:%d",
					txHash, inIdx, in.PreviousOutPoint.Hash, in.PreviousOutPoint.Index)
			}

			if entry.IsCoinbase {
				if height < entry.Height {
					return 0, fmt.Errorf("tx %s input %d: coinbase at height %d cannot be spent at height %d",
						txHash, inIdx, entry.Height, height)
				}
				maturityDepth := height - entry.Height
				if maturityDepth < p.CoinbaseMaturity {
					return 0, fmt.Errorf("tx %s input %d: coinbase output at height %d not mature (need %d confirmations, have %d)",
						txHash, inIdx, entry.Height, p.CoinbaseMaturity, maturityDepth)
				}
			}

			if totalIn+entry.Value < totalIn {
				return 0, fmt.Errorf("tx %s: input value overflow at input %d", txHash, inIdx)
			}
			totalIn += entry.Value
			spentInBlock[opKey] = struct{}{}
		}

		var totalOut uint64
		for outIdx, out := range tx.Outputs {
			if out.Value == 0 {
				return 0, fmt.Errorf("tx %s output %d: zero value not allowed", txHash, outIdx)
			}
			if out.Value > params.MaxMoneyValue {
				return 0, fmt.Errorf("tx %s output %d: value %d exceeds max money %d", txHash, outIdx, out.Value, params.MaxMoneyValue)
			}
			if totalOut+out.Value < totalOut {
				return 0, fmt.Errorf("tx %s output %d: value overflow", txHash, outIdx)
			}
			totalOut += out.Value
		}

		if totalIn < totalOut {
			return 0, fmt.Errorf("tx %s: input value %d < output value %d", txHash, totalIn, totalOut)
		}

	// Script validation: verify each input's SignatureScript satisfies the
	// referenced UTXO's PkScript. This is the spend authorization check.
	for inIdx, in := range tx.Inputs {
		entry := utxoSet.Get(in.PreviousOutPoint.Hash, in.PreviousOutPoint.Index)
		if entry == nil {
			continue // already validated above
		}
		if script.IsLegacyUnvalidatedScript(entry.PkScript) {
			continue
		}
		if err := script.Verify(in.SignatureScript, entry.PkScript, tx, inIdx); err != nil {
			return 0, fmt.Errorf("tx %s input %d: script validation failed: %w", txHash, inIdx, err)
		}
	}

	fee := totalIn - totalOut
		if totalFees+fee < totalFees {
			return 0, fmt.Errorf("total fees overflow at tx %d", txIdx)
		}
		totalFees += fee
	}

	subsidy := p.CalcSubsidy(height)
	maxCoinbase := subsidy + totalFees
	if maxCoinbase < subsidy {
		return 0, fmt.Errorf("subsidy + fees overflow (subsidy=%d, fees=%d)", subsidy, totalFees)
	}
	var coinbaseValue uint64
	for outIdx, out := range block.Transactions[0].Outputs {
		if coinbaseValue+out.Value < coinbaseValue {
			return 0, fmt.Errorf("coinbase output %d: value accumulation overflow", outIdx)
		}
		coinbaseValue += out.Value
	}
	if coinbaseValue > maxCoinbase {
		return 0, fmt.Errorf("coinbase value %d exceeds subsidy+fees %d (subsidy=%d, fees=%d)",
			coinbaseValue, maxCoinbase, subsidy, totalFees)
	}

	return totalFees, nil
}

// ValidateSingleTransaction checks a single non-coinbase transaction against the UTXO set.
// Used for mempool admission. Returns the fee if valid.
func ValidateSingleTransaction(tx *types.Transaction, utxoSet *utxo.Set, tipHeight uint32, p *params.ChainParams) (uint64, error) {
	if tx.IsCoinbase() {
		return 0, fmt.Errorf("coinbase transactions cannot be validated individually")
	}

	if len(tx.Inputs) == 0 {
		return 0, fmt.Errorf("transaction has no inputs")
	}
	if len(tx.Outputs) == 0 {
		return 0, fmt.Errorf("transaction has no outputs")
	}

	txHash, err := crypto.HashTransaction(tx)
	if err != nil {
		return 0, fmt.Errorf("hash transaction: %w", err)
	}

	// Detect duplicate inputs within this transaction.
	seenInputs := make(map[[36]byte]struct{}, len(tx.Inputs))

	spendHeight := tipHeight + 1

	var totalIn uint64
	for inIdx, in := range tx.Inputs {
		opKey := utxo.OutpointKey(in.PreviousOutPoint.Hash, in.PreviousOutPoint.Index)
		if _, dup := seenInputs[opKey]; dup {
			return 0, fmt.Errorf("tx %s input %d: duplicate input (outpoint %s:%d)",
				txHash, inIdx, in.PreviousOutPoint.Hash, in.PreviousOutPoint.Index)
		}
		seenInputs[opKey] = struct{}{}

		entry := utxoSet.Get(in.PreviousOutPoint.Hash, in.PreviousOutPoint.Index)
		if entry == nil {
			return 0, fmt.Errorf("tx %s input %d: references missing UTXO %s:%d",
				txHash, inIdx, in.PreviousOutPoint.Hash, in.PreviousOutPoint.Index)
		}

		if entry.IsCoinbase {
			if spendHeight < entry.Height {
				return 0, fmt.Errorf("tx %s input %d: coinbase at height %d cannot be spent at height %d",
					txHash, inIdx, entry.Height, spendHeight)
			}
			maturityDepth := spendHeight - entry.Height
			if maturityDepth < p.CoinbaseMaturity {
				return 0, fmt.Errorf("tx %s input %d: coinbase output at height %d not mature (need %d, have %d)",
					txHash, inIdx, entry.Height, p.CoinbaseMaturity, maturityDepth)
			}
		}

		if totalIn+entry.Value < totalIn {
			return 0, fmt.Errorf("tx %s: input value overflow at input %d", txHash, inIdx)
		}
		totalIn += entry.Value
	}

	var totalOut uint64
	for outIdx, out := range tx.Outputs {
		if out.Value == 0 {
			return 0, fmt.Errorf("tx %s output %d: zero value not allowed", txHash, outIdx)
		}
		if out.Value > params.MaxMoneyValue {
			return 0, fmt.Errorf("tx %s output %d: value %d exceeds max money %d", txHash, outIdx, out.Value, params.MaxMoneyValue)
		}
		if totalOut+out.Value < totalOut {
			return 0, fmt.Errorf("tx %s output %d: value overflow", txHash, outIdx)
		}
		totalOut += out.Value
	}

	if totalIn < totalOut {
		return 0, fmt.Errorf("tx %s: input value %d < output value %d", txHash, totalIn, totalOut)
	}

	// Script validation for mempool admission.
	for inIdx, in := range tx.Inputs {
		entry := utxoSet.Get(in.PreviousOutPoint.Hash, in.PreviousOutPoint.Index)
		if entry == nil {
			continue
		}
		if script.IsLegacyUnvalidatedScript(entry.PkScript) {
			continue
		}
		if err := script.Verify(in.SignatureScript, entry.PkScript, tx, inIdx); err != nil {
			return 0, fmt.Errorf("tx %s input %d: script validation failed: %w", txHash, inIdx, err)
		}
	}

	fee := totalIn - totalOut
	return fee, nil
}

// CalcTxFee computes the fee for a transaction given the UTXO set.
// Returns 0 for coinbase transactions or on overflow.
func CalcTxFee(tx *types.Transaction, utxoSet *utxo.Set) uint64 {
	if tx.IsCoinbase() {
		return 0
	}
	var totalIn uint64
	for _, in := range tx.Inputs {
		entry := utxoSet.Get(in.PreviousOutPoint.Hash, in.PreviousOutPoint.Index)
		if entry != nil {
			prev := totalIn
			totalIn += entry.Value
			if totalIn < prev {
				return 0
			}
		}
	}
	var totalOut uint64
	for _, out := range tx.Outputs {
		prev := totalOut
		totalOut += out.Value
		if totalOut < prev {
			return 0
		}
	}
	if totalIn <= totalOut {
		return 0
	}
	return totalIn - totalOut
}
