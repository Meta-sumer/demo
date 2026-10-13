package httpx

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/mail"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/platform/apperr"
)

// Validation mirrors the Node server's Zod layer: nothing downstream reads a
// raw request. Issues are collected and returned together as a 422 with
// details [{path, message}]; messages follow Zod's wording.

// V collects validation issues.
type V struct{ issues []apperr.FieldIssue }

func NewV() *V { return &V{} }

func (v *V) Add(path, msg string) {
	v.issues = append(v.issues, apperr.FieldIssue{Path: path, Message: msg})
}

// Check adds msg when ok is false and returns ok.
func (v *V) Check(ok bool, path, msg string) bool {
	if !ok {
		v.Add(path, msg)
	}
	return ok
}

// Required adds "Required" when the value is absent.
func (v *V) Required(path string, present bool) bool { return v.Check(present, path, "Required") }

// Str checks length in characters; max 0 means unbounded.
func (v *V) Str(path, s string, min, max int) {
	n := utf8.RuneCountInString(s)
	if n < min {
		v.Add(path, fmt.Sprintf("String must contain at least %d character(s)", min))
	}
	if max > 0 && n > max {
		v.Add(path, fmt.Sprintf("String must contain at most %d character(s)", max))
	}
}

// Int checks an integer range (inclusive).
func (v *V) Int(path string, n, min, max int64) {
	if n < min {
		v.Add(path, fmt.Sprintf("Number must be greater than or equal to %d", min))
	}
	if n > max {
		v.Add(path, fmt.Sprintf("Number must be less than or equal to %d", max))
	}
}

// Positive requires n > 0.
func (v *V) Positive(path string, n float64) { v.Check(n > 0, path, "Number must be greater than 0") }

// Float checks a number range (inclusive).
func (v *V) Float(path string, f, min, max float64) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		v.Add(path, "Expected number, received nan")
		return
	}
	if f < min {
		v.Add(path, "Number must be greater than or equal to "+fmtNum(min))
	}
	if f > max {
		v.Add(path, "Number must be less than or equal to "+fmtNum(max))
	}
}

// IsInt requires a whole number (for fields decoded as float64).
func (v *V) IsInt(path string, f float64) bool {
	return v.Check(f == math.Trunc(f) && !math.IsInf(f, 0), path, "Expected integer, received float")
}

func (v *V) Enum(path, s string, allowed ...string) bool {
	for _, a := range allowed {
		if s == a {
			return true
		}
	}
	quoted := make([]string, len(allowed))
	for i, a := range allowed {
		quoted[i] = "'" + a + "'"
	}
	v.Add(path, "Invalid enum value. Expected "+strings.Join(quoted, " | ")+", received '"+s+"'")
	return false
}

func (v *V) Email(path, s string) bool {
	_, err := mail.ParseAddress(s)
	ok := err == nil && !strings.ContainsAny(s, " <>") && strings.Contains(s, "@")
	return v.Check(ok, path, "Invalid email")
}

// Match checks a regular expression; msg "" means Zod's "Invalid".
func (v *V) Match(path, s string, re *regexp.Regexp, msg string) bool {
	if msg == "" {
		msg = "Invalid"
	}
	return v.Check(re.MatchString(s), path, msg)
}

var objectIDRe = regexp.MustCompile(`^[a-fA-F0-9]{24}$`)

// ObjectID validates and parses a 24-hex identifier.
func (v *V) ObjectID(path, s string) bson.ObjectID {
	if !objectIDRe.MatchString(s) {
		v.Add(path, "Invalid identifier")
		return bson.NilObjectID
	}
	id, _ := bson.ObjectIDFromHex(s)
	return id
}

func (v *V) OK() bool { return len(v.issues) == 0 }

// Err returns nil or a 422 "Invalid <source>" carrying every issue.
// source is "request body", "query parameters" or "path parameters".
func (v *V) Err(source string) error {
	if len(v.issues) == 0 {
		return nil
	}
	return apperr.Validation("Invalid "+source, v.issues)
}

func fmtNum(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// ── JSON bodies ─────────────────────────────────────────────────────────

// DecodeBody decodes a JSON object into dst. An empty body decodes as {}
// (several endpoints take no payload but browsers still send the JSON
// content type). Malformed JSON is a 400; a wrongly typed field is a 422.
func DecodeBody(r *http.Request, dst any) error {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return err // *http.MaxBytesError maps to 413 in WriteError
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	if raw[0] != '{' {
		if !json.Valid(raw) {
			return apperr.New(apperr.ValidationError, "Request body is not valid JSON", 400)
		}
		return apperr.Validation("Invalid request body", []apperr.FieldIssue{{Path: "", Message: "Expected object, received " + jsonKind(raw)}})
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) {
			return apperr.Validation("Invalid request body", []apperr.FieldIssue{{
				Path:    typeErr.Field,
				Message: "Expected " + goKindToZod(typeErr.Type.Kind().String()) + ", received " + typeErr.Value,
			}})
		}
		var syntaxErr *json.SyntaxError
		if errors.As(err, &syntaxErr) {
			return apperr.New(apperr.ValidationError, "Request body is not valid JSON", 400)
		}
		return apperr.Validation("Invalid request body", []apperr.FieldIssue{{Path: "", Message: err.Error()}})
	}
	return nil
}

func jsonKind(raw []byte) string {
	switch raw[0] {
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

func goKindToZod(kind string) string {
	switch {
	case strings.HasPrefix(kind, "int"), strings.HasPrefix(kind, "uint"), strings.HasPrefix(kind, "float"):
		return "number"
	case kind == "bool":
		return "boolean"
	case kind == "slice", kind == "array":
		return "array"
	case kind == "struct", kind == "map":
		return "object"
	}
	return kind
}

// Opt distinguishes an absent JSON field from an explicit null, for PATCH
// bodies where null means "clear".
type Opt[T any] struct {
	Set  bool // the key was present
	Null bool // the value was null
	V    T
}

func (o *Opt[T]) UnmarshalJSON(b []byte) error {
	o.Set = true
	if string(b) == "null" {
		o.Null = true
		return nil
	}
	return json.Unmarshal(b, &o.V)
}

// Ptr returns nil for absent or null, else a pointer to the value.
func (o Opt[T]) Ptr() *T {
	if !o.Set || o.Null {
		return nil
	}
	v := o.V
	return &v
}

// ── path parameters ─────────────────────────────────────────────────────

// IDParam parses a chi URL parameter as an ObjectID (422 when malformed).
func IDParam(r *http.Request, name string) (bson.ObjectID, error) {
	v := NewV()
	id := v.ObjectID(name, chi.URLParam(r, name))
	if err := v.Err("path parameters"); err != nil {
		return bson.NilObjectID, err
	}
	return id, nil
}

// Param returns a raw chi URL parameter.
func Param(r *http.Request, name string) string { return chi.URLParam(r, name) }

// ── query strings ───────────────────────────────────────────────────────

// Q parses query parameters with Zod's z.coerce semantics.
type Q struct {
	r *http.Request
	V *V
}

func Query(r *http.Request) *Q { return &Q{r: r, V: NewV()} }

func (q *Q) raw(name string) (string, bool) {
	vals, ok := q.r.URL.Query()[name]
	if !ok || len(vals) == 0 {
		return "", false
	}
	return vals[0], true
}

// Has reports whether the parameter is present.
func (q *Q) Has(name string) bool { _, ok := q.raw(name); return ok }

// Str returns the value or "" when absent.
func (q *Q) Str(name string) string { s, _ := q.raw(name); return s }

// OptStr returns nil when absent.
func (q *Q) OptStr(name string) *string {
	s, ok := q.raw(name)
	if !ok {
		return nil
	}
	return &s
}

func (q *Q) number(name, s string) (float64, bool) {
	// Number("") is 0 in JavaScript, which is what z.coerce.number sees.
	if strings.TrimSpace(s) == "" {
		return 0, true
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || math.IsNaN(f) {
		q.V.Add(name, "Expected number, received nan")
		return 0, false
	}
	return f, true
}

// Int coerces an integer in [min, max], defaulting when absent.
func (q *Q) Int(name string, def, min, max int64) int64 {
	s, ok := q.raw(name)
	if !ok {
		return def
	}
	f, ok := q.number(name, s)
	if !ok {
		return def
	}
	if !q.V.IsInt(name, f) {
		return def
	}
	n := int64(f)
	q.V.Int(name, n, min, max)
	return n
}

// OptInt is Int without a default (nil when absent).
func (q *Q) OptInt(name string, min, max int64) *int64 {
	if !q.Has(name) {
		return nil
	}
	n := q.Int(name, 0, min, max)
	return &n
}

// OptFloat coerces a number (nil when absent).
func (q *Q) OptFloat(name string, min, max float64) *float64 {
	s, ok := q.raw(name)
	if !ok {
		return nil
	}
	f, ok := q.number(name, s)
	if !ok {
		return nil
	}
	q.V.Float(name, f, min, max)
	return &f
}

// Enum returns def when absent, else validates membership.
func (q *Q) Enum(name, def string, allowed ...string) string {
	s, ok := q.raw(name)
	if !ok {
		return def
	}
	q.V.Enum(name, s, allowed...)
	return s
}

// OptEnum returns nil when absent.
func (q *Q) OptEnum(name string, allowed ...string) *string {
	s, ok := q.raw(name)
	if !ok {
		return nil
	}
	q.V.Enum(name, s, allowed...)
	return &s
}

// OptBool accepts "true"/"false" (and "1"/"0"); nil when absent.
func (q *Q) OptBool(name string) *bool {
	s, ok := q.raw(name)
	if !ok {
		return nil
	}
	var b bool
	switch strings.ToLower(s) {
	case "true", "1":
		b = true
	case "false", "0":
		b = false
	default:
		q.V.Add(name, "Invalid enum value. Expected 'true' | 'false', received '"+s+"'")
	}
	return &b
}

// OptDate coerces like z.coerce.date (ISO strings or epoch ms).
func (q *Q) OptDate(name string) *time.Time {
	s, ok := q.raw(name)
	if !ok {
		return nil
	}
	t, ok := ParseDate(s)
	if !ok {
		q.V.Add(name, "Invalid date")
		return nil
	}
	return &t
}

// OptObjectID returns nil when absent.
func (q *Q) OptObjectID(name string) *bson.ObjectID {
	s, ok := q.raw(name)
	if !ok {
		return nil
	}
	id := q.V.ObjectID(name, s)
	return &id
}

// Page reads the shared pagination fragment (page >= 1, limit 1..100, default 20).
func (q *Q) Page() (page, limit int64) {
	return q.Int("page", 1, 1, math.MaxInt32), q.Int("limit", 20, 1, 100)
}

// Err returns the collected issues as "Invalid query parameters".
func (q *Q) Err() error { return q.V.Err("query parameters") }

var dateLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02T15:04:05.000Z07:00",
	"2006-01-02T15:04:05",
	"2006-01-02T15:04",
	"2006-01-02",
}

// ParseDate accepts the formats JavaScript's Date constructor commonly sees
// from these frontends: ISO date-times, plain dates and epoch milliseconds.
func ParseDate(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil && len(s) > 4 {
		return time.UnixMilli(n).UTC(), true
	}
	for _, layout := range dateLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}
