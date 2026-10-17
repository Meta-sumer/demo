package auth

import (
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"hash"
	"strings"
	"testing"
	"time"
)

// RFC 4226 appendix D: HOTP, secret "12345678901234567890", 6 digits.
func TestHOTPRFC4226Vectors(t *testing.T) {
	key := []byte("12345678901234567890")
	want := []string{"755224", "287082", "359152", "969429", "338314", "254676", "287922", "162583", "399871", "520489"}
	for counter, w := range want {
		if got := HOTP(key, uint64(counter), 6, sha1.New); got != w {
			t.Errorf("counter %d: %s, want %s", counter, got, w)
		}
	}
}

// RFC 6238 appendix B: TOTP, 30 s step, 8 digits, for SHA-1, SHA-256 and
// SHA-512 with their 20-, 32- and 64-byte seeds.
func TestTOTPRFC6238Vectors(t *testing.T) {
	seed1 := []byte("12345678901234567890")
	seed256 := []byte("12345678901234567890123456789012")
	seed512 := []byte("1234567890123456789012345678901234567890123456789012345678901234")
	type algo struct {
		name string
		key  []byte
		h    func() hash.Hash
	}
	algos := []algo{{"SHA1", seed1, sha1.New}, {"SHA256", seed256, sha256.New}, {"SHA512", seed512, sha512.New}}
	vectors := []struct {
		unix int64
		want [3]string // SHA1, SHA256, SHA512
	}{
		{59, [3]string{"94287082", "46119246", "90693936"}},
		{1111111109, [3]string{"07081804", "68084774", "25091201"}},
		{1111111111, [3]string{"14050471", "67062674", "99943326"}},
		{1234567890, [3]string{"89005924", "91819424", "93441116"}},
		{2000000000, [3]string{"69279037", "90698825", "38618901"}},
		{20000000000, [3]string{"65353130", "77737706", "47863826"}},
	}
	for _, v := range vectors {
		for i, a := range algos {
			if got := TOTPAt(a.key, time.Unix(v.unix, 0), 30, 8, a.h); got != v.want[i] {
				t.Errorf("T=%d %s: %s, want %s", v.unix, a.name, got, v.want[i])
			}
		}
	}
}

func TestVerifyTOTPWindowAndFormat(t *testing.T) {
	key := []byte("12345678901234567890")
	now := time.Unix(1_800_000_000, 0)
	code := TOTPAt(key, now, TOTPPeriod, TOTPDigits, sha1.New)
	step, ok := verifyTOTP(key, code, now)
	if !ok || step != int64(TOTPStep(now, TOTPPeriod)) {
		t.Fatalf("current code rejected (%d %v)", step, ok)
	}
	// One step of clock drift either way is accepted, two are not.
	if _, ok := verifyTOTP(key, code, now.Add(30*time.Second)); !ok {
		t.Fatal("previous step rejected")
	}
	if _, ok := verifyTOTP(key, code, now.Add(-30*time.Second)); !ok {
		t.Fatal("next step rejected")
	}
	if _, ok := verifyTOTP(key, code, now.Add(61*time.Second)); ok {
		t.Fatal("a code two steps old was accepted")
	}
	// Spaces are tolerated; wrong length or letters are not.
	if _, ok := verifyTOTP(key, code[:3]+" "+code[3:], now); !ok {
		t.Fatal("spaced code rejected")
	}
	for _, bad := range []string{"", "12345", "1234567", "abcdef"} {
		if _, ok := verifyTOTP(key, bad, now); ok {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestSecretEncodingAndURI(t *testing.T) {
	key := []byte("12345678901234567890")
	enc := EncodeSecret(key)
	if enc != "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ" {
		t.Fatalf("base32 %s", enc)
	}
	back, err := DecodeSecret(strings.ToLower(groupSecret(enc)))
	if err != nil || string(back) != string(key) {
		t.Fatalf("round trip %q %v", back, err)
	}
	uri := OTPAuthURI("Binary Trading Admin", "ops@example.com", key)
	want := "otpauth://totp/Binary%20Trading%20Admin:ops@example.com?secret=" + enc +
		"&issuer=Binary%20Trading%20Admin&algorithm=SHA1&digits=6&period=30"
	if uri != want {
		t.Fatalf("uri\n got %s\nwant %s", uri, want)
	}
}

func TestSealedSecretIsBoundToItsUser(t *testing.T) {
	sealed, err := sealSecret("user-a", []byte("top secret"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, "top secret") {
		t.Fatal("secret stored in clear")
	}
	plain, err := openSecret("user-a", sealed)
	if err != nil || string(plain) != "top secret" {
		t.Fatalf("open: %q %v", plain, err)
	}
	if _, err := openSecret("user-b", sealed); err == nil {
		t.Fatal("a secret copied to another account decrypted")
	}
}

func TestRecoveryCodesFormat(t *testing.T) {
	codes, hashed, err := newRecoveryCodes([12]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	if len(codes) != recoveryCodeCount || len(hashed) != recoveryCodeCount {
		t.Fatalf("%d codes", len(codes))
	}
	seen := map[string]bool{}
	for i, c := range codes {
		if len(c) != 19 || strings.Count(c, "-") != 3 {
			t.Errorf("code %q", c)
		}
		if seen[c] {
			t.Errorf("duplicate %q", c)
		}
		seen[c] = true
		if hashed[i].Hash != hashRecoveryCode([12]byte{1}, normaliseRecoveryCode(strings.ToLower(c))) {
			t.Errorf("code %d does not hash to its stored value", i)
		}
		if strings.Contains(hashed[i].Hash, strings.ReplaceAll(c, "-", "")) {
			t.Error("recovery code stored in clear")
		}
	}
}
