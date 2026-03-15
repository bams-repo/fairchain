package wallet

import (
	"fmt"
	"sort"

	"github.com/bams-repo/fairchain/internal/crypto"
	"github.com/bams-repo/fairchain/internal/types"
)

const (
	// Estimated transaction sizes for fee calculation (bytes).
	// P2PKH input: 148 bytes (outpoint=36 + script=~107 + sequence=4 + overhead=1)
	// P2PKH output: 34 bytes (value=8 + script=25 + overhead=1)
	// TX overhead: 10 bytes (version=4 + locktime=4 + vin count=1 + vout count=1)
	estimatedInputSize  = 148
	estimatedOutputSize = 34
	txOverhead          = 10

	// Dust threshold: outputs below this are considered dust and rejected.
	// Bitcoin Core uses 546 satoshis for P2PKH at 3 sat/byte relay fee.
	DustThreshold = 546
)

// SendRequest describes a payment to make.
type SendRequest struct {
	ToAddress string
	Amount    uint64
}

// BuildTransaction creates, signs, and returns a transaction that sends the
// specified amount to the destination address, with automatic coin selection
// and change output generation.
func (w *HDWallet) BuildTransaction(
	req SendRequest,
	feePerByte uint64,
	utxos []UnspentOutput,
	coinbaseMaturity uint32,
	tipHeight uint32,
) (*types.Transaction, error) {
	if err := w.RequireUnlocked(); err != nil {
		return nil, err
	}
	if req.Amount == 0 {
		return nil, fmt.Errorf("amount must be greater than zero")
	}

	// Decode and validate destination address version matches our network.
	addrVer, destPKH, err := crypto.AddressToPubKeyHash(req.ToAddress)
	if err != nil {
		return nil, fmt.Errorf("invalid destination address: %w", err)
	}
	if addrVer != w.addrVersion {
		return nil, fmt.Errorf("address version mismatch: address has 0x%02x, wallet expects 0x%02x (wrong network?)", addrVer, w.addrVersion)
	}
	destScript := crypto.MakeP2PKHScript(destPKH)

	// Filter UTXOs: only spendable (sufficient confirmations, mature coinbase).
	spendable := filterSpendable(utxos, coinbaseMaturity, tipHeight)
	if len(spendable) == 0 {
		return nil, fmt.Errorf("no spendable UTXOs available")
	}

	// Sort by value descending for largest-first coin selection.
	sort.Slice(spendable, func(i, j int) bool {
		return spendable[i].Value > spendable[j].Value
	})

	// Estimate fee for a 1-output (no change) transaction, then iterate.
	selectedInputs, totalIn, err := selectCoins(spendable, req.Amount, feePerByte)
	if err != nil {
		return nil, err
	}

	// Build the transaction. LockTime is set to the current tip height as an
	// anti-fee-sniping measure (matching Bitcoin Core behavior). This prevents
	// miners from re-mining old blocks to steal fees from this transaction.
	tx := &types.Transaction{
		Version:  1,
		LockTime: tipHeight,
	}

	// Add inputs.
	for _, utxo := range selectedInputs {
		tx.Inputs = append(tx.Inputs, types.TxInput{
			PreviousOutPoint: types.OutPoint{
				Hash:  utxo.TxHash,
				Index: utxo.Index,
			},
			Sequence: 0xFFFFFFFF,
		})
	}

	// Add destination output.
	tx.Outputs = append(tx.Outputs, types.TxOutput{
		Value:    req.Amount,
		PkScript: destScript,
	})

	// Calculate fee and change.
	estimatedSize := txOverhead + len(selectedInputs)*estimatedInputSize + 1*estimatedOutputSize
	fee := uint64(estimatedSize) * feePerByte
	if fee < 1 {
		fee = 1
	}

	remaining := totalIn - req.Amount
	if remaining < fee {
		return nil, fmt.Errorf("insufficient funds: need %d (amount) + %d (fee) = %d, have %d",
			req.Amount, fee, req.Amount+fee, totalIn)
	}

	change := remaining - fee

	// Add change output if above dust threshold. A fresh change address is
	// derived for each transaction to prevent address reuse (privacy).
	if change > DustThreshold {
		changeAddr, err := w.GetChangeAddress()
		if err != nil {
			return nil, fmt.Errorf("derive change address: %w", err)
		}
		_, changePKH, err := crypto.AddressToPubKeyHash(changeAddr)
		if err != nil {
			return nil, fmt.Errorf("decode change address: %w", err)
		}
		changeScript := crypto.MakeP2PKHScript(changePKH)
		tx.Outputs = append(tx.Outputs, types.TxOutput{
			Value:    change,
			PkScript: changeScript,
		})

		// Recalculate fee with the change output.
		estimatedSize = txOverhead + len(selectedInputs)*estimatedInputSize + len(tx.Outputs)*estimatedOutputSize
		fee = uint64(estimatedSize) * feePerByte
		if fee < 1 {
			fee = 1
		}
		newChange := totalIn - req.Amount - fee
		if newChange > DustThreshold {
			tx.Outputs[1].Value = newChange
		} else {
			// Change is dust after fee recalc — drop it and add to fee.
			tx.Outputs = tx.Outputs[:1]
		}
	}

	// Sign all inputs.
	for i, utxo := range selectedInputs {
		dk := w.KeyForScript(utxo.PkScript)
		if dk == nil {
			return nil, fmt.Errorf("no private key for input %d (address %s)", i, utxo.Address)
		}
		sigScript, err := crypto.SignInput(tx, i, utxo.PkScript, dk.PrivKey)
		if err != nil {
			return nil, fmt.Errorf("sign input %d: %w", i, err)
		}
		tx.Inputs[i].SignatureScript = sigScript
	}

	return tx, nil
}

// selectCoins implements a simple largest-first coin selection algorithm.
// Returns selected UTXOs, total input value, or error if insufficient funds.
func selectCoins(utxos []UnspentOutput, targetAmount uint64, feePerByte uint64) ([]UnspentOutput, uint64, error) {
	var selected []UnspentOutput
	var totalIn uint64

	for _, utxo := range utxos {
		selected = append(selected, utxo)
		totalIn += utxo.Value

		// Estimate fee with current number of inputs + 2 outputs (dest + change).
		estimatedSize := txOverhead + len(selected)*estimatedInputSize + 2*estimatedOutputSize
		fee := uint64(estimatedSize) * feePerByte
		if fee < 1 {
			fee = 1
		}

		if totalIn >= targetAmount+fee {
			return selected, totalIn, nil
		}
	}

	return nil, 0, fmt.Errorf("insufficient funds: need %d, available %d", targetAmount, totalIn)
}

func filterSpendable(utxos []UnspentOutput, coinbaseMaturity uint32, tipHeight uint32) []UnspentOutput {
	var result []UnspentOutput
	for _, u := range utxos {
		confs := uint32(0)
		if tipHeight >= u.Height {
			confs = tipHeight - u.Height + 1
		}
		// Must have at least 1 confirmation.
		if confs < 1 {
			continue
		}
		// Coinbase outputs require maturity.
		if u.IsCoinbase && confs < coinbaseMaturity {
			continue
		}
		result = append(result, u)
	}
	return result
}

// EstimateFee returns the estimated fee for a transaction with the given
// number of inputs and outputs at the specified fee rate.
func EstimateFee(numInputs, numOutputs int, feePerByte uint64) uint64 {
	size := txOverhead + numInputs*estimatedInputSize + numOutputs*estimatedOutputSize
	return uint64(size) * feePerByte
}
