package auth

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"hash"
	"net/url"
	"strings"
	"time"
)

// TOTP (RFC 6238) over HOTP (RFC 4226), standard library only.
//
// Authenticator apps (Google Authenticator, Authy, 1Password, ...) agree on
// HMAC-SHA1, 6 digits and a 30 s step, so that is what enrolment issues. The
// primitives take the hash and digit count so the RFC test vectors (8 digits,
// SHA-1/256/512) can be checked against them.

const (
	TOTPDigits = 6
	TOTPPeriod = 30 // seconds
	// TOTPSkew accepts the previous and next step as well, for clock drift
	// and a code typed just as it rolled over.
	TOTPSkew = 1
	// totpSecretBytes is 160 bits, the RFC 4226 recommendation for SHA-1.
	totpSecretBytes = 20
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// HOTP computes the RFC 4226 value for a counter.
func HOTP(key []byte, counter uint64, digits int, h func() hash.Hash) string {
	mac := hmac.New(h, key)
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	// Dynamic truncation (RFC 4226 section 5.3).
	off := sum[len(sum)-1] & 0x0f
	bin := (uint32(sum[off])&0x7f)<<24 | uint32(sum[off+1])<<16 | uint32(sum[off+2])<<8 | uint32(sum[off+3])
	mod := uint32(1)
	for i := 0; i < digits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", digits, bin%mod)
}

// TOTPStep is the time step counter T = floor(unix / period).
func TOTPStep(t time.Time, period int64) uint64 {
	return uint64(t.Unix() / period)
}

// TOTPAt is the code for time t.
func TOTPAt(key []byte, t time.Time, period int64, digits int, h func() hash.Hash) string {
	return HOTP(key, TOTPStep(t, period), digits, h)
}

// verifyTOTP checks a 6-digit SHA-1 code against the steps around now and
// returns the matching step. Every candidate is compared, in constant time.
func verifyTOTP(key []byte, code string, now time.Time) (int64, bool) {
	code = normaliseOTP(code)
	if len(code) != TOTPDigits {
		return 0, false
	}
	step := int64(TOTPStep(now, TOTPPeriod))
	matched, found := int64(0), false
	for d := int64(-TOTPSkew); d <= TOTPSkew; d++ {
		s := step + d
		if s < 0 {
			continue
		}
		want := HOTP(key, uint64(s), TOTPDigits, sha1.New)
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 && !found {
			matched, found = s, true
		}
	}
	return matched, found
}

// normaliseOTP drops the spaces and dashes people type or paste.
func normaliseOTP(code string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '-' || r == '\t' {
			return -1
		}
		return r
	}, strings.TrimSpace(code))
}

// EncodeSecret is the base32 form authenticator apps take (no padding).
func EncodeSecret(key []byte) string { return b32.EncodeToString(key) }

// DecodeSecret accepts the base32 form, ignoring case and spacing.
func DecodeSecret(s string) ([]byte, error) {
	s = strings.ToUpper(strings.ReplaceAll(normaliseOTP(s), "=", ""))
	return b32.DecodeString(s)
}

// OTPAuthURI is the key URI authenticator apps import
// (otpauth://totp/Issuer:account?secret=...&issuer=...).
func OTPAuthURI(issuer, account string, key []byte) string {
	// %20 rather than '+': several apps show a '+' in the issuer literally.
	esc := func(s string) string { return strings.ReplaceAll(url.QueryEscape(s), "+", "%20") }
	label := url.PathEscape(issuer) + ":" + url.PathEscape(account)
	return fmt.Sprintf("otpauth://totp/%s?secret=%s&issuer=%s&algorithm=SHA1&digits=%d&period=%d",
		label, EncodeSecret(key), esc(issuer), TOTPDigits, TOTPPeriod)
}

// groupSecret formats a base32 secret in blocks of four for typing by hand.
func groupSecret(s string) string {
	var b strings.Builder
	for i, r := range s {
		if i > 0 && i%4 == 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
	}
	return b.String()
}
