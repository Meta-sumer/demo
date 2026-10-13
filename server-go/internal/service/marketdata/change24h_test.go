package marketdata

import "testing"

func f64(v float64) *float64 { return &v }

func TestChange24hPercent(t *testing.T) {
	cases := []struct {
		last, ref *float64
		want      *float64
	}{
		{f64(110), f64(100), f64(10)},
		{f64(95), f64(100), f64(-5)},
		{f64(100.123), f64(100), f64(0.12)},
		{f64(100.125), f64(100), f64(0.13)},
		{f64(64250.5), f64(63000), f64(1.98)},
		{f64(100), f64(100), f64(0)},
		{nil, f64(100), nil},
		{f64(100), nil, nil},
		{f64(100), f64(0), nil},
	}
	for _, c := range cases {
		got := Change24hPercent(c.last, c.ref)
		switch {
		case c.want == nil && got != nil:
			t.Errorf("want nil, got %v", *got)
		case c.want != nil && (got == nil || *got != *c.want):
			t.Errorf("last=%v ref=%v: want %v, got %v", deref(c.last), deref(c.ref), *c.want, got)
		}
	}
}

func deref(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}
