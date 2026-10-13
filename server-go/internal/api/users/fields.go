package users

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf16"

	"binarytrading/internal/platform/httpx"
)

// fields reads a JSON object key by key with Zod's semantics: an absent key
// is "Required" (unless optional), any other JSON type is
// "Expected <type>, received <type>", and every issue is collected.
type fields struct {
	m map[string]json.RawMessage
	v *httpx.V
}

func decodeFields(r *http.Request) (*fields, error) {
	m := map[string]json.RawMessage{}
	if err := httpx.DecodeBody(r, &m); err != nil {
		return nil, err
	}
	return &fields{m: m, v: httpx.NewV()}, nil
}

func jsonKind(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" {
		return "undefined"
	}
	switch s[0] {
	case '"':
		return "string"
	case '{':
		return "object"
	case '[':
		return "array"
	case 't', 'f':
		return "boolean"
	case 'n':
		return "null"
	}
	return "number"
}

// str returns the string and true when the key holds a string.
func (f *fields) str(key string, required bool) (string, bool) {
	raw, ok := f.m[key]
	if !ok {
		if required {
			f.v.Add(key, "Required")
		}
		return "", false
	}
	if k := jsonKind(raw); k != "string" {
		f.v.Add(key, "Expected string, received "+k)
		return "", false
	}
	var s string
	_ = json.Unmarshal(raw, &s)
	return s, true
}

func (f *fields) boolean(key string) (bool, bool) {
	raw, ok := f.m[key]
	if !ok {
		return false, false
	}
	if k := jsonKind(raw); k != "boolean" {
		f.v.Add(key, "Expected boolean, received "+k)
		return false, false
	}
	var b bool
	_ = json.Unmarshal(raw, &b)
	return b, true
}

// jsLen is JavaScript's String.length (UTF-16 code units), which Zod uses.
func jsLen(s string) int { return len(utf16.Encode([]rune(s))) }

func (f *fields) min(key, s string, n int) {
	if jsLen(s) < n {
		f.v.Add(key, fmt.Sprintf("String must contain at least %d character(s)", n))
	}
}

func (f *fields) max(key, s string, n int) {
	if jsLen(s) > n {
		f.v.Add(key, fmt.Sprintf("String must contain at most %d character(s)", n))
	}
}

func (f *fields) length(key, s string, n int) {
	if jsLen(s) != n {
		f.v.Add(key, fmt.Sprintf("String must contain exactly %d character(s)", n))
	}
}

func (f *fields) regex(key, s string, re *regexp.Regexp, msg string) {
	if !re.MatchString(s) {
		f.v.Add(key, msg)
	}
}

func (f *fields) err() error { return f.v.Err("request body") }
