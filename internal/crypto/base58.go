package crypto

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"math/big"
)

const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

var bigZero = big.NewInt(0)
var bigRadix = big.NewInt(58)

// Base58Encode encodes a byte slice to a Base58-encoded string.
func Base58Encode(b []byte) string {
	x := new(big.Int).SetBytes(b)
	result := make([]byte, 0, len(b)*136/100)
	mod := new(big.Int)
	for x.Cmp(bigZero) > 0 {
		x.DivMod(x, bigRadix, mod)
		result = append(result, base58Alphabet[mod.Int64()])
	}
	for _, byt := range b {
		if byt != 0x00 {
			break
		}
		result = append(result, base58Alphabet[0])
	}
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}
	return string(result)
}

// Base58Decode decodes a Base58-encoded string to a byte slice.
func Base58Decode(s string) ([]byte, error) {
	result := big.NewInt(0)
	for _, c := range []byte(s) {
		idx := base58AlphabetIndex(c)
		if idx < 0 {
			return nil, errors.New("invalid base58 character")
		}
		result.Mul(result, bigRadix)
		result.Add(result, big.NewInt(int64(idx)))
	}
	decoded := result.Bytes()
	numLeadingZeros := 0
	for _, c := range []byte(s) {
		if c != base58Alphabet[0] {
			break
		}
		numLeadingZeros++
	}
	final := make([]byte, numLeadingZeros+len(decoded))
	copy(final[numLeadingZeros:], decoded)
	return final, nil
}

func base58AlphabetIndex(c byte) int {
	for i, a := range []byte(base58Alphabet) {
		if a == c {
			return i
		}
	}
	return -1
}

func base58Checksum(payload []byte) [4]byte {
	first := sha256.Sum256(payload)
	second := sha256.Sum256(first[:])
	var cksum [4]byte
	copy(cksum[:], second[:4])
	return cksum
}

// Base58CheckEncode encodes a version byte and payload into a Base58Check string.
// This is the standard Bitcoin address encoding: version(1) + payload(N) + checksum(4).
func Base58CheckEncode(version byte, payload []byte) string {
	versioned := make([]byte, 1+len(payload))
	versioned[0] = version
	copy(versioned[1:], payload)
	cksum := base58Checksum(versioned)
	full := append(versioned, cksum[:]...)
	return Base58Encode(full)
}

// Base58CheckDecode decodes a Base58Check string, returning the version byte and payload.
func Base58CheckDecode(address string) (version byte, payload []byte, err error) {
	decoded, err := Base58Decode(address)
	if err != nil {
		return 0, nil, err
	}
	if len(decoded) < 5 {
		return 0, nil, errors.New("base58check: decoded too short")
	}
	version = decoded[0]
	payload = decoded[1 : len(decoded)-4]
	cksum := base58Checksum(decoded[:len(decoded)-4])
	if cksum[0] != decoded[len(decoded)-4] ||
		cksum[1] != decoded[len(decoded)-3] ||
		cksum[2] != decoded[len(decoded)-2] ||
		cksum[3] != decoded[len(decoded)-1] {
		return 0, nil, errors.New("base58check: invalid checksum")
	}
	return version, payload, nil
}

// PubKeyHashToAddress converts a 20-byte pubkey hash to a Base58Check address
// using the given version byte (0x00 for mainnet, 0x6F for testnet).
func PubKeyHashToAddress(pubKeyHash [PubKeyHashSize]byte, version byte) string {
	return Base58CheckEncode(version, pubKeyHash[:])
}

// AddressToPubKeyHash decodes a Base58Check address and returns the version and
// 20-byte pubkey hash. Returns an error if the address is invalid or the payload
// is not exactly 20 bytes.
func AddressToPubKeyHash(address string) (version byte, pubKeyHash [PubKeyHashSize]byte, err error) {
	ver, payload, err := Base58CheckDecode(address)
	if err != nil {
		return 0, pubKeyHash, err
	}
	if len(payload) != PubKeyHashSize {
		return 0, pubKeyHash, errors.New("address payload is not 20 bytes")
	}
	copy(pubKeyHash[:], payload)
	return ver, pubKeyHash, nil
}

// WIF version bytes matching Bitcoin Core.
const (
	WIFVersionMainnet = 0x80
	WIFVersionTestnet = 0xEF
)

// EncodeWIF encodes a 32-byte private key into Wallet Import Format (WIF).
// Bitcoin Core WIF: Base58Check(version(1) + privkey(32) + compressed_flag(1))
// The compressed flag byte (0x01) indicates the corresponding public key is compressed.
func EncodeWIF(privKey []byte, addrVersion byte) string {
	wifVersion := WIFVersionMainnet
	if addrVersion != 0x00 {
		wifVersion = WIFVersionTestnet
	}
	payload := make([]byte, 33)
	copy(payload, privKey)
	payload[32] = 0x01 // compressed pubkey flag
	return Base58CheckEncode(byte(wifVersion), payload)
}

// DecodeWIF decodes a WIF-encoded private key string.
// Returns the raw 32-byte private key, whether it's compressed, and the WIF version byte.
func DecodeWIF(wif string) (privKey []byte, compressed bool, wifVersion byte, err error) {
	ver, payload, err := Base58CheckDecode(wif)
	if err != nil {
		return nil, false, 0, fmt.Errorf("invalid WIF: %w", err)
	}
	if ver != WIFVersionMainnet && ver != WIFVersionTestnet {
		return nil, false, 0, fmt.Errorf("invalid WIF version: 0x%02x", ver)
	}
	switch len(payload) {
	case 33:
		if payload[32] != 0x01 {
			return nil, false, 0, fmt.Errorf("invalid WIF compression flag: 0x%02x", payload[32])
		}
		return payload[:32], true, ver, nil
	case 32:
		return payload, false, ver, nil
	default:
		return nil, false, 0, fmt.Errorf("invalid WIF payload length: %d", len(payload))
	}
}

// WIFVersionForAddress returns the WIF version byte corresponding to an address version.
func WIFVersionForAddress(addrVersion byte) byte {
	if addrVersion == 0x00 {
		return WIFVersionMainnet
	}
	return WIFVersionTestnet
}
