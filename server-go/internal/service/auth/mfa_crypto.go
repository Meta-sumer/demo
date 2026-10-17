package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"

	"binarytrading/internal/config"
)

// TOTP secrets are stored encrypted (AES-256-GCM), bound to their user id as
// additional data so a secret copied onto another account does not decrypt.
// The key comes from MFA_ENCRYPTION_KEY, or is derived from JWT_SECRET when
// that is unset.

const sealedPrefix = "v1:"

func mfaKey() []byte {
	src := ""
	if config.E != nil {
		src = config.E.MFAEncryptionKey
		if src == "" {
			src = "jwt:" + config.E.JWTSecret
		}
	}
	sum := sha256.Sum256([]byte("binary-trading/admin-mfa/v1\x00" + src))
	return sum[:]
}

func mfaAEAD() (cipher.AEAD, error) {
	block, err := aes.NewCipher(mfaKey())
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func sealSecret(userID string, secret []byte) (string, error) {
	aead, err := mfaAEAD()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := aead.Seal(nonce, nonce, secret, []byte(userID))
	return sealedPrefix + base64.RawStdEncoding.EncodeToString(out), nil
}

var errSecretUnreadable = errors.New("stored two-factor secret cannot be decrypted")

func openSecret(userID, sealed string) ([]byte, error) {
	if !strings.HasPrefix(sealed, sealedPrefix) {
		return nil, errSecretUnreadable
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(sealed, sealedPrefix))
	if err != nil {
		return nil, errSecretUnreadable
	}
	aead, err := mfaAEAD()
	if err != nil {
		return nil, err
	}
	if len(raw) < aead.NonceSize() {
		return nil, errSecretUnreadable
	}
	plain, err := aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], []byte(userID))
	if err != nil {
		return nil, errSecretUnreadable
	}
	return plain, nil
}
