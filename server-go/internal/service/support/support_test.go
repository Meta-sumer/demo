package support

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf16"

	"binarytrading/internal/platform/apperr"
)

func TestPreview(t *testing.T) {
	short := strings.Repeat("a", 140)
	if preview(short) != short {
		t.Fatal("140 characters must not be truncated")
	}
	long := strings.Repeat("b", 141)
	p := preview(long)
	if p != strings.Repeat("b", 139)+"…" || len(utf16.Encode([]rune(p))) != 140 {
		t.Fatalf("preview = %q", p)
	}
	// Astral characters count as two UTF-16 units, as in JavaScript.
	emoji := strings.Repeat("😀", 71)
	if got := len(utf16.Encode([]rune(preview(emoji)))); got != 140 {
		t.Fatalf("emoji preview length = %d", got)
	}
}

func TestValidateBody(t *testing.T) {
	if b, err := validateBody("  hi \n"); err != nil || b != "hi" {
		t.Fatalf("got %q %v", b, err)
	}
	if _, err := validateBody("   "); !apperr.HasCode(err, apperr.ValidationError) {
		t.Error("empty body accepted")
	}
	if _, err := validateBody(strings.Repeat("x", 2001)); !apperr.HasCode(err, apperr.ValidationError) {
		t.Error("oversized body accepted")
	}
	if _, err := validateBody(strings.Repeat("x", 2000)); err != nil {
		t.Error(err)
	}
}

func TestDisplayName(t *testing.T) {
	full := "  Asha Rao "
	blank := "  "
	if displayName(&userNames{Username: "asha", FullName: &full}, "Support") != "Asha Rao" {
		t.Error("full name not preferred")
	}
	if displayName(&userNames{Username: "asha", FullName: &blank}, "Support") != "asha" {
		t.Error("blank full name not skipped")
	}
	if displayName(nil, "Customer") != "Customer" {
		t.Error("fallback not used")
	}
}

func TestTypingPayload(t *testing.T) {
	b, _ := json.Marshal(TypingSignal{ConversationID: "c", Side: "USER"})
	if string(b) != `{"conversationId":"c","side":"USER"}` {
		t.Fatalf("user typing = %s", b)
	}
	b, _ = json.Marshal(TypingSignal{ConversationID: "c", Side: "AGENT", Name: "Asha"})
	if string(b) != `{"conversationId":"c","side":"AGENT","name":"Asha"}` {
		t.Fatalf("agent typing = %s", b)
	}
}
