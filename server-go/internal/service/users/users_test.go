package users

import (
	"regexp"
	"strings"
	"testing"
)

func TestEscapeRegex(t *testing.T) {
	bs := string(rune(92))
	specials := ".*+?^${}()|[]/" + bs
	in := "a" + specials + "z"
	var want strings.Builder
	want.WriteString("a")
	for _, c := range specials {
		want.WriteString(bs + string(c))
	}
	want.WriteString("z")
	out := EscapeRegex(in)
	if out != want.String() {
		t.Fatalf("got %q want %q", out, want.String())
	}
	if !regexp.MustCompile("^" + out + "$").MatchString(in) {
		t.Fatal("escaped pattern does not match the literal")
	}
}
