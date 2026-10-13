// Package util holds money, id, time and percentile helpers ported from the
// Node server's utils folder.
package util

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ── Money ────────────────────────────────────────────────────────────────
// Every monetary value in the database, the API and on the wire is an
// integer number of minor units (paise). ₹100.00 -> 10000.

const MinorUnitsPerMajor = 100

// RoundHalfUp matches JavaScript's Math.round (half rounds towards +Inf).
func RoundHalfUp(x float64) float64 { return math.Floor(x + 0.5) }

func ToMinor(major float64) int64   { return int64(RoundHalfUp(major * MinorUnitsPerMajor)) }
func FromMinor(minor int64) float64 { return float64(minor) / MinorUnitsPerMajor }

// CalculateProfit: payoutPercent is a percentage of the stake (82 => 82%).
func CalculateProfit(stakeMinor int64, payoutPercent float64) int64 {
	return int64(RoundHalfUp(float64(stakeMinor) * payoutPercent / 100))
}

// CalculateWinningCredit is stake returned plus profit.
func CalculateWinningCredit(stakeMinor int64, payoutPercent float64) int64 {
	return stakeMinor + CalculateProfit(stakeMinor, payoutPercent)
}

// CalculateLiability is what the platform pays if the trade wins.
func CalculateLiability(stakeMinor int64, payoutPercent float64) int64 {
	return CalculateWinningCredit(stakeMinor, payoutPercent)
}

func SafePercent(numerator, denominator float64) float64 {
	if denominator == 0 {
		return 0
	}
	return numerator / denominator * 100
}

// FormatMinor renders an INR amount the way the Node server's Intl formatter
// does for en-IN ("₹1,23,456.78").
func FormatMinor(minor int64) string {
	neg := minor < 0
	if neg {
		minor = -minor
	}
	whole := minor / 100
	frac := minor % 100
	s := strconv.FormatInt(whole, 10)
	// Indian grouping: last three digits, then groups of two.
	if len(s) > 3 {
		head, tail := s[:len(s)-3], s[len(s)-3:]
		var parts []string
		for len(head) > 2 {
			parts = append([]string{head[len(head)-2:]}, parts...)
			head = head[:len(head)-2]
		}
		if head != "" {
			parts = append([]string{head}, parts...)
		}
		s = strings.Join(parts, ",") + "," + tail
	}
	out := fmt.Sprintf("₹%s.%02d", s, frac)
	if neg {
		out = "-" + out
	}
	return out
}

// ── Ids ──────────────────────────────────────────────────────────────────

func UUID() string { return uuid.NewString() }

// Reference is a short, URL-safe, non-sequential reference ("TRD_xxxxxxxxxxxx").
func Reference(prefix string) string {
	b := make([]byte, 9)
	_, _ = rand.Read(b)
	return prefix + "_" + base64.RawURLEncoding.EncodeToString(b)
}

func SHA256(value string) string {
	h := sha256.Sum256([]byte(value))
	return hex.EncodeToString(h[:])
}

// Fingerprint is a stable hash of a payload, used to detect an idempotency key
// replayed with a different body. The canonical form sorts object keys, as the
// Node implementation does. Node drops `undefined` but keeps `null`; tag
// optional request fields `omitempty` to get the same effect.
func Fingerprint(payload any) string {
	// Round-trip through JSON to reduce structs to maps.
	raw, _ := json.Marshal(payload)
	var v any
	_ = json.Unmarshal(raw, &v)
	return SHA256(stableStringify(v))
}

func stableStringify(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			kb, _ := json.Marshal(k)
			parts[i] = string(kb) + ":" + stableStringify(x[k])
		}
		return "{" + strings.Join(parts, ",") + "}"
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = stableStringify(e)
		}
		return "[" + strings.Join(parts, ",") + "]"
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}

// ── Time ─────────────────────────────────────────────────────────────────

var durationRe = regexp.MustCompile(`^(\d+)(ms|s|m|h|d)$`)

// ParseDuration parses "15m" / "30d" / "500ms" into milliseconds.
func ParseDuration(value string) (int64, error) {
	m := durationRe.FindStringSubmatch(strings.TrimSpace(value))
	if m == nil {
		return 0, fmt.Errorf("Invalid duration: %s", value)
	}
	n, _ := strconv.ParseInt(m[1], 10, 64)
	unit := map[string]int64{"ms": 1, "s": 1000, "m": 60_000, "h": 3_600_000, "d": 86_400_000}[m[2]]
	return n * unit, nil
}

// NowMs is authoritative server time in epoch milliseconds.
func NowMs() int64 { return time.Now().UnixMilli() }

// UTCDayKey is "YYYY-MM-DD" for the given epoch ms.
func UTCDayKey(atMs int64) string { return time.UnixMilli(atMs).UTC().Format("2006-01-02") }

func StartOfUTCDay(atMs int64) int64 {
	t := time.UnixMilli(atMs).UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC).UnixMilli()
}

// TradingWindow is a UTC minute range on ISO weekdays (1 = Monday ... 7 = Sunday).
type TradingWindow struct {
	Days           []int `bson:"days" json:"days"`
	StartMinuteUtc int   `bson:"startMinuteUtc" json:"startMinuteUtc"`
	EndMinuteUtc   int   `bson:"endMinuteUtc" json:"endMinuteUtc"`
}

// IsWithinTradingHours: an empty list means always open (crypto).
func IsWithinTradingHours(windows []TradingWindow, atMs int64) bool {
	if len(windows) == 0 {
		return true
	}
	d := time.UnixMilli(atMs).UTC()
	isoDay := int(d.Weekday())
	if isoDay == 0 {
		isoDay = 7
	}
	minute := d.Hour()*60 + d.Minute()
	for _, w := range windows {
		found := false
		for _, day := range w.Days {
			if day == isoDay {
				found = true
				break
			}
		}
		if !found {
			continue
		}
		if w.StartMinuteUtc <= w.EndMinuteUtc {
			if minute >= w.StartMinuteUtc && minute < w.EndMinuteUtc {
				return true
			}
		} else if minute >= w.StartMinuteUtc || minute < w.EndMinuteUtc {
			return true
		}
	}
	return false
}

// NextTradingOpen is the first instant at or after atMs at which
// IsWithinTradingHours holds: atMs itself while open, otherwise the start of
// the next open minute. ok is false when the windows never open (no valid
// day, or only empty ranges).
//
// It walks each window's open spans day by day rather than testing minute by
// minute. Eight days (today plus seven) cover a window that opens only on
// today's weekday, earlier than now.
func NextTradingOpen(windows []TradingWindow, atMs int64) (int64, bool) {
	if IsWithinTradingHours(windows, atMs) {
		return atMs, true
	}
	const minuteMs = int64(60_000)
	const dayMs = 1440 * minuteMs
	today := StartOfUTCDay(atMs)
	nowMinute := int((atMs - today) / minuteMs)
	for offset := int64(0); offset <= 7; offset++ {
		dayStart := today + offset*dayMs
		isoDay := int(time.UnixMilli(dayStart).UTC().Weekday())
		if isoDay == 0 {
			isoDay = 7
		}
		best := -1
		for _, w := range windows {
			if !slices.Contains(w.Days, isoDay) {
				continue
			}
			for _, span := range openSpans(w) {
				// Closed now, so a span that began today at or before this
				// minute has already ended.
				if offset == 0 && span[0] <= nowMinute {
					continue
				}
				if best < 0 || span[0] < best {
					best = span[0]
				}
			}
		}
		// Any span on a later day starts later than every span on this one.
		if best >= 0 {
			return dayStart + int64(best)*minuteMs, true
		}
	}
	return 0, false
}

// openSpans lists the [start, end) minute ranges a window is open on each of
// its days, read exactly as IsWithinTradingHours reads it: a window whose
// start is after its end crosses midnight, which on a listed day means open
// from midnight to end and again from start to midnight.
func openSpans(w TradingWindow) [][2]int {
	clamp := func(m int) int { return min(max(m, 0), 1440) }
	spans := make([][2]int, 0, 2)
	add := func(start, end int) {
		if start, end = clamp(start), clamp(end); start < end {
			spans = append(spans, [2]int{start, end})
		}
	}
	if w.StartMinuteUtc <= w.EndMinuteUtc {
		add(w.StartMinuteUtc, w.EndMinuteUtc)
	} else {
		add(0, w.EndMinuteUtc)
		add(w.StartMinuteUtc, 1440)
	}
	return spans
}

// ── Percentiles ──────────────────────────────────────────────────────────

type PercentileSummary struct {
	Count int     `json:"count"`
	Min   float64 `json:"min"`
	Max   float64 `json:"max"`
	Avg   float64 `json:"avg"`
	P50   float64 `json:"p50"`
	P95   float64 `json:"p95"`
	P99   float64 `json:"p99"`
}

func Summarise(samples []float64) PercentileSummary {
	if len(samples) == 0 {
		return PercentileSummary{}
	}
	sorted := append([]float64(nil), samples...)
	sort.Float64s(sorted)
	sum := 0.0
	for _, v := range sorted {
		sum += v
	}
	return PercentileSummary{
		Count: len(sorted),
		Min:   sorted[0],
		Max:   sorted[len(sorted)-1],
		Avg:   round2(sum / float64(len(sorted))),
		P50:   Percentile(sorted, 50),
		P95:   Percentile(sorted, 95),
		P99:   Percentile(sorted, 99),
	}
}

// Percentile is nearest-rank over an already sorted slice.
func Percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(math.Ceil(p / 100 * float64(len(sorted))))
	idx := rank - 1
	if idx < 0 {
		idx = 0
	}
	if idx > len(sorted)-1 {
		idx = len(sorted) - 1
	}
	return round2(sorted[idx])
}

func round2(v float64) float64 { return RoundHalfUp(v*100) / 100 }

// ── Birth date ───────────────────────────────────────────────────────────

const (
	MaxAgeYears = 120
	MinAgeYears = 18
)

var isoDateRe = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})$`)

// IsPlausibleBirthDate: a real calendar day and an age between 18 and 120.
func IsPlausibleBirthDate(value string, now time.Time) bool {
	m := isoDateRe.FindStringSubmatch(value)
	if m == nil {
		return false
	}
	year, _ := strconv.Atoi(m[1])
	month, _ := strconv.Atoi(m[2])
	day, _ := strconv.Atoi(m[3])
	if month < 1 || month > 12 || day < 1 || day > 31 {
		return false
	}
	parsed := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	if parsed.Year() != year || int(parsed.Month()) != month || parsed.Day() != day {
		return false
	}
	age := AgeInYears(parsed, now)
	return age >= MinAgeYears && age <= MaxAgeYears
}

func AgeInYears(birth, now time.Time) int {
	birth, now = birth.UTC(), now.UTC()
	age := now.Year() - birth.Year()
	monthDiff := int(now.Month()) - int(birth.Month())
	if monthDiff < 0 || (monthDiff == 0 && now.Day() < birth.Day()) {
		age--
	}
	return age
}
