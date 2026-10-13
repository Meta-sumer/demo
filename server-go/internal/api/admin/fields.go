package admin

import (
	"encoding/json"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/platform/httpx"
)

// A small Zod stand-in for the admin bodies: fields are read from the raw
// JSON so a wrong type, a null and an absent key are told apart, and every
// issue is collected with Zod's wording and dotted path.

type body struct {
	f map[string]json.RawMessage
	v *httpx.V
}

func readBody(r *http.Request) (*body, error) {
	f := map[string]json.RawMessage{}
	if err := httpx.DecodeBody(r, &f); err != nil {
		return nil, err
	}
	return &body{f: f, v: httpx.NewV()}, nil
}

func (b *body) err() error { return b.v.Err("request body") }

func (b *body) get(key string) (json.RawMessage, bool) {
	raw, ok := b.f[key]
	return raw, ok
}

func kindOf(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" {
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

func isNull(raw json.RawMessage) bool { return kindOf(raw) == "null" }

func jsLen(s string) int { return len(utf16.Encode([]rune(s))) }

func fmtNum(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

func typeIssue(v *httpx.V, path, expected string, raw json.RawMessage) {
	v.Add(path, "Expected "+expected+", received "+kindOf(raw))
}

// ── element checks (path-addressed, for nested values too) ──────────────

type numRule struct {
	Int      bool
	Positive bool
	Min, Max *float64
}

func f(v float64) *float64 { return &v }

func checkNum(v *httpx.V, path string, raw json.RawMessage, rule numRule) (float64, bool) {
	if kindOf(raw) != "number" {
		typeIssue(v, path, "number", raw)
		return 0, false
	}
	var n float64
	if err := json.Unmarshal(raw, &n); err != nil {
		typeIssue(v, path, "number", raw)
		return 0, false
	}
	if rule.Int && n != math.Trunc(n) {
		v.Add(path, "Expected integer, received float")
	}
	if rule.Positive && !(n > 0) {
		v.Add(path, "Number must be greater than 0")
	}
	if rule.Min != nil && n < *rule.Min {
		v.Add(path, "Number must be greater than or equal to "+fmtNum(*rule.Min))
	}
	if rule.Max != nil && n > *rule.Max {
		v.Add(path, "Number must be less than or equal to "+fmtNum(*rule.Max))
	}
	return n, true
}

func checkStr(v *httpx.V, path string, raw json.RawMessage, min, max int) (string, bool) {
	if kindOf(raw) != "string" {
		typeIssue(v, path, "string", raw)
		return "", false
	}
	var s string
	_ = json.Unmarshal(raw, &s)
	n := jsLen(s)
	if n < min {
		v.Add(path, "String must contain at least "+strconv.Itoa(min)+" character(s)")
	}
	if max > 0 && n > max {
		v.Add(path, "String must contain at most "+strconv.Itoa(max)+" character(s)")
	}
	return s, true
}

func checkBool(v *httpx.V, path string, raw json.RawMessage) (bool, bool) {
	if kindOf(raw) != "boolean" {
		typeIssue(v, path, "boolean", raw)
		return false, false
	}
	return strings.TrimSpace(string(raw)) == "true", true
}

func quoteAll(allowed []string) string {
	q := make([]string, len(allowed))
	for i, a := range allowed {
		q[i] = "'" + a + "'"
	}
	return strings.Join(q, " | ")
}

func checkEnum(v *httpx.V, path string, raw json.RawMessage, allowed ...string) (string, bool) {
	if kindOf(raw) != "string" {
		v.Add(path, "Expected "+quoteAll(allowed)+", received "+kindOf(raw))
		return "", false
	}
	var s string
	_ = json.Unmarshal(raw, &s)
	for _, a := range allowed {
		if a == s {
			return s, true
		}
	}
	v.Add(path, "Invalid enum value. Expected "+quoteAll(allowed)+", received '"+s+"'")
	return "", false
}

// checkArray validates the container and minimum length and returns the
// elements for per-item checks.
func checkArray(v *httpx.V, path string, raw json.RawMessage, min int) ([]json.RawMessage, bool) {
	if kindOf(raw) != "array" {
		typeIssue(v, path, "array", raw)
		return nil, false
	}
	var items []json.RawMessage
	_ = json.Unmarshal(raw, &items)
	if len(items) < min {
		v.Add(path, "Array must contain at least "+strconv.Itoa(min)+" element(s)")
	}
	return items, true
}

func checkObject(v *httpx.V, path string, raw json.RawMessage) (map[string]json.RawMessage, bool) {
	if kindOf(raw) != "object" {
		typeIssue(v, path, "object", raw)
		return nil, false
	}
	m := map[string]json.RawMessage{}
	_ = json.Unmarshal(raw, &m)
	return m, true
}

func sub(path string, key any) string {
	switch k := key.(type) {
	case int:
		return path + "." + strconv.Itoa(k)
	case string:
		return path + "." + k
	}
	return path
}

// ── top-level key readers ───────────────────────────────────────────────

func (b *body) reqStr(key string, min, max int) (string, bool) {
	raw, ok := b.get(key)
	if !ok {
		b.v.Add(key, "Required")
		return "", false
	}
	return checkStr(b.v, key, raw, min, max)
}

func (b *body) optStr(key string, min, max int) *string {
	raw, ok := b.get(key)
	if !ok {
		return nil
	}
	s, ok := checkStr(b.v, key, raw, min, max)
	if !ok {
		return nil
	}
	return &s
}

func (b *body) reqNum(key string, rule numRule) (float64, bool) {
	raw, ok := b.get(key)
	if !ok {
		b.v.Add(key, "Required")
		return 0, false
	}
	return checkNum(b.v, key, raw, rule)
}

func (b *body) optNum(key string, rule numRule) *float64 {
	raw, ok := b.get(key)
	if !ok {
		return nil
	}
	n, ok := checkNum(b.v, key, raw, rule)
	if !ok {
		return nil
	}
	return &n
}

func (b *body) optInt(key string, rule numRule) *int64 {
	rule.Int = true
	n := b.optNum(key, rule)
	if n == nil {
		return nil
	}
	i := int64(*n)
	return &i
}

// nullableInt is `.nullable().optional()`: set reports the key was present,
// val is nil for an explicit null.
func (b *body) nullableInt(key string, rule numRule) (set bool, val *int64) {
	raw, ok := b.get(key)
	if !ok {
		return false, nil
	}
	if isNull(raw) {
		return true, nil
	}
	rule.Int = true
	n, ok := checkNum(b.v, key, raw, rule)
	if !ok {
		return false, nil
	}
	i := int64(n)
	return true, &i
}

func (b *body) nullableNum(key string, rule numRule) (set bool, val *float64) {
	raw, ok := b.get(key)
	if !ok {
		return false, nil
	}
	if isNull(raw) {
		return true, nil
	}
	n, ok := checkNum(b.v, key, raw, rule)
	if !ok {
		return false, nil
	}
	return true, &n
}

func (b *body) reqBool(key string) (bool, bool) {
	raw, ok := b.get(key)
	if !ok {
		b.v.Add(key, "Required")
		return false, false
	}
	return checkBool(b.v, key, raw)
}

func (b *body) optBool(key string) *bool {
	raw, ok := b.get(key)
	if !ok {
		return nil
	}
	x, ok := checkBool(b.v, key, raw)
	if !ok {
		return nil
	}
	return &x
}

func (b *body) reqEnum(key string, allowed ...string) (string, bool) {
	raw, ok := b.get(key)
	if !ok {
		b.v.Add(key, "Required")
		return "", false
	}
	return checkEnum(b.v, key, raw, allowed...)
}

func (b *body) optEnum(key string, allowed ...string) *string {
	raw, ok := b.get(key)
	if !ok {
		return nil
	}
	s, ok := checkEnum(b.v, key, raw, allowed...)
	if !ok {
		return nil
	}
	return &s
}

// ── query helpers ───────────────────────────────────────────────────────

// optQueryStr is `z.string().min(min).max(max).optional()` on a query key.
func optQueryStr(q *httpx.Q, key string, min, max int) *string {
	s := q.OptStr(key)
	if s == nil {
		return nil
	}
	n := jsLen(*s)
	if n < min {
		q.V.Add(key, "String must contain at least "+strconv.Itoa(min)+" character(s)")
	}
	if max > 0 && n > max {
		q.V.Add(key, "String must contain at most "+strconv.Itoa(max)+" character(s)")
	}
	return s
}

// dateRange is dateRangeSchema (from/to coerced dates).
func dateRange(q *httpx.Q) (from, to *time.Time) {
	return q.OptDate("from"), q.OptDate("to")
}

func parseHex(s string) (bson.ObjectID, bool) {
	if !hexID.MatchString(s) {
		return bson.NilObjectID, false
	}
	id, err := bson.ObjectIDFromHex(s)
	return id, err == nil
}

var hexID = regexp.MustCompile(`^[a-fA-F0-9]{24}$`)

// checkNativeEnum is z.nativeEnum over string values: unlike z.enum it also
// accepts a number as the right type, then rejects it as an enum value.
func checkNativeEnum(v *httpx.V, path string, raw json.RawMessage, allowed ...string) (string, bool) {
	if kindOf(raw) == "number" {
		v.Add(path, "Invalid enum value. Expected "+quoteAll(allowed)+", received '"+strings.TrimSpace(string(raw))+"'")
		return "", false
	}
	return checkEnum(v, path, raw, allowed...)
}

func (b *body) optNativeEnum(key string, allowed ...string) *string {
	raw, ok := b.get(key)
	if !ok {
		return nil
	}
	s, ok := checkNativeEnum(b.v, key, raw, allowed...)
	if !ok {
		return nil
	}
	return &s
}
