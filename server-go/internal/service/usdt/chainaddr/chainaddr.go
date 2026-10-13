// Package chainaddr validates the withdrawal addresses of the chains USDT is
// paid out on. A mistyped address that still looked valid would send real
// money nowhere, so each format's checksum is verified, not just its shape.
package chainaddr

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math/big"
	"strings"

	"golang.org/x/crypto/sha3"
)

var (
	ErrFormat   = errors.New("address has the wrong format")
	ErrChecksum = errors.New("address checksum does not match")
	ErrZero     = errors.New("the zero address cannot receive funds")
	ErrChain    = errors.New("unknown chain")
)

// Validate checks an address for a chain ("TRON" or "BSC").
func Validate(chain, address string) error {
	switch strings.ToUpper(chain) {
	case "TRON":
		return ValidateTron(address)
	case "BSC":
		return ValidateEVM(address)
	}
	return ErrChain
}

// ── TRON ────────────────────────────────────────────────────────────────

const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

var base58Index = func() [256]int {
	var idx [256]int
	for i := range idx {
		idx[i] = -1
	}
	for i := 0; i < len(base58Alphabet); i++ {
		idx[base58Alphabet[i]] = i
	}
	return idx
}()

// tronPrefix is the mainnet address version byte.
const tronPrefix = 0x41

// ValidateTron: 34 base58 characters decoding to 25 bytes - 0x41, a 20-byte
// account id and the first 4 bytes of sha256(sha256(first 21 bytes)).
func ValidateTron(address string) error {
	if len(address) != 34 || address[0] != 'T' {
		return ErrFormat
	}
	raw, err := base58Decode(address)
	if err != nil || len(raw) != 25 || raw[0] != tronPrefix {
		return ErrFormat
	}
	if !checksumOK(raw[:21], raw[21:]) {
		return ErrChecksum
	}
	if isZero(raw[1:21]) {
		return ErrZero
	}
	return nil
}

func checksumOK(payload, sum []byte) bool {
	h1 := sha256.Sum256(payload)
	h2 := sha256.Sum256(h1[:])
	return string(h2[:4]) == string(sum)
}

func base58Decode(s string) ([]byte, error) {
	n := new(big.Int)
	radix := big.NewInt(58)
	for i := 0; i < len(s); i++ {
		v := base58Index[s[i]]
		if v < 0 {
			return nil, ErrFormat
		}
		n.Mul(n, radix)
		n.Add(n, big.NewInt(int64(v)))
	}
	out := n.Bytes()
	// Each leading '1' encodes a leading zero byte.
	zeros := 0
	for zeros < len(s) && s[zeros] == '1' {
		zeros++
	}
	return append(make([]byte, zeros), out...), nil
}

func base58Encode(b []byte) string {
	n := new(big.Int).SetBytes(b)
	radix := big.NewInt(58)
	mod := new(big.Int)
	var out []byte
	for n.Sign() > 0 {
		n.DivMod(n, radix, mod)
		out = append(out, base58Alphabet[mod.Int64()])
	}
	for i := 0; i < len(b) && b[i] == 0; i++ {
		out = append(out, '1')
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}

// TronFromAccountID encodes a 20-byte account id as a TRON address (used by
// the mock provider and tests).
func TronFromAccountID(id [20]byte) string {
	payload := append([]byte{tronPrefix}, id[:]...)
	h1 := sha256.Sum256(payload)
	h2 := sha256.Sum256(h1[:])
	return base58Encode(append(payload, h2[:4]...))
}

// ── BNB Smart Chain (EVM) ───────────────────────────────────────────────

// ValidateEVM: "0x" + 40 hex digits. All-lowercase or all-uppercase hex
// carries no checksum; mixed case must match EIP-55 (keccak-256).
func ValidateEVM(address string) error {
	if len(address) != 42 || !strings.HasPrefix(address, "0x") {
		return ErrFormat
	}
	body := address[2:]
	if _, err := hex.DecodeString(body); err != nil {
		return ErrFormat
	}
	if strings.Trim(strings.ToLower(body), "0") == "" {
		return ErrZero
	}
	if body == strings.ToLower(body) || body == strings.ToUpper(body) {
		return nil
	}
	if ChecksumEVM(body) != "0x"+body {
		return ErrChecksum
	}
	return nil
}

// ChecksumEVM returns the EIP-55 form of a 40-hex-digit address (with or
// without 0x).
func ChecksumEVM(address string) string {
	lower := strings.ToLower(strings.TrimPrefix(address, "0x"))
	h := sha3.NewLegacyKeccak256()
	h.Write([]byte(lower))
	sum := hex.EncodeToString(h.Sum(nil))
	out := []byte(lower)
	for i := range out {
		if out[i] >= 'a' && out[i] <= 'f' && sum[i] >= '8' {
			out[i] -= 'a' - 'A'
		}
	}
	return "0x" + string(out)
}

func isZero(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}
