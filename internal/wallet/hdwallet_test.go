package wallet

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/bams-repo/fairchain/internal/crypto"
)

func TestNewHDWalletCreatesAndPersists(t *testing.T) {
	dir := t.TempDir()
	w, err := NewHDWallet(dir, 0x00)
	if err != nil {
		t.Fatalf("NewHDWallet: %v", err)
	}

	if w.Mnemonic() == "" {
		t.Fatal("mnemonic should not be empty")
	}

	words := len(splitWords(w.Mnemonic()))
	if words != 24 {
		t.Fatalf("expected 24-word mnemonic, got %d", words)
	}

	if w.KeyCount() != 1 {
		t.Fatalf("expected 1 key, got %d", w.KeyCount())
	}

	addr := w.GetDefaultAddress()
	if addr == "" {
		t.Fatal("default address should not be empty")
	}
	if addr[0] != '1' {
		t.Fatalf("mainnet address should start with '1', got %q", addr)
	}

	// Verify wallet file exists.
	walletPath := filepath.Join(dir, "wallet.json")
	if _, err := os.Stat(walletPath); os.IsNotExist(err) {
		t.Fatal("wallet.json should exist")
	}
}

func TestHDWalletPersistenceAndReload(t *testing.T) {
	dir := t.TempDir()

	w1, err := NewHDWallet(dir, 0x6F)
	if err != nil {
		t.Fatalf("NewHDWallet: %v", err)
	}

	mnemonic := w1.Mnemonic()
	addr1 := w1.GetDefaultAddress()

	// Derive a second address.
	addr2, err := w1.GetNewAddress()
	if err != nil {
		t.Fatalf("GetNewAddress: %v", err)
	}

	if addr1 == addr2 {
		t.Fatal("second address should differ from first")
	}

	// Reload wallet from disk.
	w2, err := NewHDWallet(dir, 0x6F)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}

	if w2.Mnemonic() != mnemonic {
		t.Fatal("mnemonic mismatch after reload")
	}

	if w2.KeyCount() != w1.KeyCount() {
		t.Fatalf("key count mismatch: got %d, want %d", w2.KeyCount(), w1.KeyCount())
	}

	if w2.GetDefaultAddress() != addr1 {
		t.Fatal("default address mismatch after reload")
	}

	// The second address should also be present.
	dk := w2.GetKeyForAddress(addr2)
	if dk == nil {
		t.Fatal("second address not found after reload")
	}
}

func TestHDWalletFromMnemonic(t *testing.T) {
	dir1 := t.TempDir()
	w1, err := NewHDWallet(dir1, 0x00)
	if err != nil {
		t.Fatalf("NewHDWallet: %v", err)
	}

	mnemonic := w1.Mnemonic()
	addr1 := w1.GetDefaultAddress()

	// Restore from mnemonic in a different directory.
	dir2 := t.TempDir()
	w2, err := NewHDWalletFromMnemonic(dir2, 0x00, mnemonic)
	if err != nil {
		t.Fatalf("NewHDWalletFromMnemonic: %v", err)
	}

	if w2.GetDefaultAddress() != addr1 {
		t.Fatalf("restored address mismatch: got %q, want %q", w2.GetDefaultAddress(), addr1)
	}
}

func TestHDWalletDeriveMultipleKeys(t *testing.T) {
	dir := t.TempDir()
	w, err := NewHDWallet(dir, 0x00)
	if err != nil {
		t.Fatalf("NewHDWallet: %v", err)
	}

	addrs := make(map[string]bool)
	addrs[w.GetDefaultAddress()] = true

	for i := 0; i < 10; i++ {
		addr, err := w.GetNewAddress()
		if err != nil {
			t.Fatalf("GetNewAddress %d: %v", i, err)
		}
		if addrs[addr] {
			t.Fatalf("duplicate address at index %d: %s", i, addr)
		}
		addrs[addr] = true
	}

	if w.KeyCount() != 11 {
		t.Fatalf("expected 11 keys, got %d", w.KeyCount())
	}
}

func TestHDWalletChangeAddresses(t *testing.T) {
	dir := t.TempDir()
	w, err := NewHDWallet(dir, 0x00)
	if err != nil {
		t.Fatalf("NewHDWallet: %v", err)
	}

	changeAddr, err := w.GetChangeAddress()
	if err != nil {
		t.Fatalf("GetChangeAddress: %v", err)
	}

	// Change address should differ from the default receiving address.
	if changeAddr == w.GetDefaultAddress() {
		t.Fatal("change address should differ from default address")
	}

	// Change key should be found.
	dk := w.GetKeyForAddress(changeAddr)
	if dk == nil {
		t.Fatal("change address not found in wallet")
	}
	if !dk.IsChange {
		t.Fatal("expected change key")
	}
}

func TestHDWalletIsOurScript(t *testing.T) {
	dir := t.TempDir()
	w, err := NewHDWallet(dir, 0x00)
	if err != nil {
		t.Fatalf("NewHDWallet: %v", err)
	}

	script := w.GetDefaultP2PKHScript()
	if !w.IsOurScript(script) {
		t.Fatal("wallet should recognize its own script")
	}

	otherScript := crypto.MakeP2PKHScript([crypto.PubKeyHashSize]byte{0xff, 0xfe, 0xfd})
	if w.IsOurScript(otherScript) {
		t.Fatal("wallet should not recognize a foreign script")
	}
}

func TestHDWalletDumpAndImportPrivKeyWIF(t *testing.T) {
	dir := t.TempDir()
	w, err := NewHDWallet(dir, 0x00)
	if err != nil {
		t.Fatalf("NewHDWallet: %v", err)
	}

	addr := w.GetDefaultAddress()
	wifKey, err := w.DumpPrivKey(addr)
	if err != nil {
		t.Fatalf("DumpPrivKey: %v", err)
	}

	// WIF-compressed mainnet keys start with 'K' or 'L'.
	if wifKey[0] != 'K' && wifKey[0] != 'L' {
		t.Fatalf("mainnet WIF should start with 'K' or 'L', got %q", wifKey[:1])
	}

	// Import WIF into a different wallet.
	dir2 := t.TempDir()
	w2, err := NewHDWallet(dir2, 0x00)
	if err != nil {
		t.Fatalf("NewHDWallet: %v", err)
	}

	importedAddr, err := w2.ImportPrivKey(wifKey)
	if err != nil {
		t.Fatalf("ImportPrivKey WIF: %v", err)
	}

	if importedAddr != addr {
		t.Fatalf("imported address mismatch: got %q, want %q", importedAddr, addr)
	}

	dk := w2.GetKeyForAddress(importedAddr)
	if dk == nil {
		t.Fatal("imported key not found")
	}
	if dk.Path != "imported" {
		t.Fatalf("expected path 'imported', got %q", dk.Path)
	}
}

func TestHDWalletDumpAndImportPrivKeyTestnet(t *testing.T) {
	dir := t.TempDir()
	w, err := NewHDWallet(dir, 0x6F)
	if err != nil {
		t.Fatalf("NewHDWallet: %v", err)
	}

	addr := w.GetDefaultAddress()
	wifKey, err := w.DumpPrivKey(addr)
	if err != nil {
		t.Fatalf("DumpPrivKey: %v", err)
	}

	// WIF-compressed testnet keys start with 'c'.
	if wifKey[0] != 'c' {
		t.Fatalf("testnet WIF should start with 'c', got %q", wifKey[:1])
	}

	// Import into a different wallet.
	dir2 := t.TempDir()
	w2, err := NewHDWallet(dir2, 0x6F)
	if err != nil {
		t.Fatalf("NewHDWallet: %v", err)
	}

	importedAddr, err := w2.ImportPrivKey(wifKey)
	if err != nil {
		t.Fatalf("ImportPrivKey WIF: %v", err)
	}

	if importedAddr != addr {
		t.Fatalf("imported address mismatch: got %q, want %q", importedAddr, addr)
	}
}

func TestHDWalletImportPrivKeyHexFallback(t *testing.T) {
	dir := t.TempDir()
	w, err := NewHDWallet(dir, 0x00)
	if err != nil {
		t.Fatalf("NewHDWallet: %v", err)
	}

	// Generate a key and get its raw hex.
	dk := w.GetKeyForAddress(w.GetDefaultAddress())
	rawHex := fmt.Sprintf("%x", dk.PrivKey.Serialize())

	// Import raw hex into a different wallet (backward compatibility).
	dir2 := t.TempDir()
	w2, err := NewHDWallet(dir2, 0x00)
	if err != nil {
		t.Fatalf("NewHDWallet: %v", err)
	}

	importedAddr, err := w2.ImportPrivKey(rawHex)
	if err != nil {
		t.Fatalf("ImportPrivKey hex: %v", err)
	}

	if importedAddr != w.GetDefaultAddress() {
		t.Fatalf("hex import address mismatch: got %q, want %q", importedAddr, w.GetDefaultAddress())
	}
}

func TestHDWalletInvalidMnemonic(t *testing.T) {
	dir := t.TempDir()
	_, err := NewHDWalletFromMnemonic(dir, 0x00, "not a valid mnemonic phrase")
	if err == nil {
		t.Fatal("expected error for invalid mnemonic")
	}
}

func TestHDWalletEncryptAndUnlock(t *testing.T) {
	dir := t.TempDir()
	w, err := NewHDWallet(dir, 0x00)
	if err != nil {
		t.Fatalf("NewHDWallet: %v", err)
	}

	if w.IsEncrypted() {
		t.Fatal("new wallet should not be encrypted")
	}
	if w.IsLocked() {
		t.Fatal("unencrypted wallet should not be locked")
	}

	addr := w.GetDefaultAddress()
	mnemonic := w.Mnemonic()

	if err := w.EncryptWallet("testpassword123"); err != nil {
		t.Fatalf("EncryptWallet: %v", err)
	}

	if !w.IsEncrypted() {
		t.Fatal("wallet should be encrypted")
	}
	if !w.IsLocked() {
		t.Fatal("wallet should be locked after encryption")
	}

	// Mnemonic should be empty when locked.
	if w.Mnemonic() != "" {
		t.Fatal("mnemonic should be empty when locked")
	}

	// DumpPrivKey should fail when locked.
	if _, err := w.DumpPrivKey(addr); err == nil {
		t.Fatal("DumpPrivKey should fail when locked")
	}

	// Unlock with wrong passphrase should fail.
	if err := w.WalletPassphrase("wrongpassword", 300); err == nil {
		t.Fatal("WalletPassphrase should fail with wrong passphrase")
	}

	// Unlock with correct passphrase.
	if err := w.WalletPassphrase("testpassword123", 300); err != nil {
		t.Fatalf("WalletPassphrase: %v", err)
	}

	if w.IsLocked() {
		t.Fatal("wallet should be unlocked")
	}

	// Mnemonic should be accessible when unlocked.
	if w.Mnemonic() != mnemonic {
		t.Fatal("mnemonic mismatch after unlock")
	}

	// DumpPrivKey should work when unlocked.
	wif, err := w.DumpPrivKey(addr)
	if err != nil {
		t.Fatalf("DumpPrivKey after unlock: %v", err)
	}
	if wif == "" {
		t.Fatal("WIF should not be empty")
	}

	// Lock again.
	if err := w.WalletLock(); err != nil {
		t.Fatalf("WalletLock: %v", err)
	}
	if !w.IsLocked() {
		t.Fatal("wallet should be locked after WalletLock")
	}
}

func TestHDWalletEncryptAlreadyEncrypted(t *testing.T) {
	dir := t.TempDir()
	w, err := NewHDWallet(dir, 0x00)
	if err != nil {
		t.Fatalf("NewHDWallet: %v", err)
	}

	if err := w.EncryptWallet("pass1"); err != nil {
		t.Fatalf("EncryptWallet: %v", err)
	}

	if err := w.EncryptWallet("pass2"); err == nil {
		t.Fatal("should not be able to encrypt an already encrypted wallet")
	}
}

func TestHDWalletEncryptedPersistence(t *testing.T) {
	dir := t.TempDir()
	w1, err := NewHDWallet(dir, 0x00)
	if err != nil {
		t.Fatalf("NewHDWallet: %v", err)
	}

	addr := w1.GetDefaultAddress()

	if err := w1.EncryptWallet("mypassword"); err != nil {
		t.Fatalf("EncryptWallet: %v", err)
	}

	// Reload wallet from disk.
	w2, err := NewHDWallet(dir, 0x00)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}

	if !w2.IsEncrypted() {
		t.Fatal("reloaded wallet should be encrypted")
	}
	if !w2.IsLocked() {
		t.Fatal("reloaded wallet should be locked")
	}

	// Unlock and verify address matches.
	if err := w2.WalletPassphrase("mypassword", 300); err != nil {
		t.Fatalf("WalletPassphrase: %v", err)
	}

	if w2.GetDefaultAddress() != addr {
		t.Fatalf("address mismatch after encrypted reload: got %q, want %q", w2.GetDefaultAddress(), addr)
	}
}

func TestHDWalletGetCurrentChangeAddress(t *testing.T) {
	dir := t.TempDir()
	w, err := NewHDWallet(dir, 0x00)
	if err != nil {
		t.Fatalf("NewHDWallet: %v", err)
	}

	// First call should derive a change address.
	addr1, err := w.GetCurrentChangeAddress()
	if err != nil {
		t.Fatalf("GetCurrentChangeAddress: %v", err)
	}
	if addr1 == "" {
		t.Fatal("change address should not be empty")
	}

	// Second call should return the same address without advancing.
	addr2, err := w.GetCurrentChangeAddress()
	if err != nil {
		t.Fatalf("GetCurrentChangeAddress: %v", err)
	}
	if addr1 != addr2 {
		t.Fatalf("GetCurrentChangeAddress should return same address: got %q and %q", addr1, addr2)
	}
}

func splitWords(s string) []string {
	var words []string
	word := ""
	for _, c := range s {
		if c == ' ' {
			if word != "" {
				words = append(words, word)
				word = ""
			}
		} else {
			word += string(c)
		}
	}
	if word != "" {
		words = append(words, word)
	}
	return words
}
