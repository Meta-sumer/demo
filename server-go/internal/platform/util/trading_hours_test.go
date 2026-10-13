package util

import (
	"math/rand/v2"
	"testing"
	"time"
)

// at builds a UTC instant in the week of Monday 2026-09-28 (day 1 = Monday ...
// day 7 = Sunday; day 8 = the following Monday).
func at(t *testing.T, day, hour, minute, second int) int64 {
	t.Helper()
	ts := time.Date(2026, 9, 27+day, hour, minute, second, 0, time.UTC)
	if day <= 7 {
		if wd := int(ts.Weekday()); (wd == 0 && day != 7) || (wd != 0 && wd != day) {
			t.Fatalf("fixture day %d is a %s", day, ts.Weekday())
		}
	}
	return ts.UnixMilli()
}

func TestNextTradingOpen(t *testing.T) {
	weekdays := []int{1, 2, 3, 4, 5}
	officeHours := []TradingWindow{{Days: weekdays, StartMinuteUtc: 9 * 60, EndMinuteUtc: 17 * 60}}
	// Crosses midnight: on each listed day, open 00:00-02:00 and 22:00-24:00.
	overnight := []TradingWindow{{Days: weekdays, StartMinuteUtc: 22 * 60, EndMinuteUtc: 2 * 60}}
	// Forex-like week: Sunday 22:00 through Friday 22:00.
	forex := []TradingWindow{
		{Days: []int{7}, StartMinuteUtc: 22 * 60, EndMinuteUtc: 1440},
		{Days: []int{1, 2, 3, 4}, StartMinuteUtc: 0, EndMinuteUtc: 1440},
		{Days: []int{5}, StartMinuteUtc: 0, EndMinuteUtc: 22 * 60},
	}
	mondayOnly := []TradingWindow{{Days: []int{1}, StartMinuteUtc: 0, EndMinuteUtc: 60}}
	wednesdayMorning := []TradingWindow{{Days: []int{3}, StartMinuteUtc: 10 * 60, EndMinuteUtc: 11 * 60}}

	cases := []struct {
		name    string
		windows []TradingWindow
		now     int64
		want    int64
	}{
		{"open now returns now", officeHours, at(t, 3, 12, 30, 15), at(t, 3, 12, 30, 15)},
		{"later the same day", officeHours, at(t, 3, 8, 30, 0), at(t, 3, 9, 0, 0)},
		{"one millisecond before the open", officeHours, at(t, 3, 9, 0, 0) - 1, at(t, 3, 9, 0, 0)},
		{"end minute is exclusive", officeHours, at(t, 3, 17, 0, 0), at(t, 4, 9, 0, 0)},
		{"weekday wrap: Friday evening to Monday", officeHours, at(t, 5, 18, 0, 0), at(t, 8, 9, 0, 0)},
		{"weekday wrap: Saturday to Monday", officeHours, at(t, 6, 12, 0, 0), at(t, 8, 9, 0, 0)},
		{"midnight window: open after 22:00", overnight, at(t, 1, 23, 15, 0), at(t, 1, 23, 15, 0)},
		{"midnight window: open before 02:00", overnight, at(t, 2, 1, 59, 59), at(t, 2, 1, 59, 59)},
		{"midnight window: closed at 02:00, opens at 22:00", overnight, at(t, 2, 2, 0, 0), at(t, 2, 22, 0, 0)},
		{"midnight window: Saturday is not a listed day", overnight, at(t, 6, 1, 0, 0), at(t, 8, 0, 0, 0)},
		{"Sunday to Monday", mondayOnly, at(t, 7, 23, 59, 30), at(t, 8, 0, 0, 0)},
		{"forex: Saturday to Sunday 22:00", forex, at(t, 6, 3, 0, 0), at(t, 7, 22, 0, 0)},
		{"forex: Friday after the close", forex, at(t, 5, 22, 0, 0), at(t, 7, 22, 0, 0)},
		{"forex: Sunday afternoon", forex, at(t, 7, 15, 0, 0), at(t, 7, 22, 0, 0)},
		{"forex: Sunday night is open", forex, at(t, 7, 23, 0, 0), at(t, 7, 23, 0, 0)},
		{"forex: Monday 00:00 is open", forex, at(t, 8, 0, 0, 0), at(t, 8, 0, 0, 0)},
		{"same weekday, earlier hours: a week ahead", wednesdayMorning, at(t, 3, 12, 0, 0), at(t, 3, 10, 0, 0) + 7*86_400_000},
		{"earliest of several windows", append(append([]TradingWindow{}, officeHours...), mondayOnly...), at(t, 6, 0, 0, 0), at(t, 8, 0, 0, 0)},
	}
	for _, c := range cases {
		got, ok := NextTradingOpen(c.windows, c.now)
		if !ok || got != c.want {
			t.Errorf("%s: got %v (ok=%v), want %v", c.name,
				time.UnixMilli(got).UTC(), ok, time.UnixMilli(c.want).UTC())
		}
	}

	for name, never := range map[string][]TradingWindow{
		"no days":         {{Days: []int{}, StartMinuteUtc: 0, EndMinuteUtc: 1440}},
		"invalid days":    {{Days: []int{0, 8}, StartMinuteUtc: 0, EndMinuteUtc: 1440}},
		"empty range":     {{Days: []int{1, 2, 3}, StartMinuteUtc: 600, EndMinuteUtc: 600}},
		"start past 1440": {{Days: []int{1}, StartMinuteUtc: 1500, EndMinuteUtc: 1500}},
	} {
		if got, ok := NextTradingOpen(never, at(t, 3, 12, 0, 0)); ok {
			t.Errorf("%s: want never, got %v", name, time.UnixMilli(got).UTC())
		}
	}

	if got, ok := NextTradingOpen(nil, 42); !ok || got != 42 {
		t.Errorf("no windows is always open: got %v %v", got, ok)
	}
}

// bruteNextOpen steps minute by minute with IsWithinTradingHours - the
// definition NextTradingOpen must agree with.
func bruteNextOpen(windows []TradingWindow, atMs int64) (int64, bool) {
	if IsWithinTradingHours(windows, atMs) {
		return atMs, true
	}
	const minuteMs = 60_000
	for m := atMs - atMs%minuteMs + minuteMs; m <= atMs+8*86_400_000; m += minuteMs {
		if IsWithinTradingHours(windows, m) {
			return m, true
		}
	}
	return 0, false
}

func TestNextTradingOpenMatchesMinuteByMinute(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 11))
	minuteOf := func() int {
		switch rng.IntN(6) {
		case 0:
			return 0
		case 1:
			return 1440
		default:
			return rng.IntN(1441)
		}
	}
	base := at(t, 1, 0, 0, 0)
	var openNow, later, never int
	for i := 0; i < 400; i++ {
		windows := make([]TradingWindow, 1+rng.IntN(3))
		for j := range windows {
			var days []int
			for d := 1; d <= 7; d++ {
				if rng.IntN(3) == 0 {
					days = append(days, d)
				}
			}
			windows[j] = TradingWindow{Days: days, StartMinuteUtc: minuteOf(), EndMinuteUtc: minuteOf()}
		}
		now := base + rng.Int64N(14*86_400_000)
		want, wantOK := bruteNextOpen(windows, now)
		got, ok := NextTradingOpen(windows, now)
		if ok != wantOK || got != want {
			t.Fatalf("windows %+v at %v: got %v (ok=%v), want %v (ok=%v)", windows,
				time.UnixMilli(now).UTC(), time.UnixMilli(got).UTC(), ok, time.UnixMilli(want).UTC(), wantOK)
		}
		switch {
		case !ok:
			never++
		case got == now:
			openNow++
		default:
			later++
		}
	}
	// Guard against a generator that only ever produces one kind of case.
	if openNow < 20 || later < 100 || never < 5 {
		t.Fatalf("poor coverage: open now %d, opens later %d, never %d", openNow, later, never)
	}
}
