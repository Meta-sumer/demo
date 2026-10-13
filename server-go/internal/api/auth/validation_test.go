package auth

import (
	"bytes"
	"net/http/httptest"
	"testing"

	"binarytrading/internal/platform/apperr"
)

func details(t *testing.T, err error) []apperr.FieldIssue {
	t.Helper()
	if err == nil {
		return nil
	}
	e, ok := apperr.As(err)
	if !ok || e.StatusCode != 422 {
		t.Fatalf("unexpected error %v", err)
	}
	return e.Details.([]apperr.FieldIssue)
}

func TestRegisterValidation(t *testing.T) {
	r := httptest.NewRequest("POST", "/auth/register", bytes.NewBufferString(
		`{"email":"Trader@Example.COM","username":"trader_1","password":"Secret123","country":"in","fullName":" Jo Doe "}`))
	in, err := parseRegister(r)
	if err != nil {
		t.Fatal(err)
	}
	if in.Email != "trader@example.com" || *in.Country != "IN" || *in.FullName != "Jo Doe" {
		t.Fatalf("transforms not applied: %+v", in)
	}

	r = httptest.NewRequest("POST", "/auth/register", bytes.NewBufferString(`{"email":"nope","username":"a b","password":"short"}`))
	_, err = parseRegister(r)
	got := details(t, err)
	want := []apperr.FieldIssue{
		{Path: "email", Message: "Invalid email"},
		{Path: "username", Message: "Username may only contain letters, digits and underscores"},
		{Path: "password", Message: "Password must be at least 8 characters"},
		{Path: "password", Message: "Password must contain an uppercase letter"},
		{Path: "password", Message: "Password must contain a digit"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("issue %d: got %v want %v", i, got[i], want[i])
		}
	}
}

func TestLoginValidation(t *testing.T) {
	r := httptest.NewRequest("POST", "/auth/login", bytes.NewBufferString(`{"email":null}`))
	_, err := parseLogin(r)
	got := details(t, err)
	if len(got) != 2 || got[0].Message != "Expected string, received null" || got[1].Message != "Required" {
		t.Fatalf("got %v", got)
	}
}

func TestZodEmail(t *testing.T) {
	for s, want := range map[string]bool{
		"a@b.co": true, "first.last+tag@sub.example.org": true, "o'neil@x.io": true,
		".a@b.co": false, "a..b@c.co": false, "a.@b.co": false, "a@b": false, "a@b.c": false, " a@b.co": false,
	} {
		if isZodEmail(s) != want {
			t.Errorf("isZodEmail(%q) != %v", s, want)
		}
	}
}
