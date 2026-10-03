package history

import (
	"testing"
	"time"
)

func TestLongestStreak(t *testing.T) {
	// Day 0 is a Monday; the window spans the end of daylight saving in Europe.
	monday := time.Date(2026, 10, 19, 0, 0, 0, 0, time.Local)
	on := func(offsets ...int) []time.Time {
		var out []time.Time
		for _, o := range offsets {
			out = append(out, monday.AddDate(0, 0, o))
		}
		return out
	}
	workweeks := func(weeks int) []int {
		var out []int
		for w := 0; w < weeks; w++ {
			for d := 0; d < 5; d++ {
				out = append(out, w*7+d)
			}
		}
		return out
	}

	cases := []struct {
		name string
		days []time.Time
		want int64
	}{
		{"nothing", nil, 0},
		{"one day", on(0), 1},
		{"a full week", on(0, 1, 2, 3, 4, 5, 6), 7},
		// Monday of week one to Friday of week four, weekends forgiven.
		{"four working weeks", on(workweeks(4)...), 26},
		// Friday to the next Tuesday misses three days running.
		{"long weekend breaks it", on(0, 1, 2, 3, 4, 8, 9, 10, 11), 5},
		// Wednesday, Saturday and Sunday fall within one week: the streak
		// restarts on Thursday and runs to the next Tuesday.
		{"three misses in a week", on(0, 1, 3, 4, 7, 8), 6},
		// Every other day misses three in any seven.
		{"every other day", on(0, 2, 4, 6, 8), 5},
	}
	for _, c := range cases {
		if got := longestStreak(c.days); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}

func TestCurrentStreak(t *testing.T) {
	monday := time.Date(2026, 10, 19, 0, 0, 0, 0, time.Local)
	on := func(offsets ...int) []time.Time {
		var out []time.Time
		for _, o := range offsets {
			out = append(out, monday.AddDate(0, 0, o))
		}
		return out
	}
	cases := []struct {
		name  string
		days  []time.Time
		today int
		want  int64
	}{
		{"nothing", nil, 0, 0},
		{"dictated today", on(0, 1, 2), 2, 3},
		// Today isn't over: the streak up to yesterday still stands.
		{"not yet today", on(0, 1, 2), 3, 3},
		// A working week, and Saturday and Sunday off: still alive on Monday.
		{"weekend forgiven", on(0, 1, 2, 3, 4), 7, 5},
		// Friday was the last day; by Tuesday three days are missed.
		{"long weekend broke it", on(0, 1, 2, 3, 4), 8, 0},
		{"broken earlier, going again", on(0, 1, 5, 6, 7, 8), 8, 4},
	}
	for _, c := range cases {
		if got := currentStreak(c.days, monday.AddDate(0, 0, c.today)); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}
