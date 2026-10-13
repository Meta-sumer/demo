// Package iso formats times for the API exactly as JavaScript's
// Date.prototype.toISOString does: UTC with three fractional digits
// ("2026-09-28T10:04:05.000Z"). The fixed width keeps string comparison of
// timestamps correct in the frontends.
package iso

import (
	"encoding/json"
	"time"
)

const Layout = "2006-01-02T15:04:05.000Z"

// Time is a time.Time that marshals as a JS ISO string.
type Time struct{ time.Time }

func From(t time.Time) Time { return Time{t} }

// Ptr converts a nullable time; nil stays nil and marshals as null.
func Ptr(t *time.Time) *Time {
	if t == nil {
		return nil
	}
	v := Time{*t}
	return &v
}

func Format(t time.Time) string { return t.UTC().Format(Layout) }

// FromMillis converts epoch milliseconds.
func FromMillis(ms int64) Time { return Time{time.UnixMilli(ms).UTC()} }

func (t Time) MarshalJSON() ([]byte, error) {
	return json.Marshal(t.UTC().Format(Layout))
}

func (t *Time) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	parsed, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return err
	}
	t.Time = parsed
	return nil
}
