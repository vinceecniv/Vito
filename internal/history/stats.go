package history

import (
	"strconv"
	"time"
)

// DayWords is one day's aggregates for the weekly chart + its hover tooltip.
type DayWords struct {
	Label         string `json:"label"`              // weekday, day-of-month, week or month
	Date          string `json:"date"`               // yyyy-mm-dd, first day of the bucket
	EndDate       string `json:"end_date,omitempty"` // last day, when the bucket spans more than one
	Hour          int    `json:"hour,omitempty"`     // 0..23 when the series is hourly
	Words         int    `json:"words"`
	Sentences     int    `json:"sentences"`
	Activations   int    `json:"activations"`
	SpokenSeconds int    `json:"spoken_seconds"`
	SavedMinutes  int    `json:"saved_minutes"`
	Weekend       bool   `json:"weekend"`

	// Billable quantities for this day. They carry no prices — the API layer
	// owns the provider rates and currency — so they stay out of the payload
	// and only Cost is served.
	DurationMS       int64   `json:"-"`
	CleanupInTokens  int64   `json:"-"`
	CleanupOutTokens int64   `json:"-"`
	CommandInTokens  int64   `json:"-"` // Vito Assist tokens, priced at the assist rate
	CommandOutTokens int64   `json:"-"`
	Cost             float64 `json:"cost"` // in Stats.Currency, filled by the server
}

// Stats is a usage summary over the requested period plus the current week's
// per-day words for the chart.
type Stats struct {
	PeriodDays        int        `json:"period_days"` // effective window length in days
	Words             int        `json:"words"`
	Sentences         int        `json:"sentences"`
	Activations       int        `json:"activations"`
	Commands          int        `json:"commands"` // Vito Assist commands in the period
	ActivationsPerDay float64    `json:"activations_per_day"`
	SavedMinutes      int        `json:"saved_minutes"`
	SpokenSeconds     int        `json:"spoken_seconds"` // total dictation time in the period
	TypingWPM         int        `json:"typing_wpm"`     // wpm baseline used for saved-time
	FirstDay          string     `json:"first_day"`      // earliest day with any data, "" if none
	Week              []DayWords `json:"week"`
	WeekPeakIndex     int        `json:"week_peak_index"`
	// SeriesUnit says what one bar covers: day | week | month. It follows the
	// requested window, so the chart never turns into a wall of thin bars.
	SeriesUnit string `json:"series_unit"`
	Currency   string `json:"currency"` // currency of DayWords.Cost, set by the server
}

// Stats computes the summary over the last `days` calendar days (0 = all time)
// using the permanent day_stats aggregate, so it works far beyond the history
// row cap. `wpm` sets the saved-typing-time baseline. `now`'s location sets the
// day boundaries. The weekly chart always covers the current Monday..Sunday.
func (s *Store) Stats(now time.Time, wpm float64, days int) (Stats, error) {
	if wpm <= 0 {
		wpm = 40
	}
	const day = 24 * time.Hour
	loc := now.Location()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)

	// days == -1 is "yesterday": the same single-day shape as today, only the
	// window ends a day earlier. Every calculation below works off that anchor.
	anchor := today
	if days == -1 {
		anchor, days = today.Add(-day), 1
	}
	toDay := anchor.Format("2006-01-02")
	fromDay := ""
	if days > 0 {
		fromDay = anchor.Add(-time.Duration(days-1) * day).Format("2006-01-02")
	}

	words, sent, act, durMS, firstDay, err := s.DayTotals(fromDay, toDay)
	if err != nil {
		return Stats{}, err
	}
	commands, err := s.CommandTotal(fromDay, toDay)
	if err != nil {
		return Stats{}, err
	}

	// Divide the per-day average only over days Vito has actually had data:
	// from the first day with data in range up to today, inclusive. So a fresh
	// install isn't diluted by empty days it was never around for.
	divisor := 1
	span := days
	if firstDay != "" {
		if fd, e := time.ParseInLocation("2006-01-02", firstDay, loc); e == nil {
			d := int(anchor.Sub(fd)/day) + 1
			if d < 1 {
				d = 1
			}
			divisor = d
			if days <= 0 {
				span = d // all-time: the window is however long we've had data
			} else if divisor > days {
				divisor = days
			}
		}
	}
	if span < 1 {
		span = 1
	}

	saved := float64(words)/wpm - float64(durMS)/60000.0
	if saved < 0 {
		saved = 0
	}

	// All-time earliest day with data, for the weekly chart's placeholder bars.
	allTimeFirst, err := s.FirstDataDay()
	if err != nil {
		return Stats{}, err
	}

	st := Stats{
		PeriodDays:        span,
		Words:             words,
		Sentences:         sent,
		Activations:       act,
		Commands:          commands,
		ActivationsPerDay: float64(act) / float64(divisor),
		SavedMinutes:      int(saved + 0.5),
		SpokenSeconds:     int(durMS / 1000),
		TypingWPM:         int(wpm),
		FirstDay:          allTimeFirst,
		WeekPeakIndex:     -1,
	}

	// "Today" gets an hourly breakdown — a single day bar says nothing, and the
	// hours show when you actually dictate.
	if days == 1 {
		w, sn, ac, dur, inTok, outTok, cmdIn, cmdOut, err := s.HourTotals(anchor)
		if err != nil {
			return Stats{}, err
		}
		st.SeriesUnit = "hour"
		peak := -1
		for h := 0; h < 24; h++ {
			savedH := float64(w[h])/wpm - float64(dur[h])/60000.0
			if savedH < 0 {
				savedH = 0
			}
			st.Week = append(st.Week, DayWords{
				Label:            strconv.Itoa(h),
				Date:             toDay,
				Hour:             h,
				Words:            w[h],
				Sentences:        sn[h],
				Activations:      ac[h],
				SpokenSeconds:    int(dur[h] / 1000),
				SavedMinutes:     int(savedH + 0.5),
				DurationMS:       dur[h],
				CleanupInTokens:  inTok[h],
				CleanupOutTokens: outTok[h],
				CommandInTokens:  cmdIn[h],
				CommandOutTokens: cmdOut[h],
			})
			if w[h] > peak {
				peak, st.WeekPeakIndex = w[h], h
			}
		}
		if peak <= 0 {
			st.WeekPeakIndex = -1
		}
		return st, nil
	}

	// Otherwise the chart covers the same window as the figures above it. Buckets
	// grow with the window so the bar count stays readable: days for a week or a
	// month, weeks for four weeks, months for a quarter and beyond.
	buckets, unit := ChartBuckets(now, days, firstDay)
	st.SeriesUnit = unit
	peak := -1
	for i, b := range buckets {
		w, sn, ac, dur, _, err := s.DayTotals(b.From, b.To)
		if err != nil {
			return Stats{}, err
		}
		_, _, inTok, outTok, cmdIn, cmdOut, _, err := s.CostTotals(b.From, b.To)
		if err != nil {
			return Stats{}, err
		}
		savedD := float64(w)/wpm - float64(dur)/60000.0
		if savedD < 0 {
			savedD = 0
		}
		st.Week = append(st.Week, DayWords{
			Label:            b.Label,
			Date:             b.From,
			EndDate:          b.To,
			Words:            w,
			Sentences:        sn,
			Activations:      ac,
			SpokenSeconds:    int(dur / 1000),
			SavedMinutes:     int(savedD + 0.5),
			Weekend:          b.Weekend,
			DurationMS:       dur,
			CleanupInTokens:  inTok,
			CleanupOutTokens: outTok,
			CommandInTokens:  cmdIn,
			CommandOutTokens: cmdOut,
		})
		if w > peak {
			peak, st.WeekPeakIndex = w, i
		}
	}
	if peak <= 0 {
		st.WeekPeakIndex = -1 // nothing in this window: highlight nothing
	}
	return st, nil
}

// Bucket is one bar: an inclusive day range plus the label under it. Exported
// so the demo generator can lay its fake data out over exactly the same bars.
type Bucket struct {
	From, To string
	Label    string
	Weekend  bool
}

// Days expands the bucket into the individual dates it covers.
func (b Bucket) Days(loc *time.Location) []time.Time {
	from, err := time.ParseInLocation("2006-01-02", b.From, loc)
	if err != nil {
		return nil
	}
	to, err := time.ParseInLocation("2006-01-02", b.To, loc)
	if err != nil {
		return nil
	}
	var out []time.Time
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		out = append(out, d)
	}
	return out
}

// Chart axis labels. English, because that is the UI's source language: the web
// side runs every one of these through t(), which is keyed by the English text.
var dayLabels = []string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"}
var monthLabels = []string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}

// ChartBuckets lays out the bars for a window of `days` (0 = all time, which
// starts at firstDay). It returns the ranges plus the unit the UI names them by.
func ChartBuckets(now time.Time, days int, firstDay string) ([]Bucket, string) {
	const day = 24 * time.Hour
	loc := now.Location()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	fmtDay := func(t time.Time) string { return t.Format("2006-01-02") }

	// "Today" as a single bar is not a chart — keep a week of context around it.
	// The caller labels the chart with its real span, so this stays honest.
	if days == 1 {
		days = 7
	}
	start := today
	if days > 0 {
		start = today.Add(-time.Duration(days-1) * day)
	} else if fd, err := time.ParseInLocation("2006-01-02", firstDay, loc); err == nil {
		start = fd
	}
	span := int(today.Sub(start)/day) + 1

	// The "last 4 weeks" and "last 3 months" periods are picked to divide
	// exactly, so they get that many bars: four rolling weeks ending today, or
	// three whole months ending with the current one. Anchoring those to the
	// calendar instead would leave a stub bar at either end.
	if days == 28 {
		out := make([]Bucket, 0, 4)
		for i := 3; i >= 0; i-- {
			from := today.Add(-time.Duration(i*7+6) * day)
			to := today.Add(-time.Duration(i*7) * day)
			out = append(out, Bucket{From: fmtDay(from), To: fmtDay(to),
				Label: strconv.Itoa(from.Day()) + "/" + strconv.Itoa(int(from.Month()))})
		}
		return out, "week"
	}
	// All time: one bar per calendar year. Anything finer turns into a wall of
	// bars the moment Vito has been in use for a while.
	if days == 0 {
		firstYear := today.Year()
		if fd, err := time.ParseInLocation("2006-01-02", firstDay, loc); err == nil {
			firstYear = fd.Year()
		}
		out := make([]Bucket, 0, today.Year()-firstYear+1)
		for y := firstYear; y <= today.Year(); y++ {
			from := time.Date(y, time.January, 1, 0, 0, 0, 0, loc)
			to := time.Date(y, time.December, 31, 0, 0, 0, 0, loc)
			if to.After(today) {
				to = today
			}
			out = append(out, Bucket{From: fmtDay(from), To: fmtDay(to), Label: strconv.Itoa(y)})
		}
		return out, "year"
	}
	if days == 92 {
		out := make([]Bucket, 0, 3)
		first := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, loc).AddDate(0, -2, 0)
		for i := 0; i < 3; i++ {
			m := first.AddDate(0, i, 0)
			end := m.AddDate(0, 1, -1)
			if end.After(today) {
				end = today
			}
			out = append(out, Bucket{From: fmtDay(m), To: fmtDay(end), Label: monthLabels[int(m.Month())-1]})
		}
		return out, "month"
	}

	switch {
	case span <= 31:
		// A day per bar, labelled with the weekday for a week and the day of the
		// month once there are too many for that to be readable.
		out := make([]Bucket, 0, span)
		for d := start; !d.After(today); d = d.Add(day) {
			wd := (int(d.Weekday()) + 6) % 7
			label := dayLabels[wd]
			if span > 10 {
				label = strconv.Itoa(d.Day())
			}
			out = append(out, Bucket{From: fmtDay(d), To: fmtDay(d), Label: label, Weekend: wd >= 5})
		}
		return out, "day"
	case span <= 190:
		// Whole weeks, Monday-anchored, labelled with the Monday's date.
		out := []Bucket{}
		wd := (int(start.Weekday()) + 6) % 7
		for w := start.Add(-time.Duration(wd) * day); !w.After(today); w = w.Add(7 * day) {
			end := w.Add(6 * day)
			if end.After(today) {
				end = today
			}
			out = append(out, Bucket{From: fmtDay(w), To: fmtDay(end), Label: strconv.Itoa(w.Day()) + "/" + strconv.Itoa(int(w.Month()))})
		}
		return out, "week"
	default:
		// Calendar months.
		out := []Bucket{}
		for m := time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, loc); !m.After(today); m = m.AddDate(0, 1, 0) {
			end := m.AddDate(0, 1, -1)
			if end.After(today) {
				end = today
			}
			out = append(out, Bucket{From: fmtDay(m), To: fmtDay(end), Label: monthLabels[int(m.Month())-1]})
		}
		return out, "month"
	}
}
