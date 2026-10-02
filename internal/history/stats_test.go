package history

import (
	"testing"
	"time"
)

func TestRangeBuckets(t *testing.T) {
	d := func(s string) time.Time {
		v, _ := time.ParseInLocation("2006-01-02", s, time.Local)
		return v
	}
	cases := []struct {
		from, to   string
		unit       string
		n          int
		first, end string // From of the first bar, To of the last
	}{
		{"2026-09-03", "2026-09-20", "day", 18, "2026-09-03", "2026-09-20"},
		// 2026-08-20 is a Thursday: its week bar starts there, not on Monday.
		{"2026-08-20", "2026-10-10", "week", 8, "2026-08-20", "2026-10-10"},
		{"2026-01-15", "2026-09-10", "month", 9, "2026-01-15", "2026-09-10"},
	}
	for _, c := range cases {
		bars, unit := RangeBuckets(d(c.from), d(c.to))
		if unit != c.unit || len(bars) != c.n {
			t.Errorf("%s..%s: got %d %s bars, want %d %s", c.from, c.to, len(bars), unit, c.n, c.unit)
			continue
		}
		if bars[0].From != c.first || bars[len(bars)-1].To != c.end {
			t.Errorf("%s..%s: bars span %s..%s", c.from, c.to, bars[0].From, bars[len(bars)-1].To)
		}
	}
}
