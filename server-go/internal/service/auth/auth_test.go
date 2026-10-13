package auth

import (
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// Hashes produced by bcryptjs (the Node server) must verify here.
func TestComparesBcryptjsHashes(t *testing.T) {
	if !ComparePassword("Passw0rd!", "$2a$04$B.JUlMCYQOiWKGHekckTvOiXoOiQ/twlSSNTmfAJW9qmBlOpawJZG") {
		t.Fatal("bcryptjs hash did not verify")
	}
	if ComparePassword("Passw0rd?", "$2a$04$B.JUlMCYQOiWKGHekckTvOiXoOiQ/twlSSNTmfAJW9qmBlOpawJZG") {
		t.Fatal("wrong password verified")
	}
	// bcryptjs only uses the first 72 bytes of a longer password.
	long := "A1a" + strings.Repeat("x", 100)
	if !ComparePassword(long, "$2a$04$xRPYw5n4l0GVERZn2B0YgeZssX26pDDBDdYYpk6sMp4a0YsMvimxm") {
		t.Fatal("long bcryptjs hash did not verify")
	}
	// $2b$ prefixes (newer bcryptjs) are accepted too.
	if !ComparePassword("Passw0rd!", "$2b$04$B.JUlMCYQOiWKGHekckTvOiXoOiQ/twlSSNTmfAJW9qmBlOpawJZG") {
		t.Fatal("$2b$ hash did not verify")
	}
}

func TestDummyHashIsWellFormed(t *testing.T) {
	cost, err := bcrypt.Cost([]byte(dummyHash))
	if err != nil || cost != 12 {
		t.Fatalf("dummy hash cost=%d err=%v", cost, err)
	}
}

func TestHashLongPassword(t *testing.T) {
	h, err := bcrypt.GenerateFromPassword(bcryptKey(strings.Repeat("Ab1", 40)), 4)
	if err != nil {
		t.Fatal(err)
	}
	if !ComparePassword(strings.Repeat("Ab1", 40), string(h)) {
		t.Fatal("round trip failed")
	}
}
