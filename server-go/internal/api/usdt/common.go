// Package usdt is the HTTP surface of the USDT wallet (BeeX): the
// customer's /wallet/usdt routes, the operators' /admin/usdt routes and the
// BeeX webhook. Routes parse, authorise, audit and delegate; every rule
// about money lives in service/usdt.
package usdt

import (
	"bytes"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/httpx"
)

// maxMicro bounds any amount a request may carry: 1e15 micro-USDT is a
// billion USDT, far above every limit and well inside float64's exact range.
const maxMicro int64 = 1_000_000_000_000_000

// fields is a decoded JSON object whose values are read one by one, so an
// amount is taken from its exact digits rather than through a float64.
type fields struct {
	raw map[string]json.RawMessage
	v   *httpx.V
}

func readFields(r *http.Request) (*fields, error) {
	raw := map[string]json.RawMessage{}
	if err := httpx.DecodeBody(r, &raw); err != nil {
		return nil, err
	}
	return &fields{raw: raw, v: httpx.NewV()}, nil
}

func (f *fields) err() error { return f.v.Err("request body") }

func (f *fields) present(key string) bool {
	raw, ok := f.raw[key]
	return ok && strings.TrimSpace(string(raw)) != "null"
}

// str reads a required (or optional) string of min..max characters, trimmed.
func (f *fields) str(key string, min, max int, required bool) *string {
	raw, ok := f.raw[key]
	if !ok || strings.TrimSpace(string(raw)) == "null" {
		if required {
			f.v.Add(key, "Required")
		}
		return nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		f.v.Add(key, "Expected string, received "+kind(raw))
		return nil
	}
	s = strings.TrimSpace(s)
	f.v.Str(key, s, min, max)
	return &s
}

// micro reads a required integer amount of micro-USDT (at most maxMicro).
func (f *fields) micro(key string) int64 {
	raw, ok := f.raw[key]
	if !ok {
		f.v.Add(key, "Required")
		return 0
	}
	text := strings.TrimSpace(string(raw))
	if kind(raw) != "number" {
		f.v.Add(key, "Expected number, received "+kind(raw))
		return 0
	}
	n, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		// A decimal, an exponent or an out-of-range value: amounts are whole
		// micro-USDT.
		f.v.Add(key, "Expected integer, received float")
		return 0
	}
	// Zero and negative amounts are the service's INVALID_AMOUNT, which the
	// clients show as it comes.
	f.v.Int(key, n, -maxMicro, maxMicro)
	return n
}

func kind(raw []byte) string {
	s := bytes.TrimSpace(raw)
	if len(s) == 0 {
		return "undefined"
	}
	switch s[0] {
	case '{':
		return "object"
	case '[':
		return "array"
	case '"':
		return "string"
	case 't', 'f':
		return "boolean"
	case 'n':
		return "null"
	}
	return "number"
}

var idempotencyKeyRe = regexp.MustCompile(`^[A-Za-z0-9._:-]{8,128}$`)

// idempotencyKey is the Idempotency-Key header every money request carries:
// the same key with the same body is acted on once.
func idempotencyKey(r *http.Request) (string, error) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		return "", apperr.Validation("Invalid request headers", []apperr.FieldIssue{{
			Path: "idempotency-key", Message: "An Idempotency-Key header is required for this request",
		}})
	}
	if !idempotencyKeyRe.MatchString(key) {
		return "", apperr.Validation("Invalid request headers", []apperr.FieldIssue{{
			Path: "idempotency-key", Message: "8-128 characters: letters, digits, '.', '_', ':' or '-'",
		}})
	}
	return key, nil
}

// auditDoc turns a view into plain JSON values for the audit row (struct
// views carry json tags, not bson ones).
func auditDoc(v any) any {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var out any
	if err := dec.Decode(&out); err != nil {
		return nil
	}
	return plain(out)
}

func plain(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			t[k] = plain(x)
		}
		return t
	case []any:
		for i, x := range t {
			t[i] = plain(x)
		}
		return t
	case json.Number:
		if n, err := t.Int64(); err == nil {
			return n
		}
		f, _ := t.Float64()
		return f
	}
	return v
}
