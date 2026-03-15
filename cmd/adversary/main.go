package main

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/bams-repo/fairchain/internal/crypto"
	"github.com/bams-repo/fairchain/internal/types"
)

func main() {
	attack := flag.String("attack", "", "Attack type: bad-nonce, bad-merkle, duplicate, time-warp-future, time-warp-past, orphan-flood, inflated-coinbase, empty-block, wrong-bits, double-spend, immature-coinbase-spend, overspend, duplicate-input, intra-block-double-spend, steal-utxo, steal-premine, difficulty-manipulation")
	rpc := flag.String("rpc", "http://127.0.0.1:31000", "Target node RPC address")
	count := flag.Int("count", 1, "Number of attack payloads to send (for flood attacks)")
	flag.Parse()

	if *attack == "" {
		fmt.Fprintln(os.Stderr, "Usage: fairchain-adversary -attack <type> -rpc <addr>")
		flag.PrintDefaults()
		os.Exit(1)
	}

	var results []attackResult
	var err error

	switch *attack {
	case "bad-nonce":
		results, err = attackBadNonce(*rpc)
	case "bad-merkle":
		results, err = attackBadMerkle(*rpc)
	case "duplicate":
		results, err = attackDuplicate(*rpc)
	case "time-warp-future":
		results, err = attackTimeWarp(*rpc, true)
	case "time-warp-past":
		results, err = attackTimeWarp(*rpc, false)
	case "orphan-flood":
		results, err = attackOrphanFlood(*rpc, *count)
	case "inflated-coinbase":
		results, err = attackInflatedCoinbase(*rpc)
	case "empty-block":
		results, err = attackEmptyBlock(*rpc)
	case "wrong-bits":
		results, err = attackWrongBits(*rpc)
	case "double-spend":
		results, err = attackDoubleSpend(*rpc)
	case "immature-coinbase-spend":
		results, err = attackImmatureCoinbaseSpend(*rpc)
	case "overspend":
		results, err = attackOverspend(*rpc)
	case "duplicate-input":
		results, err = attackDuplicateInput(*rpc)
	case "intra-block-double-spend":
		results, err = attackIntraBlockDoubleSpend(*rpc)
	case "steal-utxo":
		results, err = attackStealUTXO(*rpc)
	case "steal-premine":
		results, err = attackStealPremine(*rpc)
	case "difficulty-manipulation":
		results, err = attackDifficultyManipulation(*rpc, *count)
	default:
		fmt.Fprintf(os.Stderr, "Unknown attack: %s\n", *attack)
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "attack setup error: %v\n", err)
		os.Exit(2)
	}

	out, _ := json.Marshal(results)
	fmt.Println(string(out))
}

type attackResult struct {
	Attack   string `json:"attack"`
	Rejected bool   `json:"rejected"`
	Error    string `json:"error,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

func fetchBlockByHeight(rpc string, height int) (*blockInfo, error) {
	resp, err := http.Get(fmt.Sprintf("%s/getblockbyheight?height=%d", rpc, height))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var info blockInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, err
	}
	return &info, nil
}

type blockInfo struct {
	Hash      string `json:"hash"`
	Height    int    `json:"height"`
	Version   uint32 `json:"version"`
	PrevBlock string `json:"previousblockhash"`
	Merkle    string `json:"merkleroot"`
	Timestamp uint32 `json:"time"`
	Bits      string `json:"bits"`
	Nonce     uint32 `json:"nonce"`
	TxCount   int    `json:"nTx"`
}

type chainInfo struct {
	Height   int    `json:"blocks"`
	BestHash string `json:"bestblockhash"`
	Bits     string `json:"bits"`
	Chain    string `json:"chain"`
}

type chainStatus struct {
	Bits             string `json:"bits"`
	RetargetInterval uint32 `json:"retarget_interval"`
}

func fetchChainInfo(rpc string) (*chainInfo, error) {
	resp, err := http.Get(fmt.Sprintf("%s/getblockchaininfo", rpc))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var info chainInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, err
	}
	return &info, nil
}

func fetchChainStatus(rpc string) (*chainStatus, error) {
	resp, err := http.Get(fmt.Sprintf("%s/getchainstatus", rpc))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var status chainStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return nil, err
	}
	return &status, nil
}

type fullChainInfo struct {
	Height           int
	BestHash         string
	Bits             string
	Chain            string
	RetargetInterval uint32
}

func fetchFullChainInfo(rpc string) (*fullChainInfo, error) {
	ci, err := fetchChainInfo(rpc)
	if err != nil {
		return nil, fmt.Errorf("fetch chain info: %w", err)
	}
	cs, err := fetchChainStatus(rpc)
	if err != nil {
		return nil, fmt.Errorf("fetch chain status: %w", err)
	}
	bits := ci.Bits
	if bits == "" {
		bits = cs.Bits
	}
	return &fullChainInfo{
		Height:           ci.Height,
		BestHash:         ci.BestHash,
		Bits:             bits,
		Chain:            ci.Chain,
		RetargetInterval: cs.RetargetInterval,
	}, nil
}

func submitBlock(rpc string, block *types.Block) (bool, string) {
	data, err := block.SerializeToBytes()
	if err != nil {
		return false, fmt.Sprintf("serialize error: %v", err)
	}

	resp, err := http.Post(rpc+"/submitblock", "application/octet-stream", bytes.NewReader(data))
	if err != nil {
		return false, fmt.Sprintf("http error: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return true, string(body)
	}
	return false, string(body)
}

func makeCoinbaseTx(height uint32, value uint64) types.Transaction {
	heightBytes := make([]byte, 4)
	binary.LittleEndian.PutUint32(heightBytes, height)
	return types.Transaction{
		Version: 1,
		Inputs: []types.TxInput{{
			PreviousOutPoint: types.CoinbaseOutPoint,
			SignatureScript:  append(append([]byte{0x04}, heightBytes...), []byte("fairchain")...),
			Sequence:         0xFFFFFFFF,
		}},
		Outputs: []types.TxOutput{{
			Value:    value,
			PkScript: []byte("adversary-test"),
		}},
		LockTime: 0,
	}
}

func buildBlockOnTip(rpc string) (*types.Block, uint32, error) {
	ci, err := fetchFullChainInfo(rpc)
	if err != nil {
		return nil, 0, err
	}

	prevHash, err := types.HashFromReverseHex(ci.BestHash)
	if err != nil {
		return nil, 0, fmt.Errorf("parse best hash: %w", err)
	}

	var bits uint32
	fmt.Sscanf(ci.Bits, "%x", &bits)
	newHeight := uint32(ci.Height) + 1

	cb := makeCoinbaseTx(newHeight, 5000000000)

	block := &types.Block{
		Header: types.BlockHeader{
			Version:   1,
			PrevBlock: prevHash,
			Timestamp: uint32(time.Now().Unix()),
			Bits:      bits,
			Nonce:     0,
		},
		Transactions: []types.Transaction{cb},
	}

	merkle, err := crypto.ComputeMerkleRoot(block.Transactions)
	if err != nil {
		return nil, 0, err
	}
	block.Header.MerkleRoot = merkle

	return block, newHeight, nil
}

// Attack 1: Valid block structure but nonce doesn't satisfy PoW
func attackBadNonce(rpc string) ([]attackResult, error) {
	block, _, err := buildBlockOnTip(rpc)
	if err != nil {
		return nil, err
	}

	block.Header.Nonce = 0xDEADBEEF

	rejected, detail := submitBlock(rpc, block)
	return []attackResult{{
		Attack:   "bad-nonce",
		Rejected: rejected,
		Detail:   detail,
	}}, nil
}

// Attack 2: Valid block but merkle root is corrupted
func attackBadMerkle(rpc string) ([]attackResult, error) {
	block, _, err := buildBlockOnTip(rpc)
	if err != nil {
		return nil, err
	}

	block.Header.MerkleRoot[0] ^= 0xFF
	block.Header.MerkleRoot[15] ^= 0xAA

	rejected, detail := submitBlock(rpc, block)
	return []attackResult{{
		Attack:   "bad-merkle",
		Rejected: rejected,
		Detail:   detail,
	}}, nil
}

// Attack 3: Resubmit block at height 1 (already accepted)
func attackDuplicate(rpc string) ([]attackResult, error) {
	ci, err := fetchChainInfo(rpc)
	if err != nil {
		return nil, err
	}
	if ci.Height < 1 {
		return []attackResult{{Attack: "duplicate", Rejected: false, Detail: "chain too short"}}, nil
	}

	info, err := fetchBlockByHeight(rpc, 1)
	if err != nil {
		return nil, err
	}

	prevHash, _ := types.HashFromReverseHex(info.PrevBlock)
	merkle, _ := types.HashFromReverseHex(info.Merkle)
	var bits uint32
	fmt.Sscanf(info.Bits, "%x", &bits)

	block := &types.Block{
		Header: types.BlockHeader{
			Version:    info.Version,
			PrevBlock:  prevHash,
			MerkleRoot: merkle,
			Timestamp:  info.Timestamp,
			Bits:       bits,
			Nonce:      info.Nonce,
		},
		Transactions: []types.Transaction{makeCoinbaseTx(1, 5000000000)},
	}

	rejected, detail := submitBlock(rpc, block)
	return []attackResult{{
		Attack:   "duplicate",
		Rejected: rejected,
		Detail:   detail,
	}}, nil
}

// Attack 4: Block with timestamp far in the future or before parent
func attackTimeWarp(rpc string, future bool) ([]attackResult, error) {
	block, _, err := buildBlockOnTip(rpc)
	if err != nil {
		return nil, err
	}

	if future {
		block.Header.Timestamp = uint32(time.Now().Unix()) + 7200 + 1 // >2h ahead
	} else {
		block.Header.Timestamp = 1 // way before any parent
	}

	merkle, _ := crypto.ComputeMerkleRoot(block.Transactions)
	block.Header.MerkleRoot = merkle

	rejected, detail := submitBlock(rpc, block)

	label := "time-warp-future"
	if !future {
		label = "time-warp-past"
	}
	return []attackResult{{
		Attack:   label,
		Rejected: rejected,
		Detail:   detail,
	}}, nil
}

// Attack 5: Flood with blocks referencing random nonexistent parents
func attackOrphanFlood(rpc string, count int) ([]attackResult, error) {
	var results []attackResult
	for i := 0; i < count; i++ {
		var fakeParent types.Hash
		rand.Read(fakeParent[:])

		cb := makeCoinbaseTx(uint32(i+99999), 5000000000)
		block := &types.Block{
			Header: types.BlockHeader{
				Version:   1,
				PrevBlock: fakeParent,
				Timestamp: uint32(time.Now().Unix()),
				Bits:      0x1e0fffff,
				Nonce:     uint32(i),
			},
			Transactions: []types.Transaction{cb},
		}
		merkle, _ := crypto.ComputeMerkleRoot(block.Transactions)
		block.Header.MerkleRoot = merkle

		rejected, detail := submitBlock(rpc, block)
		results = append(results, attackResult{
			Attack:   "orphan-flood",
			Rejected: rejected,
			Detail:   detail,
		})
	}
	return results, nil
}

// Attack 6: Block with inflated coinbase (more than allowed subsidy)
func attackInflatedCoinbase(rpc string) ([]attackResult, error) {
	block, newHeight, err := buildBlockOnTip(rpc)
	if err != nil {
		return nil, err
	}

	block.Transactions[0].Outputs[0].Value = 999999999999999

	merkle, _ := crypto.ComputeMerkleRoot(block.Transactions)
	block.Header.MerkleRoot = merkle

	target := crypto.CompactToHash(block.Header.Bits)
	found, _ := (&powSealer{}).seal(&block.Header, target, 200000000)
	detail := ""
	if !found {
		detail = "could not find PoW (expected for high-diff); submitting anyway"
	}

	rejected, submitDetail := submitBlock(rpc, block)
	return []attackResult{{
		Attack:   "inflated-coinbase",
		Rejected: rejected,
		Detail:   detail + " | " + submitDetail,
		Error:    fmt.Sprintf("attempted %d sats at height %d", block.Transactions[0].Outputs[0].Value, newHeight),
	}}, nil
}

// Attack 7: Block with zero transactions (no coinbase)
func attackEmptyBlock(rpc string) ([]attackResult, error) {
	ci, err := fetchFullChainInfo(rpc)
	if err != nil {
		return nil, err
	}

	prevHash, _ := types.HashFromReverseHex(ci.BestHash)
	var bits uint32
	fmt.Sscanf(ci.Bits, "%x", &bits)

	block := &types.Block{
		Header: types.BlockHeader{
			Version:   1,
			PrevBlock: prevHash,
			Timestamp: uint32(time.Now().Unix()),
			Bits:      bits,
			Nonce:     0,
		},
		Transactions: []types.Transaction{},
	}

	rejected, detail := submitBlock(rpc, block)
	return []attackResult{{
		Attack:   "empty-block",
		Rejected: rejected,
		Detail:   detail,
	}}, nil
}

// Attack 8: Block with artificially easy difficulty bits (should be rejected by bits validation)
func attackWrongBits(rpc string) ([]attackResult, error) {
	ci, err := fetchFullChainInfo(rpc)
	if err != nil {
		return nil, err
	}

	prevHash, err := types.HashFromReverseHex(ci.BestHash)
	if err != nil {
		return nil, err
	}

	easyBits := uint32(0x207fffff)
	newHeight := uint32(ci.Height) + 1
	cb := makeCoinbaseTx(newHeight, 5000000000)

	block := &types.Block{
		Header: types.BlockHeader{
			Version:   1,
			PrevBlock: prevHash,
			Timestamp: uint32(time.Now().Unix()),
			Bits:      easyBits,
			Nonce:     0,
		},
		Transactions: []types.Transaction{cb},
	}

	merkle, _ := crypto.ComputeMerkleRoot(block.Transactions)
	block.Header.MerkleRoot = merkle

	target := crypto.CompactToHash(easyBits)
	found, _ := (&powSealer{}).seal(&block.Header, target, 10000000)
	detail := ""
	if !found {
		detail = "could not find PoW with easy bits; submitting anyway"
	}

	rejected, submitDetail := submitBlock(rpc, block)
	return []attackResult{{
		Attack:   "wrong-bits",
		Rejected: rejected,
		Detail:   detail + " | " + submitDetail,
		Error:    fmt.Sprintf("used bits=0x%08x at height %d", easyBits, newHeight),
	}}, nil
}

// buildBlockWithSpendTx constructs a block on the current tip that includes
// a coinbase and a spend transaction. The spend tx references the given outpoint
// and sends outputValue to a dummy script. The block is mined with valid PoW.
func buildBlockWithSpendTx(rpc string, spendTxHash types.Hash, spendIndex uint32, outputValue uint64, tag string) (*types.Block, uint32, error) {
	ci, err := fetchFullChainInfo(rpc)
	if err != nil {
		return nil, 0, err
	}

	prevHash, err := types.HashFromReverseHex(ci.BestHash)
	if err != nil {
		return nil, 0, fmt.Errorf("parse best hash: %w", err)
	}

	var bits uint32
	fmt.Sscanf(ci.Bits, "%x", &bits)
	newHeight := uint32(ci.Height) + 1

	cb := makeCoinbaseTx(newHeight, 5000000000)
	cb.Inputs[0].SignatureScript = append(cb.Inputs[0].SignatureScript, []byte(tag)...)

	spendTx := types.Transaction{
		Version: 1,
		Inputs: []types.TxInput{{
			PreviousOutPoint: types.OutPoint{Hash: spendTxHash, Index: spendIndex},
			SignatureScript:  []byte("adversary-spend"),
			Sequence:         0xFFFFFFFF,
		}},
		Outputs: []types.TxOutput{{
			Value:    outputValue,
			PkScript: []byte("adversary-recipient"),
		}},
		LockTime: 0,
	}

	block := &types.Block{
		Header: types.BlockHeader{
			Version:   1,
			PrevBlock: prevHash,
			Timestamp: uint32(time.Now().Unix()),
			Bits:      bits,
			Nonce:     0,
		},
		Transactions: []types.Transaction{cb, spendTx},
	}

	merkle, err := crypto.ComputeMerkleRoot(block.Transactions)
	if err != nil {
		return nil, 0, err
	}
	block.Header.MerkleRoot = merkle

	target := crypto.CompactToHash(bits)
	found, _ := (&powSealer{}).seal(&block.Header, target, 200000000)
	if !found {
		return block, newHeight, fmt.Errorf("could not find PoW within iteration limit")
	}

	return block, newHeight, nil
}

type txoutInfo struct {
	Confirmations int    `json:"confirmations"`
	Value         uint64 `json:"value"`
	Coinbase      bool   `json:"coinbase"`
}

func fetchTxOut(rpc string, txid string, n int) (*txoutInfo, error) {
	resp, err := http.Get(fmt.Sprintf("%s/gettxout?txid=%s&n=%d", rpc, txid, n))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("gettxout returned %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) == "null\n" || string(body) == "null" {
		return nil, fmt.Errorf("UTXO not found (already spent)")
	}
	var info txoutInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return nil, fmt.Errorf("parse gettxout: %w (body: %s)", err, string(body))
	}
	return &info, nil
}

// findSpendableUTXO searches backwards from the tip for a coinbase UTXO that
// is mature enough to spend. Returns the tx hash (display order), output index, and value.
func findSpendableUTXO(rpc string, mustBeMature bool) (string, uint32, uint64, error) {
	ci, err := fetchChainInfo(rpc)
	if err != nil {
		return "", 0, 0, err
	}

	// Search backwards from an old-enough height to find a spendable coinbase.
	// Testnet maturity is 10, so anything at height <= (tip - 10) should be mature.
	startHeight := ci.Height
	if mustBeMature {
		startHeight = ci.Height - 15
	}
	if startHeight < 1 {
		startHeight = 1
	}

	for h := startHeight; h >= 1; h-- {
		if _, err := fetchBlockByHeight(rpc, h); err != nil {
			continue
		}

		heightBytes := make([]byte, 4)
		binary.LittleEndian.PutUint32(heightBytes, uint32(h))

		// Try the standard miner coinbase format: BIP34 (0x04 + height) + suffix
		for _, suffix := range []string{"fairchain", "test", ""} {
			cb := types.Transaction{
				Version: 1,
				Inputs: []types.TxInput{{
					PreviousOutPoint: types.CoinbaseOutPoint,
					SignatureScript:  append(append([]byte{0x04}, heightBytes...), []byte(suffix)...),
					Sequence:         0xFFFFFFFF,
				}},
				Outputs: []types.TxOutput{{
					Value:    5000000000,
					PkScript: []byte{0x00},
				}},
				LockTime: 0,
			}

			txHash, err := crypto.HashTransaction(&cb)
			if err != nil {
				continue
			}
			txHashDisplay := txHash.ReverseString()

			utxoInfo, err := fetchTxOut(rpc, txHashDisplay, 0)
			if err != nil {
				continue
			}

			if utxoInfo != nil && utxoInfo.Value > 0 {
				return txHashDisplay, 0, utxoInfo.Value, nil
			}
		}
	}

	return "", 0, 0, fmt.Errorf("no spendable UTXO found")
}

// findImmatureUTXO finds a coinbase UTXO that is NOT yet mature (recent block).
func findImmatureUTXO(rpc string) (string, uint32, uint64, error) {
	ci, err := fetchChainInfo(rpc)
	if err != nil {
		return "", 0, 0, err
	}

	// Look at only the most recent 2 blocks to ensure coinbase is still immature
	// by the time we build and submit the attack block.
	for h := ci.Height; h >= 1 && h > ci.Height-2; h-- {
		heightBytes := make([]byte, 4)
		binary.LittleEndian.PutUint32(heightBytes, uint32(h))

		for _, suffix := range []string{"fairchain", "test", ""} {
			cb := types.Transaction{
				Version: 1,
				Inputs: []types.TxInput{{
					PreviousOutPoint: types.CoinbaseOutPoint,
					SignatureScript:  append(append([]byte{0x04}, heightBytes...), []byte(suffix)...),
					Sequence:         0xFFFFFFFF,
				}},
				Outputs: []types.TxOutput{{
					Value:    5000000000,
					PkScript: []byte{0x00},
				}},
				LockTime: 0,
			}

			txHash, err := crypto.HashTransaction(&cb)
			if err != nil {
				continue
			}
			txHashDisplay := txHash.ReverseString()

			utxoInfo, err := fetchTxOut(rpc, txHashDisplay, 0)
			if err != nil {
				continue
			}

			if utxoInfo != nil && utxoInfo.Coinbase && utxoInfo.Confirmations < 10 {
				return txHashDisplay, 0, utxoInfo.Value, nil
			}
		}
	}

	return "", 0, 0, fmt.Errorf("no immature coinbase UTXO found")
}

// Attack 9: Double-spend — submit a block that references a non-existent UTXO,
// simulating an attempt to spend an already-consumed output.
func attackDoubleSpend(rpc string) ([]attackResult, error) {
	// Use a fabricated outpoint that definitely doesn't exist in the UTXO set.
	// This simulates spending an output that was already consumed.
	var fakeTxHash types.Hash
	rand.Read(fakeTxHash[:])
	block, newHeight, buildErr := buildBlockWithSpendTx(rpc, fakeTxHash, 0, 1000, "double-spend")
	if buildErr != nil {
		return []attackResult{{
			Attack:   "double-spend",
			Rejected: true,
			Detail:   fmt.Sprintf("could not build attack block: %v", buildErr),
		}}, nil
	}
	rejected, detail := submitBlock(rpc, block)
	return []attackResult{{
		Attack:   "double-spend",
		Rejected: rejected,
		Detail:   fmt.Sprintf("fabricated outpoint at height %d | %s", newHeight, detail),
	}}, nil
}

// Attack 10: Spend a coinbase output that hasn't reached maturity.
func attackImmatureCoinbaseSpend(rpc string) ([]attackResult, error) {
	txidDisplay, idx, value, err := findImmatureUTXO(rpc)
	if err != nil {
		// Fallback: use the tip block's coinbase (guaranteed immature if chain is moving).
		ci, ciErr := fetchChainInfo(rpc)
		if ciErr != nil {
			return nil, ciErr
		}
		heightBytes := make([]byte, 4)
		binary.LittleEndian.PutUint32(heightBytes, uint32(ci.Height))
		cb := types.Transaction{
			Version: 1,
			Inputs: []types.TxInput{{
				PreviousOutPoint: types.CoinbaseOutPoint,
				SignatureScript:  append(append([]byte{0x04}, heightBytes...), []byte("fairchain")...),
				Sequence:         0xFFFFFFFF,
			}},
			Outputs: []types.TxOutput{{
				Value:    5000000000,
				PkScript: []byte{0x00},
			}},
			LockTime: 0,
		}
		txHash, _ := crypto.HashTransaction(&cb)
		txidDisplay = txHash.ReverseString()
		idx = 0
		value = 5000000000
	}

	txHash, _ := types.HashFromReverseHex(txidDisplay)

	block, newHeight, err := buildBlockWithSpendTx(rpc, txHash, idx, value-1000, "immature-spend")
	if err != nil {
		if block != nil {
			rejected, detail := submitBlock(rpc, block)
			return []attackResult{{
				Attack:   "immature-coinbase-spend",
				Rejected: rejected,
				Detail:   fmt.Sprintf("no PoW, height %d | %s", newHeight, detail),
			}}, nil
		}
		return nil, fmt.Errorf("build immature spend block: %w", err)
	}

	rejected, detail := submitBlock(rpc, block)
	return []attackResult{{
		Attack:   "immature-coinbase-spend",
		Rejected: rejected,
		Detail:   fmt.Sprintf("spent immature coinbase %s:%d at height %d | %s", txidDisplay[:16], idx, newHeight, detail),
	}}, nil
}

// Attack 11: Overspend — output value exceeds input value (creating coins from nothing).
func attackOverspend(rpc string) ([]attackResult, error) {
	txidDisplay, idx, value, err := findSpendableUTXO(rpc, true)
	if err != nil {
		// Fallback: fabricate a UTXO reference with an absurd output value.
		var fakeTxHash types.Hash
		rand.Read(fakeTxHash[:])
		block, newHeight, buildErr := buildBlockWithSpendTx(rpc, fakeTxHash, 0, 999999999999, "overspend-fake")
		if buildErr != nil && block != nil {
			rejected, detail := submitBlock(rpc, block)
			return []attackResult{{
				Attack:   "overspend",
				Rejected: rejected,
				Detail:   fmt.Sprintf("fabricated outpoint at height %d | %s", newHeight, detail),
			}}, nil
		}
		return []attackResult{{
			Attack:   "overspend",
			Rejected: true,
			Detail:   fmt.Sprintf("no spendable UTXO to craft attack: utxo=%v build=%v", err, buildErr),
		}}, nil
	}

	txHash, _ := types.HashFromReverseHex(txidDisplay)

	// Spend with output value = input value * 10 (massive overspend).
	overspendValue := value * 10
	block, newHeight, err := buildBlockWithSpendTx(rpc, txHash, idx, overspendValue, "overspend")
	if err != nil {
		if block != nil {
			rejected, detail := submitBlock(rpc, block)
			return []attackResult{{
				Attack:   "overspend",
				Rejected: rejected,
				Detail:   fmt.Sprintf("no PoW, input=%d output=%d at height %d | %s", value, overspendValue, newHeight, detail),
			}}, nil
		}
		return nil, fmt.Errorf("build overspend block: %w", err)
	}

	rejected, detail := submitBlock(rpc, block)
	return []attackResult{{
		Attack:   "overspend",
		Rejected: rejected,
		Detail:   fmt.Sprintf("input=%d output=%d at height %d | %s", value, overspendValue, newHeight, detail),
	}}, nil
}

// Attack 12: Duplicate-input — a single transaction references the same outpoint twice,
// attempting to count its value twice and create coins.
func attackDuplicateInput(rpc string) ([]attackResult, error) {
	txidDisplay, idx, value, err := findSpendableUTXO(rpc, true)
	if err != nil {
		return []attackResult{{
			Attack:   "duplicate-input",
			Rejected: true,
			Detail:   fmt.Sprintf("no spendable UTXO to craft attack: %v", err),
		}}, nil
	}

	txHash, _ := types.HashFromReverseHex(txidDisplay)

	ci, err := fetchFullChainInfo(rpc)
	if err != nil {
		return nil, err
	}

	prevHash, _ := types.HashFromReverseHex(ci.BestHash)
	var bits uint32
	fmt.Sscanf(ci.Bits, "%x", &bits)
	newHeight := uint32(ci.Height) + 1

	cb := makeCoinbaseTx(newHeight, 5000000000)
	cb.Inputs[0].SignatureScript = append(cb.Inputs[0].SignatureScript, []byte("dup-input")...)

	// Craft a tx with the same input listed twice — attempts to double-count the value.
	dupInputTx := types.Transaction{
		Version: 1,
		Inputs: []types.TxInput{
			{PreviousOutPoint: types.OutPoint{Hash: txHash, Index: idx}, SignatureScript: []byte("dup-1"), Sequence: 0xFFFFFFFF},
			{PreviousOutPoint: types.OutPoint{Hash: txHash, Index: idx}, SignatureScript: []byte("dup-2"), Sequence: 0xFFFFFFFF},
		},
		Outputs: []types.TxOutput{{
			Value:    value * 2,
			PkScript: []byte("adversary-dup-input"),
		}},
		LockTime: 0,
	}

	block := &types.Block{
		Header: types.BlockHeader{
			Version:   1,
			PrevBlock: prevHash,
			Timestamp: uint32(time.Now().Unix()),
			Bits:      bits,
			Nonce:     0,
		},
		Transactions: []types.Transaction{cb, dupInputTx},
	}

	merkle, _ := crypto.ComputeMerkleRoot(block.Transactions)
	block.Header.MerkleRoot = merkle

	target := crypto.CompactToHash(bits)
	found, _ := (&powSealer{}).seal(&block.Header, target, 200000000)
	if !found {
		if block != nil {
			rejected, detail := submitBlock(rpc, block)
			return []attackResult{{
				Attack:   "duplicate-input",
				Rejected: rejected,
				Detail:   fmt.Sprintf("no PoW, value=%d at height %d | %s", value, newHeight, detail),
			}}, nil
		}
		return nil, fmt.Errorf("could not find PoW")
	}

	rejected, detail := submitBlock(rpc, block)
	return []attackResult{{
		Attack:   "duplicate-input",
		Rejected: rejected,
		Detail:   fmt.Sprintf("input=%s:%d value=%d claimed=%d at height %d | %s", txidDisplay[:16], idx, value, value*2, newHeight, detail),
	}}, nil
}

// Attack 13: Intra-block double-spend — two separate transactions within the same block
// both spend the same outpoint.
func attackIntraBlockDoubleSpend(rpc string) ([]attackResult, error) {
	txidDisplay, idx, value, err := findSpendableUTXO(rpc, true)
	if err != nil {
		return []attackResult{{
			Attack:   "intra-block-double-spend",
			Rejected: true,
			Detail:   fmt.Sprintf("no spendable UTXO to craft attack: %v", err),
		}}, nil
	}

	txHash, _ := types.HashFromReverseHex(txidDisplay)

	ci, err := fetchFullChainInfo(rpc)
	if err != nil {
		return nil, err
	}

	prevHash, _ := types.HashFromReverseHex(ci.BestHash)
	var bits uint32
	fmt.Sscanf(ci.Bits, "%x", &bits)
	newHeight := uint32(ci.Height) + 1

	cb := makeCoinbaseTx(newHeight, 5000000000)
	cb.Inputs[0].SignatureScript = append(cb.Inputs[0].SignatureScript, []byte("intra-ds")...)

	spendTx1 := types.Transaction{
		Version: 1,
		Inputs: []types.TxInput{{
			PreviousOutPoint: types.OutPoint{Hash: txHash, Index: idx},
			SignatureScript:  []byte("spend-1"),
			Sequence:         0xFFFFFFFF,
		}},
		Outputs: []types.TxOutput{{
			Value:    value - 1000,
			PkScript: []byte("adversary-intra-1"),
		}},
		LockTime: 0,
	}

	spendTx2 := types.Transaction{
		Version: 1,
		Inputs: []types.TxInput{{
			PreviousOutPoint: types.OutPoint{Hash: txHash, Index: idx},
			SignatureScript:  []byte("spend-2"),
			Sequence:         0xFFFFFFFF,
		}},
		Outputs: []types.TxOutput{{
			Value:    value - 2000,
			PkScript: []byte("adversary-intra-2"),
		}},
		LockTime: 0,
	}

	block := &types.Block{
		Header: types.BlockHeader{
			Version:   1,
			PrevBlock: prevHash,
			Timestamp: uint32(time.Now().Unix()),
			Bits:      bits,
			Nonce:     0,
		},
		Transactions: []types.Transaction{cb, spendTx1, spendTx2},
	}

	merkle, _ := crypto.ComputeMerkleRoot(block.Transactions)
	block.Header.MerkleRoot = merkle

	target := crypto.CompactToHash(bits)
	found, _ := (&powSealer{}).seal(&block.Header, target, 200000000)
	if !found {
		if block != nil {
			rejected, detail := submitBlock(rpc, block)
			return []attackResult{{
				Attack:   "intra-block-double-spend",
				Rejected: rejected,
				Detail:   fmt.Sprintf("no PoW, value=%d at height %d | %s", value, newHeight, detail),
			}}, nil
		}
		return nil, fmt.Errorf("could not find PoW")
	}

	rejected, detail := submitBlock(rpc, block)
	return []attackResult{{
		Attack:   "intra-block-double-spend",
		Rejected: rejected,
		Detail:   fmt.Sprintf("input=%s:%d value=%d at height %d | %s", txidDisplay[:16], idx, value, newHeight, detail),
	}}, nil
}

type powSealer struct{}

func (p *powSealer) seal(header *types.BlockHeader, target types.Hash, maxIter uint64) (bool, error) {
	for i := uint64(0); i < maxIter; i++ {
		hash := crypto.HashBlockHeader(header)
		if hash.LessOrEqual(target) {
			return true, nil
		}
		header.Nonce++
	}
	return false, nil
}

// fetchSubsidy estimates the current per-block subsidy from the chain info.
// Testnet: 50_000_000, Mainnet: 5_000_000_000, Regtest: 5_000_000_000.
func fetchSubsidy(rpc string) uint64 {
	ci, err := fetchChainInfo(rpc)
	if err != nil {
		return 50_000_000
	}
	switch ci.Chain {
	case "mainnet":
		return 5_000_000_000
	case "testnet":
		return 50_000_000
	default:
		return 5_000_000_000
	}
}

// findMinerCoinbaseUTXO searches for a spendable miner coinbase UTXO by
// reconstructing the exact coinbase transaction the miner produces (height LE
// bytes + "fairchain", PkScript=0x00, subsidy from chain).
func findMinerCoinbaseUTXO(rpc string, mature bool) (types.Hash, uint32, uint64, string, error) {
	ci, err := fetchChainInfo(rpc)
	if err != nil {
		return types.Hash{}, 0, 0, "", err
	}

	subsidy := fetchSubsidy(rpc)

	startHeight := ci.Height
	if mature {
		startHeight = ci.Height - 15
	}
	if startHeight < 1 {
		startHeight = 1
	}

	for h := startHeight; h >= 1; h-- {
		heightBytes := make([]byte, 4)
		binary.LittleEndian.PutUint32(heightBytes, uint32(h))
		msg := append(append([]byte{0x04}, heightBytes...), []byte("fairchain")...)

		cb := types.Transaction{
			Version: 1,
			Inputs: []types.TxInput{{
				PreviousOutPoint: types.CoinbaseOutPoint,
				SignatureScript:  msg,
				Sequence:         0xFFFFFFFF,
			}},
			Outputs: []types.TxOutput{{
				Value:    subsidy,
				PkScript: []byte{0x00},
			}},
			LockTime: 0,
		}

		txHash, err := crypto.HashTransaction(&cb)
		if err != nil {
			continue
		}
		txidDisplay := txHash.ReverseString()

		utxoInfo, err := fetchTxOut(rpc, txidDisplay, 0)
		if err != nil || utxoInfo == nil {
			continue
		}

		if utxoInfo.Value > 0 {
			if mature && utxoInfo.Confirmations < 10 {
				continue
			}
			return txHash, 0, utxoInfo.Value, txidDisplay, nil
		}
	}

	return types.Hash{}, 0, 0, "", fmt.Errorf("no miner coinbase UTXO found")
}

// Attack 14: Steal any UTXO — exploits the lack of signature/script validation.
// Finds a mature coinbase UTXO belonging to a miner and spends it with a
// fabricated SignatureScript. If accepted, this proves spend authorization is broken.
func attackStealUTXO(rpc string) ([]attackResult, error) {
	subsidy := fetchSubsidy(rpc)
	var results []attackResult

	// Retry loop: race the honest miner. We build on the latest tip and try to
	// submit before the next honest block arrives.
	for attempt := 0; attempt < 20; attempt++ {
		txHash, idx, value, txidDisplay, err := findMinerCoinbaseUTXO(rpc, true)
		if err != nil {
			results = append(results, attackResult{
				Attack: "steal-utxo",
				Detail: fmt.Sprintf("attempt %d: no spendable UTXO: %v", attempt, err),
			})
			time.Sleep(500 * time.Millisecond)
			continue
		}

		ci, err := fetchFullChainInfo(rpc)
		if err != nil {
			continue
		}

		prevHash, _ := types.HashFromReverseHex(ci.BestHash)
		var bits uint32
		fmt.Sscanf(ci.Bits, "%x", &bits)
		newHeight := uint32(ci.Height) + 1

		fee := uint64(100)
		cb := makeCoinbaseTx(newHeight, subsidy+fee)
		cb.Inputs[0].SignatureScript = append(cb.Inputs[0].SignatureScript,
			[]byte(fmt.Sprintf("steal-utxo-%d", attempt))...)

		theftTx := types.Transaction{
			Version: 1,
			Inputs: []types.TxInput{{
				PreviousOutPoint: types.OutPoint{Hash: txHash, Index: idx},
				SignatureScript:  []byte("STOLEN-no-signature-required"),
				Sequence:         0xFFFFFFFF,
			}},
			Outputs: []types.TxOutput{{
				Value:    value - fee,
				PkScript: []byte("attacker-wallet-addr"),
			}},
			LockTime: 0,
		}

		block := &types.Block{
			Header: types.BlockHeader{
				Version:   1,
				PrevBlock: prevHash,
				Timestamp: uint32(time.Now().Unix()),
				Bits:      bits,
				Nonce:     0,
			},
			Transactions: []types.Transaction{cb, theftTx},
		}

		merkle, _ := crypto.ComputeMerkleRoot(block.Transactions)
		block.Header.MerkleRoot = merkle

		target := crypto.CompactToHash(bits)
		found, _ := (&powSealer{}).seal(&block.Header, target, 500000000)
		if !found {
			results = append(results, attackResult{
				Attack: "steal-utxo",
				Detail: fmt.Sprintf("attempt %d: PoW exhausted", attempt),
			})
			continue
		}

		wasRejected, detail := submitBlock(rpc, block)
		if !wasRejected {
			results = append(results, attackResult{
				Attack:   "steal-utxo",
				Rejected: false,
				Detail:   fmt.Sprintf("CONSENSUS BROKEN (attempt %d) | stole %d sats from %s:%d at height %d | %s", attempt, value-fee, txidDisplay[:16], idx, newHeight, detail),
			})
			return results, nil
		}

		results = append(results, attackResult{
			Attack:   "steal-utxo",
			Rejected: true,
			Detail:   fmt.Sprintf("attempt %d: %s", attempt, detail),
		})

		time.Sleep(200 * time.Millisecond)
	}

	return results, nil
}

// Attack 15: Steal the testnet premine burn output.
// The testnet genesis has a second output worth ~420 trillion sats locked to
// "burn:testnet:premine:v1". Without script validation, we can spend it.
func attackStealPremine(rpc string) ([]attackResult, error) {
	subsidy := fetchSubsidy(rpc)
	var results []attackResult

	genesisCb := types.Transaction{
		Version: 1,
		Inputs: []types.TxInput{{
			PreviousOutPoint: types.CoinbaseOutPoint,
			SignatureScript:  []byte("fairchain testnet genesis"),
			Sequence:         0xFFFFFFFF,
		}},
		Outputs: []types.TxOutput{
			{
				Value:    50_0000_00,
				PkScript: []byte{0x00},
			},
			{
				Value:    419_999_999_538_000,
				PkScript: []byte("burn:testnet:premine:v1"),
			},
		},
		LockTime: 0,
	}

	genesisTxHash, err := crypto.HashTransaction(&genesisCb)
	if err != nil {
		return nil, fmt.Errorf("hash genesis coinbase: %w", err)
	}

	txidDisplay := genesisTxHash.ReverseString()

	for attempt := 0; attempt < 20; attempt++ {
		utxoInfo, err := fetchTxOut(rpc, txidDisplay, 1)
		premineValue := uint64(419_999_999_538_000)
		outputIdx := uint32(1)
		if err != nil || utxoInfo == nil {
			utxoInfo, err = fetchTxOut(rpc, txidDisplay, 0)
			if err != nil || utxoInfo == nil {
				return append(results, attackResult{
					Attack: "steal-premine",
					Detail: fmt.Sprintf("premine UTXO not found (already spent?): txid=%s", txidDisplay[:16]),
				}), nil
			}
			premineValue = utxoInfo.Value
			outputIdx = 0
		}

		ci, err := fetchFullChainInfo(rpc)
		if err != nil {
			continue
		}

		prevHash, _ := types.HashFromReverseHex(ci.BestHash)
		var bits uint32
		fmt.Sscanf(ci.Bits, "%x", &bits)
		newHeight := uint32(ci.Height) + 1

		fee := uint64(1000)
		cb := makeCoinbaseTx(newHeight, subsidy+fee)
		cb.Inputs[0].SignatureScript = append(cb.Inputs[0].SignatureScript,
			[]byte(fmt.Sprintf("steal-premine-%d", attempt))...)

		stealTx := types.Transaction{
			Version: 1,
			Inputs: []types.TxInput{{
				PreviousOutPoint: types.OutPoint{Hash: genesisTxHash, Index: outputIdx},
				SignatureScript:  []byte("PREMINE-THEFT-no-script-validation"),
				Sequence:         0xFFFFFFFF,
			}},
			Outputs: []types.TxOutput{
				{
					Value:    premineValue / 2,
					PkScript: []byte("attacker-wallet-1"),
				},
				{
					Value:    premineValue/2 - fee,
					PkScript: []byte("attacker-wallet-2"),
				},
			},
			LockTime: 0,
		}

		block := &types.Block{
			Header: types.BlockHeader{
				Version:   1,
				PrevBlock: prevHash,
				Timestamp: uint32(time.Now().Unix()),
				Bits:      bits,
				Nonce:     0,
			},
			Transactions: []types.Transaction{cb, stealTx},
		}

		merkle, _ := crypto.ComputeMerkleRoot(block.Transactions)
		block.Header.MerkleRoot = merkle

		target := crypto.CompactToHash(bits)
		found, _ := (&powSealer{}).seal(&block.Header, target, 500000000)
		if !found {
			results = append(results, attackResult{
				Attack: "steal-premine",
				Detail: fmt.Sprintf("attempt %d: PoW exhausted", attempt),
			})
			continue
		}

		wasRejected, detail := submitBlock(rpc, block)
		if !wasRejected {
			results = append(results, attackResult{
				Attack:   "steal-premine",
				Rejected: false,
				Detail:   fmt.Sprintf("CONSENSUS BROKEN - PREMINE STOLEN (attempt %d) | stole %d sats at height %d | %s", attempt, premineValue, newHeight, detail),
			})
			return results, nil
		}

		results = append(results, attackResult{
			Attack:   "steal-premine",
			Rejected: true,
			Detail:   fmt.Sprintf("attempt %d: %s", attempt, detail),
		})

		time.Sleep(200 * time.Millisecond)
	}

	return results, nil
}

// Attack 16: Difficulty manipulation via timestamp gaming.
// Mines a sequence of blocks with timestamps compressed near the start of a
// retarget window, then stretched at the end, to maximize the difficulty drop
// at the next retarget. On testnet (retarget every 20 blocks, 5s target),
// this can reduce difficulty by 4x per epoch.
func attackDifficultyManipulation(rpc string, epochs int) ([]attackResult, error) {
	if epochs < 1 {
		epochs = 1
	}

	subsidy := fetchSubsidy(rpc)
	var results []attackResult

	for epoch := 0; epoch < epochs; epoch++ {
		ci, err := fetchFullChainInfo(rpc)
		if err != nil {
			return results, err
		}

		currentHeight := uint32(ci.Height)
		retargetInterval := ci.RetargetInterval
		if retargetInterval == 0 {
			retargetInterval = 20
		}

		// Calculate how many blocks until the next retarget boundary.
		nextRetarget := ((currentHeight / retargetInterval) + 1) * retargetInterval
		blocksNeeded := nextRetarget - currentHeight

		if blocksNeeded == 0 {
			blocksNeeded = retargetInterval
			nextRetarget += retargetInterval
		}

		detail := fmt.Sprintf("epoch %d: height=%d, next_retarget=%d, blocks_needed=%d",
			epoch, currentHeight, nextRetarget, blocksNeeded)

		prevHash, _ := types.HashFromReverseHex(ci.BestHash)
		var bits uint32
		fmt.Sscanf(ci.Bits, "%x", &bits)
		origBits := bits

		tipBlock, err := fetchBlockByHeight(rpc, int(currentHeight))
		if err != nil {
			results = append(results, attackResult{
				Attack: "difficulty-manipulation",
				Detail: fmt.Sprintf("%s | failed to fetch tip: %v", detail, err),
			})
			continue
		}
		lastTimestamp := tipBlock.Timestamp

		accepted := 0
		rejected := 0

		for i := uint32(1); i <= blocksNeeded; i++ {
			newHeight := currentHeight + i

			// Timestamp strategy: stretch timestamps to make actualTimespan = 4x target.
			// targetTimespan = 100s, so we want actualTimespan >= 400s.
			// Spread across 20 blocks: each block timestamp += 20s (vs 5s target).
			blockTimestamp := lastTimestamp + (i * 20)

			now := uint32(time.Now().Unix())
			maxAllowed := now + 120
			if blockTimestamp > maxAllowed {
				blockTimestamp = maxAllowed
			}
			if blockTimestamp <= lastTimestamp {
				blockTimestamp = lastTimestamp + 1
			}

			cb := makeCoinbaseTx(newHeight, subsidy)
			cb.Inputs[0].SignatureScript = append(cb.Inputs[0].SignatureScript,
				[]byte(fmt.Sprintf("diff-manip-e%d-b%d", epoch, i))...)

			block := &types.Block{
				Header: types.BlockHeader{
					Version:   1,
					PrevBlock: prevHash,
					Timestamp: blockTimestamp,
					Bits:      bits,
					Nonce:     0,
				},
				Transactions: []types.Transaction{cb},
			}

			merkle, _ := crypto.ComputeMerkleRoot(block.Transactions)
			block.Header.MerkleRoot = merkle

			target := crypto.CompactToHash(bits)
			found, _ := (&powSealer{}).seal(&block.Header, target, 500000000)
			if !found {
				results = append(results, attackResult{
					Attack: "difficulty-manipulation",
					Detail: fmt.Sprintf("%s | PoW exhausted at block %d/%d (bits=0x%08x)", detail, i, blocksNeeded, bits),
				})
				break
			}

			wasRejected, submitDetail := submitBlock(rpc, block)
			if wasRejected {
				rejected++
				results = append(results, attackResult{
					Attack:   "difficulty-manipulation",
					Rejected: true,
					Detail:   fmt.Sprintf("block %d/%d rejected: %s", i, blocksNeeded, submitDetail),
				})
				break
			}

			accepted++
			prevHash = crypto.HashBlockHeader(&block.Header)

			if newHeight%retargetInterval == 0 {
				time.Sleep(100 * time.Millisecond)
				newCs, csErr := fetchChainStatus(rpc)
				if csErr == nil {
					fmt.Sscanf(newCs.Bits, "%x", &bits)
				}
			}
		}

		time.Sleep(200 * time.Millisecond)
		newCs, _ := fetchChainStatus(rpc)
		newBitsStr := "unknown"
		if newCs != nil {
			newBitsStr = newCs.Bits
		}

		results = append(results, attackResult{
			Attack: "difficulty-manipulation",
			Detail: fmt.Sprintf("%s | accepted=%d rejected=%d | new_bits=%s (was 0x%08x)",
				detail, accepted, rejected, newBitsStr, origBits),
		})
	}

	return results, nil
}
