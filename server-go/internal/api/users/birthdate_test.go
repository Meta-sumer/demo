package users

import (
	"bytes"
	"net/http/httptest"
	"testing"
	"time"

	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/util"
)

// Port of tests/unit/birth-date.test.ts. A date of birth is stored as a plain
// calendar day, and every failure here is silent if unchecked.
var now = time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)

func TestIsPlausibleBirthDate(t *testing.T) {
	cases := map[string]bool{
		"1995-04-17": true,
		"1996-02-29": true, // a real leap day
		"1995-02-29": false,
		"1995-02-31": false, "1995-04-31": false, "1995-06-31": false,
		"1995-13-01": false, "1995-00-10": false, "1995-05-00": false, "1995-05-32": false,
		"17/04/1995": false, "1995-4-17": false, "1995-04-17T00:00:00Z": false, "": false, "yesterday": false, "19950417": false,
		"2030-01-01": false, // future
		"2008-09-08": false, // one day short of eighteen
		"2008-09-07": true,  // turned eighteen today
		"1850-01-01": false,
	}
	for in, want := range cases {
		if got := util.IsPlausibleBirthDate(in, now); got != want {
			t.Errorf("IsPlausibleBirthDate(%q) = %v, want %v", in, got, want)
		}
	}
	youngest := time.Date(now.Year()-util.MinAgeYears, now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).Format("2006-01-02")
	oldest := time.Date(now.Year()-util.MaxAgeYears, now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).Format("2006-01-02")
	if !util.IsPlausibleBirthDate(youngest, now) || !util.IsPlausibleBirthDate(oldest, now) {
		t.Error("bounds not inclusive")
	}
}

func TestAgeInYears(t *testing.T) {
	d := func(s string) time.Time { v, _ := time.Parse("2006-01-02", s); return v }
	for in, want := range map[string]int{"2000-12-31": 25, "2000-01-01": 26, "2000-09-07": 26, "2000-09-08": 25} {
		if got := util.AgeInYears(d(in), now); got != want {
			t.Errorf("AgeInYears(%s) = %d, want %d", in, got, want)
		}
	}
}

func issues(t *testing.T, body string) []apperr.FieldIssue {
	t.Helper()
	r := httptest.NewRequest("PATCH", "/users/me", bytes.NewBufferString(body))
	_, err := parseUpdateProfile(r, now)
	if err == nil {
		return nil
	}
	e, ok := apperr.As(err)
	if !ok {
		t.Fatalf("unexpected error %v", err)
	}
	return e.Details.([]apperr.FieldIssue)
}

func TestUpdateProfileValidation(t *testing.T) {
	if got := issues(t, `{"dateOfBirth":""}`); got != nil {
		t.Fatalf("empty date should clear: %v", got)
	}
	if got := issues(t, `{"dateOfBirth":"1995-02-31"}`); len(got) != 1 || got[0].Message != "Enter a real date of birth" {
		t.Fatalf("got %v", got)
	}
	if got := issues(t, `{"dateOfBirth":null}`); len(got) != 1 || got[0].Message != "Invalid input" {
		t.Fatalf("got %v", got)
	}
	if got := issues(t, `{"username":"  bad name "}`); len(got) != 1 || got[0].Message != "Use letters, numbers and . _ - # only" {
		t.Fatalf("got %v", got)
	}
	if got := issues(t, `{"username":" ok.name# ","country":"in"}`); got != nil {
		t.Fatalf("got %v", got)
	}
	if got := issues(t, `{"country":"IND"}`); len(got) != 1 || got[0].Message != "String must contain exactly 2 character(s)" {
		t.Fatalf("got %v", got)
	}
}
