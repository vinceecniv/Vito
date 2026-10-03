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

	// SpokenWPM is how fast you speak while dictating: words over recording
	// time. AvgWords is the length of a typical dictation. Both come from the
	// permanent day sums, so they cover any period.
	SpokenWPM int `json:"spoken_wpm"`

	// The streak you are on and the longest ever (see Store.Streaks); filled in
	// by the server, whatever the period.
	CurrentStreak int64 `json:"current_streak"`
	LongestStreak int64 `json:"longest_streak"`
	// PreviousStreak is the best streak before the current one, the record it
	// has to beat; 0 until a first streak has ended.
	PreviousStreak int64   `json:"previous_streak"`
	AvgWords       float64 `json:"avg_words"`
	// Insights need the individual dictations, so they reach back only as far
	// as the history does.
	Insights Insights `json:"insights"`
	// Calendar is the last CalendarWeeks weeks of words per day, for the
	// heatmap; it ends today regardless of the period.
	Calendar Calendar `json:"calendar"`
}

// Stats computes the summary over the last `days` calendar days (0 = all time)
// using the permanent day_stats aggregate, so it works far beyond the history
// row cap. `wpm` sets the saved-typing-time baseline. `now`'s location sets the
// day boundaries. The weekly chart always covers the current Monday..Sunday.
func (s *Store) Stats(now time.Time, wpm float64, days int) (Stats, error) {
	loc := now.Location()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)

	// days == -1 is "yesterday": the same single-day shape as today, only the
	// window ends a day earlier. Every calculation below works off that anchor.
	anchor := today
	if days == -1 {
		anchor, days = today.AddDate(0, 0, -1), 1
	}
	var from time.Time // zero: all time
	if days > 0 {
		from = anchor.AddDate(0, 0, -(days - 1))
	}
	return s.stats(wpm, from, anchor, func(firstDay string) ([]Bucket, string) {
		return ChartBuckets(now, days, firstDay)
	})
}

// StatsRange is Stats over a chosen stretch of the calendar, from and to both
// local midnights and both included. A single day gets the hourly chart, like
// today; longer ranges get days, weeks or months by length (RangeBuckets).
func (s *Store) StatsRange(wpm float64, from, to time.Time) (Stats, error) {
	return s.stats(wpm, from, to, func(string) ([]Bucket, string) {
		return RangeBuckets(from, to)
	})
}

// stats does the work for Stats and StatsRange: the window runs from `from`
// (zero for all time) to `anchor`, both included, and buckets lays out the
// chart bars for anything longer than a day.
func (s *Store) stats(wpm float64, from, anchor time.Time, buckets func(firstDay string) ([]Bucket, string)) (Stats, error) {
	if wpm <= 0 {
		wpm = 40
	}
	const day = 24 * time.Hour
	loc := anchor.Location()
	toDay := anchor.Format("2006-01-02")
	fromDay, days := "", 0
	if !from.IsZero() {
		fromDay = from.Format("2006-01-02")
		days = daysBetween(from, anchor) + 1
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
	if durMS > 0 {
		st.SpokenWPM = int(float64(words)/(float64(durMS)/60000.0) + 0.5)
	}
	if act > 0 {
		st.AvgWords = float64(words) / float64(act)
	}
	if st.Insights, err = s.Insights(from, anchor); err != nil {
		return Stats{}, err
	}
	if st.Calendar, err = s.Calendar(time.Now().In(loc)); err != nil {
		return Stats{}, err
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
	bars, unit := buckets(firstDay)
	st.SeriesUnit = unit
	peak := -1
	for i, b := range bars {
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

	// Every period, all time included, gets the finest bars its length allows
	// (spanBuckets): four weeks as days, three months and a year as weeks,
	// longer as months. The UI thins out the labels when the bars get many.
	return spanBuckets(start, today, false)
}

// RangeBuckets lays out the bars for a chosen range, from and to both included:
// a day per bar up to two months, then weeks up to a year, then calendar
// months. The outer bars are clipped to the range, so none counts a day
// outside it.
func RangeBuckets(from, to time.Time) ([]Bucket, string) {
	return spanBuckets(from, to, true)
}

// spanBuckets picks the bar size from the length of start..end, so the chart
// stays readable however long the window is. With clip, the first week or
// month starts at start instead of its own first day.
func spanBuckets(start, end time.Time, clip bool) ([]Bucket, string) {
	loc := start.Location()
	fmtDay := func(t time.Time) string { return t.Format("2006-01-02") }
	span := daysBetween(start, end) + 1
	bucket := func(from, to time.Time, label string) Bucket {
		if clip && from.Before(start) {
			from = start
		}
		if to.After(end) {
			to = end
		}
		return Bucket{From: fmtDay(from), To: fmtDay(to), Label: label}
	}
	switch {
	case span <= 62:
		// A day per bar, labelled with the weekday for a week and the day of the
		// month once there are too many for that to be readable.
		out := make([]Bucket, 0, span)
		for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
			wd := (int(d.Weekday()) + 6) % 7
			label := dayLabels[wd]
			if span > 10 {
				label = strconv.Itoa(d.Day())
			}
			out = append(out, Bucket{From: fmtDay(d), To: fmtDay(d), Label: label, Weekend: wd >= 5})
		}
		return out, "day"
	case span <= 371:
		// Whole weeks, Monday-anchored, labelled with the Monday's date.
		out := []Bucket{}
		wd := (int(start.Weekday()) + 6) % 7
		for w := start.AddDate(0, 0, -wd); !w.After(end); w = w.AddDate(0, 0, 7) {
			out = append(out, bucket(w, w.AddDate(0, 0, 6), strconv.Itoa(w.Day())+"/"+strconv.Itoa(int(w.Month()))))
		}
		return out, "week"
	default:
		// Calendar months.
		out := []Bucket{}
		for m := time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, loc); !m.After(end); m = m.AddDate(0, 1, 0) {
			out = append(out, bucket(m, m.AddDate(0, 1, -1), monthLabels[int(m.Month())-1]))
		}
		return out, "month"
	}
}
