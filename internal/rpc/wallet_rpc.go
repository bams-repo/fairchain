package rpc

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/bams-repo/fairchain/internal/crypto"
	"github.com/bams-repo/fairchain/internal/types"
	"github.com/bams-repo/fairchain/internal/utxo"
	"github.com/bams-repo/fairchain/internal/wallet"
)

// WalletInterface abstracts the wallet for RPC handlers.
type WalletInterface interface {
	GetNewAddress() (string, error)
	GetChangeAddress() (string, error)
	GetDefaultAddress() string
	DumpPrivKey(address string) (string, error)
	ImportPrivKey(privKeyStr string) (string, error)
	Mnemonic() string
	AllAddresses() []string
	ExternalAddresses() []string
	KeyCount() int
	AddressVersion() byte
	IsOurScript(pkScript []byte) bool
	KeyForScript(pkScript []byte) *wallet.DerivedKey
	GetKeyForAddress(address string) *wallet.DerivedKey
	BackupWallet(destPath string) error

	// Encryption (Bitcoin Core parity).
	IsEncrypted() bool
	IsLocked() bool
	EncryptWallet(passphrase string) error
	WalletPassphrase(passphrase string, timeoutSecs int64) error
	WalletLock() error
	RequireUnlocked() error

	FindUnspent(
		forEach func(fn func(txHash [32]byte, index uint32, value uint64, pkScript []byte, height uint32, isCoinbase bool)),
		tipHeight uint32,
	) []wallet.UnspentOutput

	GetBalance(
		forEach func(fn func(txHash [32]byte, index uint32, value uint64, pkScript []byte, height uint32, isCoinbase bool)),
		tipHeight uint32,
		minConf uint32,
		coinbaseMaturity uint32,
	) uint64

	BuildTransaction(
		req wallet.SendRequest,
		feePerByte uint64,
		utxos []wallet.UnspentOutput,
		coinbaseMaturity uint32,
		tipHeight uint32,
	) (*types.Transaction, error)

	SignRawTransaction(
		tx *types.Transaction,
		getPrevScript func(txHash [32]byte, index uint32) []byte,
	) (signed int, complete bool, err error)
}

// defaultFeePerByte is the initial fee rate for wallet transactions (1 sat/byte).
const defaultFeePerByte uint64 = 1

func (s *Server) requireWallet(w http.ResponseWriter) bool {
	if s.wallet == nil {
		writeError(w, http.StatusServiceUnavailable, "wallet not loaded")
		return false
	}
	return true
}

// requirePOST rejects non-POST requests for state-changing endpoints,
// preventing CSRF attacks via GET requests (e.g., <img> tag injection).
func requirePOST(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST required for state-changing operations")
		return false
	}
	return true
}

// --- Bitcoin Core parity: wallet RPCs ---

func (s *Server) handleGetNewAddress(w http.ResponseWriter, r *http.Request) {
	if !s.requireWallet(w) {
		return
	}
	addr, err := s.wallet.GetNewAddress()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, addr)
}

func (s *Server) handleGetRawChangeAddress(w http.ResponseWriter, r *http.Request) {
	if !s.requireWallet(w) {
		return
	}
	addr, err := s.wallet.GetChangeAddress()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, addr)
}

func (s *Server) handleGetBalance(w http.ResponseWriter, r *http.Request) {
	if !s.requireWallet(w) {
		return
	}
	minConfStr := r.URL.Query().Get("minconf")
	minConf := uint32(1)
	if minConfStr != "" {
		val, err := strconv.ParseUint(minConfStr, 10, 32)
		if err == nil {
			minConf = uint32(val)
		}
	}

	_, tipHeight := s.chain.Tip()
	balance := s.wallet.GetBalance(
		s.makeUtxoIterator(),
		tipHeight,
		minConf,
		s.params.CoinbaseMaturity,
	)

	writeJSON(w, map[string]interface{}{
		"balance":     balance,
		"balance_btc": float64(balance) / 1e8,
	})
}

func (s *Server) handleListUnspent(w http.ResponseWriter, r *http.Request) {
	if !s.requireWallet(w) {
		return
	}
	minConfStr := r.URL.Query().Get("minconf")
	maxConfStr := r.URL.Query().Get("maxconf")
	minConf := uint32(1)
	maxConf := uint32(9999999)
	if minConfStr != "" {
		val, _ := strconv.ParseUint(minConfStr, 10, 32)
		minConf = uint32(val)
	}
	if maxConfStr != "" {
		val, _ := strconv.ParseUint(maxConfStr, 10, 32)
		maxConf = uint32(val)
	}

	_, tipHeight := s.chain.Tip()
	utxos := s.wallet.FindUnspent(s.makeUtxoIterator(), tipHeight)

	var results []map[string]interface{}
	for _, u := range utxos {
		if u.Confirmations < minConf || u.Confirmations > maxConf {
			continue
		}
		// Skip immature coinbase.
		if u.IsCoinbase && u.Confirmations < s.params.CoinbaseMaturity {
			continue
		}
		txHashType := types.Hash(u.TxHash)
		results = append(results, map[string]interface{}{
			"txid":          txHashType.ReverseString(),
			"vout":          u.Index,
			"address":       u.Address,
			"scriptPubKey":  hex.EncodeToString(u.PkScript),
			"amount":        u.Value,
			"amount_btc":    float64(u.Value) / 1e8,
			"confirmations": u.Confirmations,
			"spendable":     true,
		})
	}

	if results == nil {
		results = make([]map[string]interface{}, 0)
	}
	writeJSON(w, results)
}

func (s *Server) handleSendToAddress(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) || !s.requireWallet(w) {
		return
	}
	if err := s.wallet.RequireUnlocked(); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	address := r.URL.Query().Get("address")
	amountStr := r.URL.Query().Get("amount")
	if address == "" || amountStr == "" {
		writeError(w, http.StatusBadRequest, "missing address or amount parameter")
		return
	}

	amount, err := strconv.ParseUint(amountStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid amount: %v", err))
		return
	}

	_, tipHeight := s.chain.Tip()
	utxos := s.wallet.FindUnspent(s.makeUtxoIterator(), tipHeight)

	tx, err := s.wallet.BuildTransaction(
		wallet.SendRequest{ToAddress: address, Amount: amount},
		s.feePerByte,
		utxos,
		s.params.CoinbaseMaturity,
		tipHeight,
	)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	txHash, err := s.submitTxToMempool(tx)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("mempool rejection: %v", err))
		return
	}

	writeJSON(w, txHash.ReverseString())
}

func (s *Server) handleSendRawTransaction(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	hexStr := r.URL.Query().Get("hexstring")
	if hexStr == "" {
		writeError(w, http.StatusBadRequest, "missing hexstring parameter")
		return
	}

	txBytes, err := hex.DecodeString(hexStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid hex: %v", err))
		return
	}

	var tx types.Transaction
	if err := tx.Deserialize(bytes.NewReader(txBytes)); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid transaction: %v", err))
		return
	}

	txHash, err := s.submitTxToMempool(&tx)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("rejected: %v", err))
		return
	}

	writeJSON(w, txHash.ReverseString())
}

func (s *Server) handleGetWalletInfo(w http.ResponseWriter, r *http.Request) {
	if !s.requireWallet(w) {
		return
	}
	_, tipHeight := s.chain.Tip()
	balance := s.wallet.GetBalance(
		s.makeUtxoIterator(),
		tipHeight,
		1,
		s.params.CoinbaseMaturity,
	)
	unconfirmed := s.wallet.GetBalance(
		s.makeUtxoIterator(),
		tipHeight,
		0,
		s.params.CoinbaseMaturity,
	)

	resp := map[string]interface{}{
		"walletname":           "default",
		"walletversion":        1,
		"balance":              balance,
		"balance_btc":          float64(balance) / 1e8,
		"unconfirmed_balance":  unconfirmed - balance,
		"txcount":              0,
		"keypoolsize":          s.wallet.KeyCount(),
		"paytxfee":             s.feePerByte,
		"hdseedid":             s.wallet.GetDefaultAddress(),
		"private_keys_enabled": true,
		"unlocked_until":       0,
	}
	if s.wallet.IsEncrypted() {
		if s.wallet.IsLocked() {
			resp["unlocked_until"] = 0
		} else {
			resp["unlocked_until"] = -1
		}
	}
	writeJSON(w, resp)
}

func (s *Server) handleDumpPrivKey(w http.ResponseWriter, r *http.Request) {
	if !s.requireWallet(w) {
		return
	}
	address := r.URL.Query().Get("address")
	if address == "" {
		writeError(w, http.StatusBadRequest, "missing address parameter")
		return
	}
	privKey, err := s.wallet.DumpPrivKey(address)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, privKey)
}

func (s *Server) handleImportPrivKey(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) || !s.requireWallet(w) {
		return
	}
	privKeyHex := r.URL.Query().Get("privkey")
	if privKeyHex == "" {
		writeError(w, http.StatusBadRequest, "missing privkey parameter")
		return
	}
	addr, err := s.wallet.ImportPrivKey(privKeyHex)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, map[string]interface{}{
		"address": addr,
	})
}

func (s *Server) handleListTransactions(w http.ResponseWriter, r *http.Request) {
	if !s.requireWallet(w) {
		return
	}
	countStr := r.URL.Query().Get("count")
	count := 10
	if countStr != "" {
		val, err := strconv.Atoi(countStr)
		if err == nil && val > 0 {
			count = val
		}
	}

	_, tipHeight := s.chain.Tip()
	utxos := s.wallet.FindUnspent(s.makeUtxoIterator(), tipHeight)

	var results []map[string]interface{}
	for _, u := range utxos {
		txHashType := types.Hash(u.TxHash)
		category := "receive"
		if u.IsCoinbase {
			if u.Confirmations >= s.params.CoinbaseMaturity {
				category = "generate"
			} else {
				category = "immature"
			}
		}
		results = append(results, map[string]interface{}{
			"address":       u.Address,
			"category":      category,
			"amount":        u.Value,
			"amount_btc":    float64(u.Value) / 1e8,
			"confirmations": u.Confirmations,
			"txid":          txHashType.ReverseString(),
			"vout":          u.Index,
			"blockheight":   u.Height,
		})
	}

	if len(results) > count {
		results = results[len(results)-count:]
	}
	if results == nil {
		results = make([]map[string]interface{}, 0)
	}
	writeJSON(w, results)
}

func (s *Server) handleSignRawTransactionWithWallet(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) || !s.requireWallet(w) {
		return
	}
	if err := s.wallet.RequireUnlocked(); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	hexStr := r.URL.Query().Get("hexstring")
	if hexStr == "" {
		writeError(w, http.StatusBadRequest, "missing hexstring parameter")
		return
	}

	txBytes, err := hex.DecodeString(hexStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid hex: %v", err))
		return
	}

	var tx types.Transaction
	if err := tx.Deserialize(bytes.NewReader(txBytes)); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid transaction: %v", err))
		return
	}

	getPrevScript := func(txHash [32]byte, index uint32) []byte {
		utxoSet := s.chain.UtxoSet()
		entry := utxoSet.Get(types.Hash(txHash), index)
		if entry != nil {
			return entry.PkScript
		}
		return nil
	}

	_, complete, err := s.wallet.SignRawTransaction(&tx, getPrevScript)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("signing failed: %v", err))
		return
	}

	var buf bytes.Buffer
	if err := tx.Serialize(&buf); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("serialize: %v", err))
		return
	}

	writeJSON(w, map[string]interface{}{
		"hex":      hex.EncodeToString(buf.Bytes()),
		"complete": complete,
	})
}

func (s *Server) handleGetReceivedByAddress(w http.ResponseWriter, r *http.Request) {
	address := r.URL.Query().Get("address")
	if address == "" {
		writeError(w, http.StatusBadRequest, "missing address parameter")
		return
	}

	_, destPKH, err := crypto.AddressToPubKeyHash(address)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid address: %v", err))
		return
	}
	destScript := crypto.MakeP2PKHScript(destPKH)

	minConfStr := r.URL.Query().Get("minconf")
	minConf := uint32(1)
	if minConfStr != "" {
		val, parseErr := strconv.ParseUint(minConfStr, 10, 32)
		if parseErr == nil {
			minConf = uint32(val)
		}
	}

	_, tipHeight := s.chain.Tip()
	var total uint64
	utxoSet := s.chain.UtxoSet()
	utxoSet.ForEach(func(txHash types.Hash, index uint32, entry *utxo.UtxoEntry) {
		if !bytes.Equal(entry.PkScript, destScript) {
			return
		}
		confs := uint32(0)
		if tipHeight >= entry.Height {
			confs = tipHeight - entry.Height + 1
		}
		if confs < minConf {
			return
		}
		total += entry.Value
	})

	writeJSON(w, map[string]interface{}{
		"amount":     total,
		"amount_btc": float64(total) / 1e8,
	})
}

func (s *Server) handleListAddressGroupings(w http.ResponseWriter, r *http.Request) {
	if !s.requireWallet(w) {
		return
	}
	_, tipHeight := s.chain.Tip()

	var grouping [][]interface{}
	for _, addr := range s.wallet.AllAddresses() {
		_, pkh, err := crypto.AddressToPubKeyHash(addr)
		if err != nil {
			continue
		}
		script := crypto.MakeP2PKHScript(pkh)
		var balance uint64
		utxoSet := s.chain.UtxoSet()
		utxoSet.ForEach(func(txHash types.Hash, index uint32, entry *utxo.UtxoEntry) {
			if !bytes.Equal(entry.PkScript, script) {
				return
			}
			confs := uint32(0)
			if tipHeight >= entry.Height {
				confs = tipHeight - entry.Height + 1
			}
			if confs >= 1 {
				balance += entry.Value
			}
		})
		grouping = append(grouping, []interface{}{addr, balance, float64(balance) / 1e8})
	}

	if grouping == nil {
		grouping = make([][]interface{}, 0)
	}
	writeJSON(w, []interface{}{grouping})
}

func (s *Server) handleBackupWallet(w http.ResponseWriter, r *http.Request) {
	if !s.requireWallet(w) {
		return
	}
	dest := r.URL.Query().Get("destination")
	if dest == "" {
		writeError(w, http.StatusBadRequest, "missing destination parameter")
		return
	}
	// Sanitize: reject path traversal and absolute paths to prevent arbitrary
	// file writes. Backups must be relative paths without ".." components.
	if filepath.IsAbs(dest) {
		writeError(w, http.StatusBadRequest, "absolute paths not allowed; use a relative path")
		return
	}
	cleaned := filepath.Clean(dest)
	if strings.HasPrefix(cleaned, "..") || strings.Contains(cleaned, string(filepath.Separator)+"..") {
		writeError(w, http.StatusBadRequest, "path traversal not allowed")
		return
	}
	if err := s.wallet.BackupWallet(cleaned); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("backup failed: %v", err))
		return
	}
	writeJSON(w, true)
}

func (s *Server) handleGetAddressesByLabel(w http.ResponseWriter, r *http.Request) {
	if !s.requireWallet(w) {
		return
	}
	result := make(map[string]interface{})
	for _, addr := range s.wallet.ExternalAddresses() {
		result[addr] = map[string]interface{}{
			"purpose": "receive",
		}
	}
	writeJSON(w, result)
}

func (s *Server) handleValidateAddress(w http.ResponseWriter, r *http.Request) {
	address := r.URL.Query().Get("address")
	if address == "" {
		writeError(w, http.StatusBadRequest, "missing address parameter")
		return
	}

	ver, pkh, err := crypto.AddressToPubKeyHash(address)
	if err != nil {
		writeJSON(w, map[string]interface{}{
			"isvalid": false,
			"address": address,
		})
		return
	}

	isMine := false
	if s.wallet != nil {
		dk := s.wallet.GetKeyForAddress(address)
		isMine = dk != nil
	}

	writeJSON(w, map[string]interface{}{
		"isvalid":      true,
		"address":      address,
		"scriptPubKey": hex.EncodeToString(crypto.MakeP2PKHScript(pkh)),
		"ismine":       isMine,
		"iswatchonly":  false,
		"isscript":     false,
		"version":      ver,
	})
}

// maxFeePerByte caps the fee rate to prevent accidental fund loss.
// 10,000 sat/byte is extremely high and should never be needed in practice.
const maxFeePerByte uint64 = 10_000

func (s *Server) handleSetTxFee(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	feeStr := r.URL.Query().Get("amount")
	if feeStr == "" {
		writeError(w, http.StatusBadRequest, "missing amount parameter")
		return
	}
	fee, err := strconv.ParseUint(feeStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid fee: %v", err))
		return
	}
	if fee > maxFeePerByte {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("fee rate %d exceeds maximum %d sat/byte", fee, maxFeePerByte))
		return
	}
	s.feePerByte = fee
	writeJSON(w, true)
}

func (s *Server) handleDumpWallet(w http.ResponseWriter, r *http.Request) {
	if !s.requireWallet(w) {
		return
	}
	if err := s.wallet.RequireUnlocked(); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	resp := map[string]interface{}{
		"mnemonic":    s.wallet.Mnemonic(),
		"addresses":   s.wallet.AllAddresses(),
		"keypoolsize": s.wallet.KeyCount(),
	}
	writeJSON(w, resp)
}

func (s *Server) handleGetTransaction(w http.ResponseWriter, r *http.Request) {
	txidStr := r.URL.Query().Get("txid")
	if txidStr == "" {
		writeError(w, http.StatusBadRequest, "missing txid parameter")
		return
	}
	txHash, err := types.HashFromReverseHex(txidStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid txid: %v", err))
		return
	}

	// Check mempool first.
	if entry, ok := s.mempool.GetTxEntry(txHash); ok {
		var txBuf bytes.Buffer
		if err := entry.Tx.Serialize(&txBuf); err == nil {
			writeJSON(w, map[string]interface{}{
				"txid":          txidStr,
				"confirmations": 0,
				"hex":           hex.EncodeToString(txBuf.Bytes()),
				"fee":           entry.Fee,
			})
			return
		}
	}

	// Without a full transaction index, we scan the UTXO set for outputs
	// matching this txid and report what we know.
	_, tipHeight := s.chain.Tip()
	utxoSet := s.chain.UtxoSet()
	var details []map[string]interface{}
	var totalValue uint64
	var blockHeight uint32
	found := false

	utxoSet.ForEach(func(hash types.Hash, index uint32, entry *utxo.UtxoEntry) {
		if hash != txHash {
			return
		}
		found = true
		blockHeight = entry.Height
		totalValue += entry.Value

		addr := ""
		hashBytes := crypto.ExtractP2PKHHash(entry.PkScript)
		if hashBytes != nil {
			var pkh [crypto.PubKeyHashSize]byte
			copy(pkh[:], hashBytes)
			if s.wallet != nil {
				addr = crypto.PubKeyHashToAddress(pkh, s.wallet.AddressVersion())
			}
		}

		details = append(details, map[string]interface{}{
			"address":  addr,
			"vout":     index,
			"amount":   entry.Value,
			"category": "receive",
		})
	})

	if !found {
		writeError(w, http.StatusNotFound, "transaction not found (no tx index available)")
		return
	}

	confs := uint32(0)
	if tipHeight >= blockHeight {
		confs = tipHeight - blockHeight + 1
	}

	writeJSON(w, map[string]interface{}{
		"txid":          txidStr,
		"confirmations": confs,
		"blockheight":   blockHeight,
		"amount":        totalValue,
		"details":       details,
	})
}

// --- Bitcoin Core parity: wallet encryption RPCs ---

func (s *Server) handleEncryptWallet(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) || !s.requireWallet(w) {
		return
	}
	passphrase := r.URL.Query().Get("passphrase")
	if passphrase == "" {
		writeError(w, http.StatusBadRequest, "missing passphrase parameter")
		return
	}
	if err := s.wallet.EncryptWallet(passphrase); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, "wallet encrypted successfully, wallet is now locked")
}

func (s *Server) handleWalletPassphrase(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) || !s.requireWallet(w) {
		return
	}
	passphrase := r.URL.Query().Get("passphrase")
	timeoutStr := r.URL.Query().Get("timeout")
	if passphrase == "" {
		writeError(w, http.StatusBadRequest, "missing passphrase parameter")
		return
	}
	timeout := int64(300) // default 5 minutes
	if timeoutStr != "" {
		val, err := strconv.ParseInt(timeoutStr, 10, 64)
		if err == nil && val > 0 {
			timeout = val
		}
	}
	if err := s.wallet.WalletPassphrase(passphrase, timeout); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	writeJSON(w, true)
}

func (s *Server) handleWalletLock(w http.ResponseWriter, r *http.Request) {
	if !s.requireWallet(w) {
		return
	}
	if err := s.wallet.WalletLock(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, true)
}

// --- Internal helpers ---

func (s *Server) makeUtxoIterator() func(fn func(txHash [32]byte, index uint32, value uint64, pkScript []byte, height uint32, isCoinbase bool)) {
	return func(fn func(txHash [32]byte, index uint32, value uint64, pkScript []byte, height uint32, isCoinbase bool)) {
		utxoSet := s.chain.UtxoSet()
		utxoSet.ForEach(func(txHash types.Hash, index uint32, entry *utxo.UtxoEntry) {
			fn(txHash, index, entry.Value, entry.PkScript, entry.Height, entry.IsCoinbase)
		})
	}
}

func (s *Server) submitTxToMempool(tx *types.Transaction) (types.Hash, error) {
	txHash, err := s.mempool.AddTx(tx)
	if err != nil {
		return types.ZeroHash, err
	}
	if s.broadcastTx != nil {
		s.broadcastTx(txHash)
	}
	return txHash, nil
}
