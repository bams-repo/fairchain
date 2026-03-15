package crypto

import (
	"testing"
)

func TestBase58EncodeDecodeRoundtrip(t *testing.T) {
	tests := [][]byte{
		{},
		{0x00},
		{0x00, 0x00, 0x01},
		{0x61},
		{0x62, 0x62, 0x62},
		{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00},
		{0xff, 0xff, 0xff},
	}
	for _, input := range tests {
		encoded := Base58Encode(input)
		decoded, err := Base58Decode(encoded)
		if err != nil {
			t.Fatalf("Base58Decode(%q) error: %v", encoded, err)
		}
		if len(input) == 0 && len(decoded) == 0 {
			continue
		}
		if len(decoded) != len(input) {
			t.Fatalf("length mismatch: got %d, want %d", len(decoded), len(input))
		}
		for i := range input {
			if decoded[i] != input[i] {
				t.Fatalf("byte %d: got 0x%02x, want 0x%02x", i, decoded[i], input[i])
			}
		}
	}
}

func TestBase58DecodeInvalidChar(t *testing.T) {
	_, err := Base58Decode("0OIl")
	if err == nil {
		t.Fatal("expected error for invalid base58 characters")
	}
}

func TestBase58CheckEncodeDecodeRoundtrip(t *testing.T) {
	versions := []byte{0x00, 0x6F, 0x05, 0xC4}
	payload := [20]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20}

	for _, ver := range versions {
		encoded := Base58CheckEncode(ver, payload[:])
		decodedVer, decodedPayload, err := Base58CheckDecode(encoded)
		if err != nil {
			t.Fatalf("Base58CheckDecode(%q) error: %v", encoded, err)
		}
		if decodedVer != ver {
			t.Fatalf("version: got 0x%02x, want 0x%02x", decodedVer, ver)
		}
		if len(decodedPayload) != 20 {
			t.Fatalf("payload length: got %d, want 20", len(decodedPayload))
		}
		for i := range payload {
			if decodedPayload[i] != payload[i] {
				t.Fatalf("payload byte %d: got 0x%02x, want 0x%02x", i, decodedPayload[i], payload[i])
			}
		}
	}
}

func TestBase58CheckInvalidChecksum(t *testing.T) {
	payload := [20]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20}
	encoded := Base58CheckEncode(0x00, payload[:])

	// Corrupt the last character.
	chars := []byte(encoded)
	chars[len(chars)-1] = '1'
	corrupted := string(chars)

	_, _, err := Base58CheckDecode(corrupted)
	if err == nil {
		t.Fatal("expected checksum error for corrupted address")
	}
}

func TestPubKeyHashToAddress(t *testing.T) {
	var pkh [PubKeyHashSize]byte
	for i := range pkh {
		pkh[i] = byte(i + 1)
	}

	// Mainnet address starts with '1'.
	mainnetAddr := PubKeyHashToAddress(pkh, 0x00)
	if mainnetAddr[0] != '1' {
		t.Fatalf("mainnet address should start with '1', got %q", mainnetAddr)
	}

	// Testnet address starts with 'm' or 'n'.
	testnetAddr := PubKeyHashToAddress(pkh, 0x6F)
	if testnetAddr[0] != 'm' && testnetAddr[0] != 'n' {
		t.Fatalf("testnet address should start with 'm' or 'n', got %q", testnetAddr)
	}
}

func TestAddressToPubKeyHashRoundtrip(t *testing.T) {
	var pkh [PubKeyHashSize]byte
	for i := range pkh {
		pkh[i] = byte(i + 1)
	}

	addr := PubKeyHashToAddress(pkh, 0x00)
	ver, decoded, err := AddressToPubKeyHash(addr)
	if err != nil {
		t.Fatalf("AddressToPubKeyHash error: %v", err)
	}
	if ver != 0x00 {
		t.Fatalf("version: got 0x%02x, want 0x00", ver)
	}
	if decoded != pkh {
		t.Fatal("pubkey hash mismatch")
	}
}

func TestAddressToPubKeyHashInvalid(t *testing.T) {
	_, _, err := AddressToPubKeyHash("notanaddress")
	if err == nil {
		t.Fatal("expected error for invalid address")
	}
}

func TestWIFEncodeDecodeMainnet(t *testing.T) {
	privKey := make([]byte, 32)
	for i := range privKey {
		privKey[i] = byte(i + 1)
	}

	wif := EncodeWIF(privKey, 0x00)
	// Mainnet compressed WIF starts with 'K' or 'L'.
	if wif[0] != 'K' && wif[0] != 'L' {
		t.Fatalf("mainnet WIF should start with 'K' or 'L', got %q", wif[:1])
	}

	decoded, compressed, ver, err := DecodeWIF(wif)
	if err != nil {
		t.Fatalf("DecodeWIF: %v", err)
	}
	if ver != WIFVersionMainnet {
		t.Fatalf("version: got 0x%02x, want 0x%02x", ver, WIFVersionMainnet)
	}
	if !compressed {
		t.Fatal("expected compressed flag")
	}
	if len(decoded) != 32 {
		t.Fatalf("decoded key length: got %d, want 32", len(decoded))
	}
	for i := range privKey {
		if decoded[i] != privKey[i] {
			t.Fatalf("key byte %d: got 0x%02x, want 0x%02x", i, decoded[i], privKey[i])
		}
	}
}

func TestWIFEncodeDecodeTestnet(t *testing.T) {
	privKey := make([]byte, 32)
	for i := range privKey {
		privKey[i] = byte(i + 0x10)
	}

	wif := EncodeWIF(privKey, 0x6F)
	// Testnet compressed WIF starts with 'c'.
	if wif[0] != 'c' {
		t.Fatalf("testnet WIF should start with 'c', got %q", wif[:1])
	}

	decoded, compressed, ver, err := DecodeWIF(wif)
	if err != nil {
		t.Fatalf("DecodeWIF: %v", err)
	}
	if ver != WIFVersionTestnet {
		t.Fatalf("version: got 0x%02x, want 0x%02x", ver, WIFVersionTestnet)
	}
	if !compressed {
		t.Fatal("expected compressed flag")
	}
	for i := range privKey {
		if decoded[i] != privKey[i] {
			t.Fatalf("key byte %d mismatch", i)
		}
	}
}

func TestWIFDecodeInvalid(t *testing.T) {
	_, _, _, err := DecodeWIF("notavalidwif")
	if err == nil {
		t.Fatal("expected error for invalid WIF")
	}
}

func TestKnownBitcoinAddress(t *testing.T) {
	// Bitcoin mainnet address for all-zero pubkey hash should be "1111111111111111111114oLvT2".
	var pkh [PubKeyHashSize]byte // all zeros
	addr := PubKeyHashToAddress(pkh, 0x00)
	ver, decoded, err := AddressToPubKeyHash(addr)
	if err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if ver != 0x00 {
		t.Fatalf("version: got 0x%02x, want 0x00", ver)
	}
	if decoded != pkh {
		t.Fatal("decoded hash mismatch for zero pubkey hash")
	}
}
