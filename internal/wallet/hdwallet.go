package wallet

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/bams-repo/fairchain/internal/crypto"
	"github.com/bams-repo/fairchain/internal/types"
	"github.com/btcsuite/btcd/btcutil/hdkeychain"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/tyler-smith/go-bip39"
	"golang.org/x/crypto/scrypt"
)

// BIP44 derivation path: m/44'/0'/0'
// External chain: m/44'/0'/0'/0/i
// Change chain:   m/44'/0'/0'/1/i
const (
	bip44Purpose  = 44
	bip44CoinType = 0 // Bitcoin-compatible
)

// WalletData is the on-disk JSON representation of the wallet.
type WalletData struct {
	Mnemonic        string `json:"mnemonic"`
	Seed            string `json:"seed"`
	NextExternalIdx uint32 `json:"next_external_idx"`
	NextChangeIdx   uint32 `json:"next_change_idx"`

	// Encryption fields (populated when wallet is encrypted).
	Encrypted      bool   `json:"encrypted,omitempty"`
	EncryptedData  string `json:"encrypted_data,omitempty"`
	Salt           string `json:"salt,omitempty"`
	IV             string `json:"iv,omitempty"`
	ScryptN        int    `json:"scrypt_n,omitempty"`
	ScryptR        int    `json:"scrypt_r,omitempty"`
	ScryptP        int    `json:"scrypt_p,omitempty"`
}

// DerivedKey holds a single derived key and its metadata.
type DerivedKey struct {
	PrivKey    *secp256k1.PrivateKey
	PubKey     []byte // compressed 33 bytes
	PubKeyHash [crypto.PubKeyHashSize]byte
	Path       string // e.g. "m/44'/0'/0'/0/0"
	IsChange   bool
	Index      uint32
}

// HDWallet implements BIP39/BIP32 hierarchical deterministic wallet.
type HDWallet struct {
	mu sync.RWMutex

	dir      string
	mnemonic string
	seed     []byte

	accountKey  *hdkeychain.ExtendedKey // m/44'/0'/0'
	externalKey *hdkeychain.ExtendedKey // m/44'/0'/0'/0
	changeKey   *hdkeychain.ExtendedKey // m/44'/0'/0'/1

	externalKeys []*DerivedKey
	changeKeys   []*DerivedKey

	nextExternalIdx uint32
	nextChangeIdx   uint32

	// Lookup maps for fast key resolution.
	keysByHash    map[[crypto.PubKeyHashSize]byte]*DerivedKey
	keysByAddress map[string]*DerivedKey

	addrVersion byte // 0x00 mainnet, 0x6F testnet/regtest

	// Encryption state (matches Bitcoin Core's wallet lock model).
	encrypted   bool
	locked      bool
	unlockUntil time.Time
	encKey      []byte // derived AES key, cleared on lock
}

// NewHDWallet creates or loads an HD wallet from the given directory.
// If no wallet exists, a new 24-word mnemonic is generated.
func NewHDWallet(dir string, addrVersion byte) (*HDWallet, error) {
	w := &HDWallet{
		dir:           dir,
		addrVersion:   addrVersion,
		keysByHash:    make(map[[crypto.PubKeyHashSize]byte]*DerivedKey),
		keysByAddress: make(map[string]*DerivedKey),
	}

	walletPath := w.walletPath()
	data, err := os.ReadFile(walletPath)
	if err == nil && len(data) > 0 {
		return w, w.loadFromJSON(data)
	}

	return w, w.createNew("")
}

// NewHDWalletFromMnemonic restores a wallet from an existing mnemonic phrase.
func NewHDWalletFromMnemonic(dir string, addrVersion byte, mnemonic string) (*HDWallet, error) {
	w := &HDWallet{
		dir:           dir,
		addrVersion:   addrVersion,
		keysByHash:    make(map[[crypto.PubKeyHashSize]byte]*DerivedKey),
		keysByAddress: make(map[string]*DerivedKey),
	}
	return w, w.createNew(mnemonic)
}

func (w *HDWallet) createNew(mnemonic string) error {
	if mnemonic == "" {
		entropy, err := bip39.NewEntropy(256) // 24 words
		if err != nil {
			return fmt.Errorf("generate entropy: %w", err)
		}
		mnemonic, err = bip39.NewMnemonic(entropy)
		if err != nil {
			return fmt.Errorf("generate mnemonic: %w", err)
		}
	}

	if !bip39.IsMnemonicValid(mnemonic) {
		return fmt.Errorf("invalid mnemonic phrase")
	}

	seed := bip39.NewSeed(mnemonic, "")
	w.mnemonic = mnemonic
	w.seed = seed

	if err := w.deriveAccountKeys(); err != nil {
		return err
	}

	// Pre-derive the first external key (the default receiving address).
	if _, err := w.DeriveNextExternal(); err != nil {
		return fmt.Errorf("derive initial key: %w", err)
	}

	return w.save()
}

func (w *HDWallet) loadFromJSON(data []byte) error {
	var wd WalletData
	if err := json.Unmarshal(data, &wd); err != nil {
		return fmt.Errorf("parse wallet data: %w", err)
	}

	w.nextExternalIdx = wd.NextExternalIdx
	w.nextChangeIdx = wd.NextChangeIdx

	if wd.Encrypted {
		w.encrypted = true
		w.locked = true
		// For encrypted wallets, we can still derive public keys from the
		// stored seed if the wallet was previously unlocked and saved.
		// But on fresh load of an encrypted wallet, we need the mnemonic/seed
		// which are in the encrypted blob. We must load them to derive keys.
		// Attempt to load from the unencrypted fields if present (backward compat).
		if wd.Seed != "" && wd.Mnemonic != "" {
			return w.loadSeedAndDerive(wd.Mnemonic, wd.Seed)
		}
		// Encrypted wallet with no plaintext seed: keys cannot be derived until unlock.
		// We still mark the wallet as loaded so address queries work after unlock.
		return nil
	}

	return w.loadSeedAndDerive(wd.Mnemonic, wd.Seed)
}

func (w *HDWallet) loadSeedAndDerive(mnemonic, seedHex string) error {
	seedBytes, err := hex.DecodeString(seedHex)
	if err != nil {
		return fmt.Errorf("decode seed: %w", err)
	}

	w.mnemonic = mnemonic
	w.seed = seedBytes

	if err := w.deriveAccountKeys(); err != nil {
		return err
	}

	for i := uint32(0); i < w.nextExternalIdx; i++ {
		if _, err := w.deriveExternal(i); err != nil {
			return fmt.Errorf("re-derive external key %d: %w", i, err)
		}
	}
	for i := uint32(0); i < w.nextChangeIdx; i++ {
		if _, err := w.deriveChange(i); err != nil {
			return fmt.Errorf("re-derive change key %d: %w", i, err)
		}
	}

	return nil
}

func (w *HDWallet) deriveAccountKeys() error {
	// Use btcd's chaincfg for HD key derivation (we only need the HD key prefixes).
	net := &chaincfg.MainNetParams
	masterKey, err := hdkeychain.NewMaster(w.seed, net)
	if err != nil {
		return fmt.Errorf("derive master key: %w", err)
	}

	// m/44'
	purpose, err := masterKey.Derive(hdkeychain.HardenedKeyStart + bip44Purpose)
	if err != nil {
		return fmt.Errorf("derive purpose: %w", err)
	}

	// m/44'/0'
	coinType, err := purpose.Derive(hdkeychain.HardenedKeyStart + bip44CoinType)
	if err != nil {
		return fmt.Errorf("derive coin type: %w", err)
	}

	// m/44'/0'/0'
	account, err := coinType.Derive(hdkeychain.HardenedKeyStart + 0)
	if err != nil {
		return fmt.Errorf("derive account: %w", err)
	}
	w.accountKey = account

	// m/44'/0'/0'/0 (external/receiving)
	external, err := account.Derive(0)
	if err != nil {
		return fmt.Errorf("derive external chain: %w", err)
	}
	w.externalKey = external

	// m/44'/0'/0'/1 (change)
	change, err := account.Derive(1)
	if err != nil {
		return fmt.Errorf("derive change chain: %w", err)
	}
	w.changeKey = change

	return nil
}

func (w *HDWallet) deriveExternal(index uint32) (*DerivedKey, error) {
	child, err := w.externalKey.Derive(index)
	if err != nil {
		return nil, fmt.Errorf("derive external key %d: %w", index, err)
	}
	return w.registerKey(child, false, index)
}

func (w *HDWallet) deriveChange(index uint32) (*DerivedKey, error) {
	child, err := w.changeKey.Derive(index)
	if err != nil {
		return nil, fmt.Errorf("derive change key %d: %w", index, err)
	}
	return w.registerKey(child, true, index)
}

func (w *HDWallet) registerKey(extKey *hdkeychain.ExtendedKey, isChange bool, index uint32) (*DerivedKey, error) {
	ecPrivKey, err := extKey.ECPrivKey()
	if err != nil {
		return nil, fmt.Errorf("extract EC private key: %w", err)
	}

	privKey := secp256k1.PrivKeyFromBytes(ecPrivKey.Serialize())
	pubKey := privKey.PubKey().SerializeCompressed()
	pubKeyHash := crypto.PubKeyHash(pubKey)

	chainIdx := 0
	if isChange {
		chainIdx = 1
	}
	path := fmt.Sprintf("m/44'/0'/0'/%d/%d", chainIdx, index)

	dk := &DerivedKey{
		PrivKey:    privKey,
		PubKey:     pubKey,
		PubKeyHash: pubKeyHash,
		Path:       path,
		IsChange:   isChange,
		Index:      index,
	}

	if isChange {
		w.changeKeys = append(w.changeKeys, dk)
	} else {
		w.externalKeys = append(w.externalKeys, dk)
	}

	w.keysByHash[pubKeyHash] = dk
	addr := crypto.PubKeyHashToAddress(pubKeyHash, w.addrVersion)
	w.keysByAddress[addr] = dk

	return dk, nil
}

// DeriveNextExternal derives the next external (receiving) key and persists the wallet.
func (w *HDWallet) DeriveNextExternal() (*DerivedKey, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	dk, err := w.deriveExternal(w.nextExternalIdx)
	if err != nil {
		return nil, err
	}
	w.nextExternalIdx++
	if err := w.save(); err != nil {
		return nil, fmt.Errorf("save wallet: %w", err)
	}
	return dk, nil
}

// DeriveNextChange derives the next change key and persists the wallet.
func (w *HDWallet) DeriveNextChange() (*DerivedKey, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	dk, err := w.deriveChange(w.nextChangeIdx)
	if err != nil {
		return nil, err
	}
	w.nextChangeIdx++
	if err := w.save(); err != nil {
		return nil, fmt.Errorf("save wallet: %w", err)
	}
	return dk, nil
}

// GetNewAddress derives a new external address and returns it as a Base58Check string.
func (w *HDWallet) GetNewAddress() (string, error) {
	dk, err := w.DeriveNextExternal()
	if err != nil {
		return "", err
	}
	return crypto.PubKeyHashToAddress(dk.PubKeyHash, w.addrVersion), nil
}

// GetChangeAddress derives a new change address.
func (w *HDWallet) GetChangeAddress() (string, error) {
	dk, err := w.DeriveNextChange()
	if err != nil {
		return "", err
	}
	return crypto.PubKeyHashToAddress(dk.PubKeyHash, w.addrVersion), nil
}

// GetCurrentChangeAddress returns the most recently derived change address
// without advancing the index. If no change address exists yet, one is derived.
// This avoids wasting key indices when building transactions that may not be submitted.
func (w *HDWallet) GetCurrentChangeAddress() (string, error) {
	w.mu.RLock()
	if len(w.changeKeys) > 0 {
		dk := w.changeKeys[len(w.changeKeys)-1]
		addr := crypto.PubKeyHashToAddress(dk.PubKeyHash, w.addrVersion)
		w.mu.RUnlock()
		return addr, nil
	}
	w.mu.RUnlock()
	return w.GetChangeAddress()
}

// GetKeyForAddress returns the derived key for a given address, or nil if not ours.
func (w *HDWallet) GetKeyForAddress(address string) *DerivedKey {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.keysByAddress[address]
}

// GetKeyForPubKeyHash returns the derived key for a given pubkey hash, or nil.
func (w *HDWallet) GetKeyForPubKeyHash(hash [crypto.PubKeyHashSize]byte) *DerivedKey {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.keysByHash[hash]
}

// IsOurScript returns true if the given pkScript pays to one of our keys.
func (w *HDWallet) IsOurScript(pkScript []byte) bool {
	hashBytes := crypto.ExtractP2PKHHash(pkScript)
	if hashBytes == nil {
		return false
	}
	var hash [crypto.PubKeyHashSize]byte
	copy(hash[:], hashBytes)
	w.mu.RLock()
	defer w.mu.RUnlock()
	_, ok := w.keysByHash[hash]
	return ok
}

// KeyForScript returns the private key that can spend the given pkScript, or nil.
func (w *HDWallet) KeyForScript(pkScript []byte) *DerivedKey {
	hashBytes := crypto.ExtractP2PKHHash(pkScript)
	if hashBytes == nil {
		return nil
	}
	var hash [crypto.PubKeyHashSize]byte
	copy(hash[:], hashBytes)
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.keysByHash[hash]
}

// Mnemonic returns the BIP39 mnemonic phrase.
// Requires the wallet to be unlocked if encrypted.
func (w *HDWallet) Mnemonic() string {
	if w.IsLocked() {
		return ""
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.mnemonic
}

// AllAddresses returns all derived addresses (external + change).
func (w *HDWallet) AllAddresses() []string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	addrs := make([]string, 0, len(w.externalKeys)+len(w.changeKeys))
	for _, dk := range w.externalKeys {
		addrs = append(addrs, crypto.PubKeyHashToAddress(dk.PubKeyHash, w.addrVersion))
	}
	for _, dk := range w.changeKeys {
		addrs = append(addrs, crypto.PubKeyHashToAddress(dk.PubKeyHash, w.addrVersion))
	}
	return addrs
}

// ExternalAddresses returns all external (receiving) addresses.
func (w *HDWallet) ExternalAddresses() []string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	addrs := make([]string, 0, len(w.externalKeys))
	for _, dk := range w.externalKeys {
		addrs = append(addrs, crypto.PubKeyHashToAddress(dk.PubKeyHash, w.addrVersion))
	}
	return addrs
}

// DumpPrivKey returns the WIF-encoded private key for a given address.
// This matches Bitcoin Core's dumpprivkey behavior.
// Requires the wallet to be unlocked if encrypted.
func (w *HDWallet) DumpPrivKey(address string) (string, error) {
	if err := w.RequireUnlocked(); err != nil {
		return "", err
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	dk, ok := w.keysByAddress[address]
	if !ok {
		return "", fmt.Errorf("address not found in wallet: %s", address)
	}
	return crypto.EncodeWIF(dk.PrivKey.Serialize(), w.addrVersion), nil
}

// ImportPrivKey imports a private key in WIF format (or raw hex for backward compatibility)
// and registers it as an external key. Matches Bitcoin Core's importprivkey behavior.
func (w *HDWallet) ImportPrivKey(privKeyStr string) (string, error) {
	var privBytes []byte

	// Try WIF first (Bitcoin Core standard), then fall back to raw hex.
	wifKey, _, _, wifErr := crypto.DecodeWIF(privKeyStr)
	if wifErr == nil {
		privBytes = wifKey
	} else {
		hexBytes, hexErr := hex.DecodeString(privKeyStr)
		if hexErr != nil {
			return "", fmt.Errorf("invalid private key: not valid WIF or hex")
		}
		privBytes = hexBytes
	}

	privKey, err := crypto.PrivKeyFromBytes(privBytes)
	if err != nil {
		return "", err
	}

	pubKey := privKey.PubKey().SerializeCompressed()
	pubKeyHash := crypto.PubKeyHash(pubKey)
	addr := crypto.PubKeyHashToAddress(pubKeyHash, w.addrVersion)

	w.mu.Lock()
	defer w.mu.Unlock()

	if _, exists := w.keysByHash[pubKeyHash]; exists {
		return addr, nil // already imported
	}

	dk := &DerivedKey{
		PrivKey:    privKey,
		PubKey:     pubKey,
		PubKeyHash: pubKeyHash,
		Path:       "imported",
		IsChange:   false,
		Index:      0,
	}

	w.externalKeys = append(w.externalKeys, dk)
	w.keysByHash[pubKeyHash] = dk
	w.keysByAddress[addr] = dk

	return addr, nil
}

// GetDefaultAddress returns the first external address.
func (w *HDWallet) GetDefaultAddress() string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if len(w.externalKeys) == 0 {
		return ""
	}
	return crypto.PubKeyHashToAddress(w.externalKeys[0].PubKeyHash, w.addrVersion)
}

// GetDefaultP2PKHScript returns the P2PKH script for the first external key.
// Used as the mining reward script.
func (w *HDWallet) GetDefaultP2PKHScript() []byte {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if len(w.externalKeys) == 0 {
		return nil
	}
	return crypto.MakeP2PKHScript(w.externalKeys[0].PubKeyHash)
}

// KeyCount returns the total number of derived keys.
func (w *HDWallet) KeyCount() int {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return len(w.externalKeys) + len(w.changeKeys)
}

// AddressVersion returns the address version byte.
func (w *HDWallet) AddressVersion() byte {
	return w.addrVersion
}

func (w *HDWallet) save() error {
	if w.encrypted {
		return w.savePreservingEncryption()
	}
	wd := WalletData{
		Mnemonic:        w.mnemonic,
		Seed:            hex.EncodeToString(w.seed),
		NextExternalIdx: w.nextExternalIdx,
		NextChangeIdx:   w.nextChangeIdx,
	}
	data, err := json.MarshalIndent(wd, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal wallet: %w", err)
	}
	if err := os.MkdirAll(w.dir, 0700); err != nil {
		return fmt.Errorf("create wallet dir: %w", err)
	}
	return os.WriteFile(w.walletPath(), data, 0600)
}

// savePreservingEncryption updates only the index counters in an encrypted wallet
// file without touching the encrypted data fields.
func (w *HDWallet) savePreservingEncryption() error {
	existing, err := os.ReadFile(w.walletPath())
	if err != nil {
		return fmt.Errorf("read encrypted wallet: %w", err)
	}
	var wd WalletData
	if err := json.Unmarshal(existing, &wd); err != nil {
		return fmt.Errorf("parse encrypted wallet: %w", err)
	}
	wd.NextExternalIdx = w.nextExternalIdx
	wd.NextChangeIdx = w.nextChangeIdx
	data, err := json.MarshalIndent(wd, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal wallet: %w", err)
	}
	return os.WriteFile(w.walletPath(), data, 0600)
}

func (w *HDWallet) walletPath() string {
	return filepath.Join(w.dir, "wallet.json")
}

// MiningKeyCompat returns the P2PKH script for the default key, compatible
// with the old KeyStore interface used by the miner.
func (w *HDWallet) MiningKeyCompat() ([]byte, [crypto.PubKeyHashSize]byte) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if len(w.externalKeys) == 0 {
		var empty [crypto.PubKeyHashSize]byte
		return nil, empty
	}
	dk := w.externalKeys[0]
	return crypto.MakeP2PKHScript(dk.PubKeyHash), dk.PubKeyHash
}

// ListUnspent returns all UTXOs that belong to this wallet.
// The caller must provide a ForEach-style iterator over the UTXO set.
type UnspentOutput struct {
	TxHash        [32]byte
	Index         uint32
	Value         uint64
	Height        uint32
	Confirmations uint32
	Address       string
	PkScript      []byte
	IsCoinbase    bool
}

// FindUnspent scans the UTXO set for outputs belonging to this wallet.
func (w *HDWallet) FindUnspent(
	forEach func(fn func(txHash [32]byte, index uint32, value uint64, pkScript []byte, height uint32, isCoinbase bool)),
	tipHeight uint32,
) []UnspentOutput {
	w.mu.RLock()
	defer w.mu.RUnlock()

	var results []UnspentOutput
	forEach(func(txHash [32]byte, index uint32, value uint64, pkScript []byte, height uint32, isCoinbase bool) {
		hashBytes := crypto.ExtractP2PKHHash(pkScript)
		if hashBytes == nil {
			return
		}
		var hash [crypto.PubKeyHashSize]byte
		copy(hash[:], hashBytes)
		if _, ok := w.keysByHash[hash]; !ok {
			return
		}
		confs := uint32(0)
		if tipHeight >= height {
			confs = tipHeight - height + 1
		}
		addr := crypto.PubKeyHashToAddress(hash, w.addrVersion)
		results = append(results, UnspentOutput{
			TxHash:        txHash,
			Index:         index,
			Value:         value,
			Height:        height,
			Confirmations: confs,
			Address:       addr,
			PkScript:      pkScript,
			IsCoinbase:    isCoinbase,
		})
	})
	return results
}

// GetBalance returns the total confirmed balance (in smallest units) for this wallet.
func (w *HDWallet) GetBalance(
	forEach func(fn func(txHash [32]byte, index uint32, value uint64, pkScript []byte, height uint32, isCoinbase bool)),
	tipHeight uint32,
	minConf uint32,
	coinbaseMaturity uint32,
) uint64 {
	w.mu.RLock()
	defer w.mu.RUnlock()

	var total uint64
	forEach(func(txHash [32]byte, index uint32, value uint64, pkScript []byte, height uint32, isCoinbase bool) {
		hashBytes := crypto.ExtractP2PKHHash(pkScript)
		if hashBytes == nil {
			return
		}
		var hash [crypto.PubKeyHashSize]byte
		copy(hash[:], hashBytes)
		if _, ok := w.keysByHash[hash]; !ok {
			return
		}
		confs := uint32(0)
		if tipHeight >= height {
			confs = tipHeight - height + 1
		}
		if confs < minConf {
			return
		}
		if isCoinbase && confs < coinbaseMaturity {
			return
		}
		total += value
	})
	return total
}

// SignRawTransaction signs all inputs of a transaction that correspond to
// wallet-owned UTXOs. Returns the number of inputs signed and whether the
// transaction is complete (all inputs signed). The caller must provide a
// function to look up the previous output script for each input.
func (w *HDWallet) SignRawTransaction(
	tx *types.Transaction,
	getPrevScript func(txHash [32]byte, index uint32) []byte,
) (signed int, complete bool, err error) {
	if err := w.RequireUnlocked(); err != nil {
		return 0, false, err
	}
	w.mu.RLock()
	defer w.mu.RUnlock()

	signed = 0
	for i := range tx.Inputs {
		if len(tx.Inputs[i].SignatureScript) > 0 {
			signed++
			continue
		}
		prevScript := getPrevScript(tx.Inputs[i].PreviousOutPoint.Hash, tx.Inputs[i].PreviousOutPoint.Index)
		if prevScript == nil {
			continue
		}
		hashBytes := crypto.ExtractP2PKHHash(prevScript)
		if hashBytes == nil {
			continue
		}
		var hash [crypto.PubKeyHashSize]byte
		copy(hash[:], hashBytes)
		dk, ok := w.keysByHash[hash]
		if !ok {
			continue
		}
		sigScript, signErr := crypto.SignInput(tx, i, prevScript, dk.PrivKey)
		if signErr != nil {
			return signed, false, fmt.Errorf("sign input %d: %w", i, signErr)
		}
		tx.Inputs[i].SignatureScript = sigScript
		signed++
	}

	complete = signed == len(tx.Inputs)
	return signed, complete, nil
}

// BackupWallet copies the wallet file to the specified destination path.
func (w *HDWallet) BackupWallet(destPath string) error {
	w.mu.RLock()
	defer w.mu.RUnlock()

	src := w.walletPath()
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read wallet file: %w", err)
	}
	dir := filepath.Dir(destPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create backup dir: %w", err)
	}
	return os.WriteFile(destPath, data, 0600)
}

// --- Encryption (Bitcoin Core parity: encryptwallet / walletpassphrase / walletlock) ---

const (
	scryptN = 1 << 15 // 32768
	scryptR = 8
	scryptP = 1
	keyLen  = 32 // AES-256
	saltLen = 16
)

// IsEncrypted returns whether the wallet has been encrypted with a passphrase.
func (w *HDWallet) IsEncrypted() bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.encrypted
}

// IsLocked returns whether the wallet is currently locked.
// An unencrypted wallet is never locked.
// If the timed unlock has expired, the wallet is re-locked and the
// in-memory AES key is wiped (matching Bitcoin Core's auto-relock).
func (w *HDWallet) IsLocked() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.encrypted {
		return false
	}
	if w.locked {
		return true
	}
	if !w.unlockUntil.IsZero() && time.Now().After(w.unlockUntil) {
		w.locked = true
		for i := range w.encKey {
			w.encKey[i] = 0
		}
		w.encKey = nil
		for i := range w.seed {
			w.seed[i] = 0
		}
		w.mnemonic = ""
		w.unlockUntil = time.Time{}
		return true
	}
	return false
}

// EncryptWallet encrypts the wallet with the given passphrase.
// After encryption the wallet is locked. This matches Bitcoin Core's
// encryptwallet behavior (requires restart in Bitcoin Core; we lock in-place).
func (w *HDWallet) EncryptWallet(passphrase string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.encrypted {
		return fmt.Errorf("wallet is already encrypted")
	}
	if passphrase == "" {
		return fmt.Errorf("passphrase must not be empty")
	}

	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return fmt.Errorf("generate salt: %w", err)
	}

	aesKey, err := scrypt.Key([]byte(passphrase), salt, scryptN, scryptR, scryptP, keyLen)
	if err != nil {
		return fmt.Errorf("derive key: %w", err)
	}

	plaintext := w.mnemonic + "\n" + hex.EncodeToString(w.seed)
	ciphertext, iv, err := aesEncrypt(aesKey, []byte(plaintext))
	if err != nil {
		return fmt.Errorf("encrypt: %w", err)
	}

	w.encrypted = true
	w.locked = true
	w.encKey = nil

	return w.saveEncrypted(ciphertext, salt, iv)
}

// WalletPassphrase unlocks the wallet for the given duration.
// Matches Bitcoin Core's walletpassphrase RPC.
func (w *HDWallet) WalletPassphrase(passphrase string, timeoutSecs int64) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if !w.encrypted {
		return fmt.Errorf("wallet is not encrypted")
	}

	// Read the encrypted wallet data to verify the passphrase.
	data, err := os.ReadFile(w.walletPath())
	if err != nil {
		return fmt.Errorf("read wallet: %w", err)
	}
	var wd WalletData
	if err := json.Unmarshal(data, &wd); err != nil {
		return fmt.Errorf("parse wallet: %w", err)
	}

	salt, err := hex.DecodeString(wd.Salt)
	if err != nil {
		return fmt.Errorf("decode salt: %w", err)
	}
	iv, err := hex.DecodeString(wd.IV)
	if err != nil {
		return fmt.Errorf("decode iv: %w", err)
	}
	ciphertext, err := hex.DecodeString(wd.EncryptedData)
	if err != nil {
		return fmt.Errorf("decode ciphertext: %w", err)
	}

	n, r, p := wd.ScryptN, wd.ScryptR, wd.ScryptP
	if n == 0 {
		n = scryptN
	}
	if r == 0 {
		r = scryptR
	}
	if p == 0 {
		p = scryptP
	}

	aesKey, err := scrypt.Key([]byte(passphrase), salt, n, r, p, keyLen)
	if err != nil {
		return fmt.Errorf("derive key: %w", err)
	}

	plaintext, err := aesDecrypt(aesKey, iv, ciphertext)
	if err != nil {
		return fmt.Errorf("incorrect passphrase")
	}

	// Verify the decrypted data looks valid (mnemonic\nseed_hex).
	parts := splitOnNewline(string(plaintext))
	if len(parts) < 2 {
		return fmt.Errorf("incorrect passphrase")
	}
	if !bip39.IsMnemonicValid(parts[0]) {
		return fmt.Errorf("incorrect passphrase")
	}

	// If keys haven't been derived yet (fresh load of encrypted wallet),
	// derive them now from the decrypted seed.
	if w.seed == nil || len(w.seed) == 0 {
		if err := w.loadSeedAndDerive(parts[0], parts[1]); err != nil {
			return fmt.Errorf("derive keys: %w", err)
		}
	}

	w.locked = false
	w.encKey = aesKey
	if timeoutSecs > 0 {
		w.unlockUntil = time.Now().Add(time.Duration(timeoutSecs) * time.Second)
	} else {
		w.unlockUntil = time.Time{}
	}

	return nil
}

// WalletLock immediately locks the wallet. Matches Bitcoin Core's walletlock RPC.
// Sensitive key material is zeroed from memory to limit the window of exposure.
func (w *HDWallet) WalletLock() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if !w.encrypted {
		return fmt.Errorf("wallet is not encrypted")
	}

	w.locked = true

	// Zero the AES encryption key.
	for i := range w.encKey {
		w.encKey[i] = 0
	}
	w.encKey = nil

	// Zero the seed.
	for i := range w.seed {
		w.seed[i] = 0
	}

	// Zero the mnemonic string (best-effort; Go strings are immutable but we
	// can overwrite the backing slice if we control it).
	w.mnemonic = ""

	w.unlockUntil = time.Time{}
	return nil
}

// RequireUnlocked returns an error if the wallet is locked.
// Call this before any operation that needs private key access.
func (w *HDWallet) RequireUnlocked() error {
	if !w.IsEncrypted() {
		return nil
	}
	if w.IsLocked() {
		return fmt.Errorf("wallet is locked, use walletpassphrase to unlock")
	}
	return nil
}

func (w *HDWallet) saveEncrypted(ciphertext, salt, iv []byte) error {
	wd := WalletData{
		NextExternalIdx: w.nextExternalIdx,
		NextChangeIdx:   w.nextChangeIdx,
		Encrypted:       true,
		EncryptedData:   hex.EncodeToString(ciphertext),
		Salt:            hex.EncodeToString(salt),
		IV:              hex.EncodeToString(iv),
		ScryptN:         scryptN,
		ScryptR:         scryptR,
		ScryptP:         scryptP,
	}
	data, err := json.MarshalIndent(wd, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal wallet: %w", err)
	}
	if err := os.MkdirAll(w.dir, 0700); err != nil {
		return fmt.Errorf("create wallet dir: %w", err)
	}
	return os.WriteFile(w.walletPath(), data, 0600)
}

func aesEncrypt(key, plaintext []byte) (ciphertext, iv []byte, err error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, err
	}
	iv = make([]byte, aes.BlockSize)
	if _, err := rand.Read(iv); err != nil {
		return nil, nil, err
	}
	padded := pkcs7Pad(plaintext, aes.BlockSize)
	ciphertext = make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ciphertext, padded)
	return ciphertext, iv, nil
}

func aesDecrypt(key, iv, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	if len(ciphertext)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("ciphertext not block-aligned")
	}
	plaintext := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plaintext, ciphertext)
	return pkcs7Unpad(plaintext, aes.BlockSize)
}

func pkcs7Pad(data []byte, blockSize int) []byte {
	padding := blockSize - len(data)%blockSize
	pad := make([]byte, padding)
	for i := range pad {
		pad[i] = byte(padding)
	}
	return append(data, pad...)
}

func pkcs7Unpad(data []byte, blockSize int) ([]byte, error) {
	if len(data) == 0 || len(data)%blockSize != 0 {
		return nil, fmt.Errorf("invalid padding")
	}
	padLen := int(data[len(data)-1])
	if padLen == 0 || padLen > blockSize {
		return nil, fmt.Errorf("invalid padding")
	}
	for i := len(data) - padLen; i < len(data); i++ {
		if data[i] != byte(padLen) {
			return nil, fmt.Errorf("invalid padding")
		}
	}
	return data[:len(data)-padLen], nil
}

func splitOnNewline(s string) []string {
	var parts []string
	current := ""
	for _, c := range s {
		if c == '\n' {
			parts = append(parts, current)
			current = ""
		} else {
			current += string(c)
		}
	}
	if current != "" {
		parts = append(parts, current)
	}
	return parts
}

// --- Helpers for UTXO iteration adapter ---

// OutpointFromKey extracts txHash and index from a 36-byte outpoint key.
func OutpointFromKey(key [36]byte) ([32]byte, uint32) {
	var txHash [32]byte
	copy(txHash[:], key[:32])
	index := binary.LittleEndian.Uint32(key[32:])
	return txHash, index
}
