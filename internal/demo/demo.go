// Package demo generates the sample data shown while demo mode is on
// (`"demo": true` in the config file).
//
// Everything here is fabricated and English-only: it is served instead of the
// user's real stats, history and dictionary, and it never reads or writes the
// database or the settings file. Values are derived deterministically from the
// calendar date rather than from a random source, so polling an endpoint twice
// returns the same numbers and the charts don't jitter between refreshes.
package demo

import (
	"sort"
	"strings"
	"time"

	"vito/internal/config"
	"vito/internal/history"
)

// dayHash spreads consecutive dates into unrelated values (FNV-1a plus an
// avalanche step), so a run of days looks organic instead of like a ramp.
func dayHash(day string, salt uint32) uint32 {
	h := uint32(2166136261) ^ salt
	for i := 0; i < len(day); i++ {
		h ^= uint32(day[i])
		h *= 16777619
	}
	h ^= h >> 13
	h *= 0x5bd1e995
	h ^= h >> 15
	return h
}

// FirstDay is where the fake history starts: everything before it reads as
// "Vito wasn't installed yet", which is what the chart's placeholder bars and
// the per-active-day averages expect.
func FirstDay(now time.Time) string { return now.AddDate(0, 0, -95).Format("2006-01-02") }

// day returns one day's aggregates. Weekends are much quieter and roughly one
// weekday in seven is a day off, so the week chart has believable shape.
func day(now time.Time, d time.Time) (words, sentences, activations int, durMS, inTok, outTok int64) {
	ds := d.Format("2006-01-02")
	// Compare dates, not instants: callers pass both midnight timestamps and
	// "now minus n days", and the latter would otherwise make today look future.
	if ds > now.Format("2006-01-02") || ds < FirstDay(now) {
		return 0, 0, 0, 0, 0, 0 // future, or before Vito existed
	}
	h := dayHash(ds, 0)
	weekend := d.Weekday() == time.Saturday || d.Weekday() == time.Sunday
	if !weekend && h%7 == 0 {
		return 0, 0, 0, 0, 0, 0 // a day off
	}
	words = 380 + int(h%900)
	if weekend {
		words = int(float64(words) * 0.28)
	}
	if words < 40 {
		return 0, 0, 0, 0, 0, 0
	}
	sentences = words/14 + 1
	activations = words/95 + 1
	// ~135 spoken words per minute, jittered a little per day.
	durMS = int64(float64(words) / (125.0 + float64(dayHash(ds, 7)%25)) * 60_000)
	// Cleanup runs on most days; the token counts follow the transcript size.
	if dayHash(ds, 3)%9 != 0 {
		inTok = int64(words)*3 + 700
		outTok = int64(words) * 2
	}
	return
}

// Stats mirrors history.Store.Stats over the fabricated days: the same window
// arithmetic, so period totals, the per-active-day average and the week chart
// stay consistent with each other.
func Stats(now time.Time, wpm float64, days int) history.Stats {
	if wpm <= 0 {
		wpm = 40
	}
	if days == -1 { // "yesterday" — the sample data has no hourly detail anyway
		days = 1
	}
	const dur = 24 * time.Hour
	loc := now.Location()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)

	from := today.AddDate(0, 0, -364) // "all time" still needs a start
	if days > 0 {
		from = today.Add(-time.Duration(days-1) * dur)
	}
	var words, sent, act int
	var durMS int64
	first := ""
	for d := from; !d.After(today); d = d.Add(dur) {
		w, sn, a, ms, _, _ := day(now, d)
		if w == 0 {
			continue
		}
		if first == "" {
			first = d.Format("2006-01-02")
		}
		words, sent, act, durMS = words+w, sent+sn, act+a, durMS+ms
	}

	divisor, span := 1, days
	if first != "" {
		if fd, err := time.ParseInLocation("2006-01-02", first, loc); err == nil {
			n := int(today.Sub(fd)/dur) + 1
			if n < 1 {
				n = 1
			}
			divisor = n
			if days <= 0 {
				span = n
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

	st := history.Stats{
		PeriodDays:        span,
		Words:             words,
		Sentences:         sent,
		Activations:       act,
		ActivationsPerDay: float64(act) / float64(divisor),
		SavedMinutes:      int(saved + 0.5),
		SpokenSeconds:     int(durMS / 1000),
		TypingWPM:         int(wpm),
		FirstDay:          FirstDay(now),
		WeekPeakIndex:     -1,
	}

	// Same buckets as the real chart, so demo mode reacts to the period
	// dropdown exactly like live data does.
	buckets, unit := history.ChartBuckets(now, days, FirstDay(now))
	st.SeriesUnit = unit
	peak := -1
	for i, b := range buckets {
		var w, sn, a int
		var ms, in, out int64
		for _, d := range b.Days(loc) {
			dw, dsn, da, dms, din, dout := day(now, d)
			w, sn, a = w+dw, sn+dsn, a+da
			ms, in, out = ms+dms, in+din, out+dout
		}
		savedD := float64(w)/wpm - float64(ms)/60000.0
		if savedD < 0 {
			savedD = 0
		}
		st.Week = append(st.Week, history.DayWords{
			Label:            b.Label,
			Date:             b.From,
			EndDate:          b.To,
			Words:            w,
			Sentences:        sn,
			Activations:      a,
			SpokenSeconds:    int(ms / 1000),
			SavedMinutes:     int(savedD + 0.5),
			Weekend:          b.Weekend,
			DurationMS:       ms,
			CleanupInTokens:  in,
			CleanupOutTokens: out,
		})
		if w > peak {
			peak, st.WeekPeakIndex = w, i
		}
	}
	if peak <= 0 {
		st.WeekPeakIndex = -1
	}
	return st
}

// CostTotals mirrors history.Store.CostTotals over the fabricated days, so the
// cost card adds up to the same usage the statistics show.
func CostTotals(now time.Time, fromDay, toDay string) (durationMS, inTokens, outTokens int64, firstDay string) {
	loc := now.Location()
	to, err := time.ParseInLocation("2006-01-02", toDay, loc)
	if err != nil {
		return
	}
	from := to.AddDate(0, 0, -364)
	if fromDay != "" {
		if f, e := time.ParseInLocation("2006-01-02", fromDay, loc); e == nil {
			from = f
		}
	}
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		w, _, _, ms, in, out := day(now, d)
		if w == 0 {
			continue
		}
		if firstDay == "" {
			firstDay = d.Format("2006-01-02")
		}
		durationMS, inTokens, outTokens = durationMS+ms, inTokens+in, outTokens+out
	}
	return
}

// Dictionary is the sample dictionary shown in demo mode. Terms a speech engine
// would plausibly get wrong: product names, jargon and homophones.
func Dictionary() config.Dictionary {
	return config.Dictionary{
		Keyterms: []string{
			"Kubernetes", "PostgreSQL", "Grafana", "OAuth", "webhook", "changelog",
			"sprint retrospective", "Anthropic", "AssemblyAI", "Figma", "staging environment",
			"pull request", "onboarding", "SKU", "Q3 roadmap", "Helvetica Neue",
		},
		Corrections: []config.Correction{
			{Wrong: "cuber netties", Right: "Kubernetes"},
			{Wrong: "post gres", Right: "PostgreSQL"},
			{Wrong: "grafana dashboard", Right: "Grafana dashboard"},
			{Wrong: "o auth", Right: "OAuth"},
			{Wrong: "web hook", Right: "webhook"},
			{Wrong: "pull rec quest", Right: "pull request"},
			{Wrong: "anthropic's", Right: "Anthropic's"},
			{Wrong: "skew", Right: "SKU"},
			{Wrong: "there roadmap", Right: "their roadmap"},
			{Wrong: "figma file", Right: "Figma file"},
		},
	}
}

// demoTexts are the fake dictations: raw as the recogniser might hear it, and
// the cleaned-up version. Written so the difference between the two is visible.
var demoTexts = []struct{ raw, cleaned string }{
	{"so i think we should ship the o auth change first and then look at the web hook retries next week",
		"I think we should ship the OAuth change first, and then look at the webhook retries next week."},
	{"can you review the pull rec quest i opened this morning it touches the post gres migration",
		"Could you review the pull request I opened this morning? It touches the PostgreSQL migration."},
	{"quick note from the sprint retrospective the team wants shorter stand ups and more written updates",
		"Quick note from the sprint retrospective: the team wants shorter stand-ups and more written updates."},
	{"the grafana dashboard is showing a latency spike around eleven every morning worth digging into",
		"The Grafana dashboard is showing a latency spike around eleven every morning — worth digging into."},
	{"lets move the staging environment to the new cluster before we touch anything in production",
		"Let's move the staging environment to the new cluster before we touch anything in production."},
	{"reply to marta saying the q3 roadmap looks good but we need a date for the onboarding rewrite",
		"Reply to Marta: the Q3 roadmap looks good, but we need a date for the onboarding rewrite."},
	{"add to the changelog fixed a crash when the microphone is unplugged during a recording",
		"Add to the changelog: fixed a crash when the microphone is unplugged during a recording."},
	{"remind me to send the figma file to the design team before the review on thursday",
		"Remind me to send the Figma file to the design team before the review on Thursday."},
	{"the skew numbers in the export dont match the dashboard im pretty sure the filter is wrong",
		"The SKU numbers in the export don't match the dashboard. I'm fairly sure the filter is wrong."},
	{"writing this with vito while walking so it might need a bit of cleaning up afterwards",
		"Writing this with Vito while walking, so it might need a bit of cleaning up afterwards."},
	{"we should document the cuber netties setup properly nobody knows how the ingress is wired",
		"We should document the Kubernetes setup properly — nobody knows how the ingress is wired."},
	{"short one just confirming that the release is on friday and the freeze starts wednesday evening",
		"Short one: confirming that the release is on Friday and the freeze starts Wednesday evening."},
}

// History fabricates past dictations, newest first, spread over the recent days
// at plausible working hours. q filters on the visible text like the real store.
func History(now time.Time, q string, limit int) []history.Entry {
	if limit <= 0 {
		limit = 100
	}
	q = strings.ToLower(strings.TrimSpace(q))

	out := []history.Entry{}
	// Walk backwards day by day, a few dictations per day, until we have enough.
	for back := 0; back < 120 && len(out) < limit; back++ {
		d := now.AddDate(0, 0, -back)
		w, _, activations, _, _, _ := day(now, d)
		if w == 0 {
			continue
		}
		for i := 0; i < activations && len(out) < limit; i++ {
			h := dayHash(d.Format("2006-01-02"), uint32(i)*31+11)
			// Step through the texts with a stride coprime to their count, from a
			// per-day starting point: a day's entries never repeat themselves,
			// but two days rarely start on the same line either.
			txt := demoTexts[(int(dayHash(d.Format("2006-01-02"), 0))+i*5)%len(demoTexts)]
			// Working hours, latest first within the day.
			at := time.Date(d.Year(), d.Month(), d.Day(), 9+int(h%9), int(h/9%60), int(h/11%60), 0, now.Location())
			if at.After(now) {
				at = now.Add(-time.Duration(i+1) * 7 * time.Minute)
			}
			cleanupUsed := h%9 != 0 // a few entries never got cleaned up
			words := len(strings.Fields(txt.cleaned))
			e := history.Entry{
				ID:          "demo-" + d.Format("20060102") + "-" + string(rune('a'+i%26)),
				Timestamp:   at,
				DurationMS:  int64(words) * 460,
				Language:    "en",
				Source:      "stream",
				Raw:         txt.raw,
				CleanupUsed: cleanupUsed,
				SttMS:       220 + int64(h%400),
				InjectedMS:  30 + int64(h%50),
				Words:       words,
				Sentences:   strings.Count(txt.cleaned, ".") + strings.Count(txt.cleaned, "?"),
			}
			if cleanupUsed {
				e.Cleaned = txt.cleaned
				e.CleanupMS = 400 + int64(h%700)
				e.CleanupInTokens = int64(words)*3 + 700
				e.CleanupOutTokens = int64(words) * 2
			}
			if q != "" && !strings.Contains(strings.ToLower(e.Raw+" "+e.Cleaned), q) {
				continue
			}
			out = append(out, e)
		}
	}
	// Newest first, like the real store — the times within a day are scattered,
	// so they have to be ordered rather than appended as generated.
	sort.Slice(out, func(i, j int) bool { return out[i].Timestamp.After(out[j].Timestamp) })
	return out
}

// Entry looks up a fabricated entry by id, so the history page's re-inject
// button works in demo mode too.
func Entry(now time.Time, id string) (history.Entry, bool) {
	for _, e := range History(now, "", 500) {
		if e.ID == id {
			return e, true
		}
	}
	return history.Entry{}, false
}

// Transcript is the scripted dictation the daemon replays as live events, so
// the status page and the detached window show a transcript arriving word by
// word without a microphone ever opening.
func Transcript() []struct{ Raw, Cleaned string } {
	out := make([]struct{ Raw, Cleaned string }, 0, len(demoTexts))
	for _, t := range demoTexts {
		out = append(out, struct{ Raw, Cleaned string }{t.raw, t.cleaned})
	}
	return out
}
