package history

import (
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Insights are the statistics that need the individual dictations rather than
// the per-day sums: how long you wait for the text, what the cleanup does to
// it, and which languages you dictate in. They come from the history
// rows, so they reach back only as far as the history does (the row cap keeps
// at least the last three months), and privacy-mode dictations, which leave no
// row, are not in them.
type Insights struct {
	Dictations int `json:"dictations"` // rows the figures below are drawn from

	// Stop to pasted, and its two main parts. Medians, because one slow call
	// would drag an average far from what a dictation usually feels like.
	LatencyMedianMS int `json:"latency_median_ms"`
	LatencyP95MS    int `json:"latency_p95_ms"`
	SttMedianMS     int `json:"stt_median_ms"`
	CleanupMedianMS int `json:"cleanup_median_ms"` // over dictations the cleanup ran on

	CleanupRuns    int `json:"cleanup_runs"`    // dictations the AI cleanup ran on
	CleanupChanged int `json:"cleanup_changed"` // ... and where it changed the text
	CleanupFailed  int `json:"cleanup_failed"`  // ... or tried and failed

	Languages []LangCount `json:"languages"` // most used first

	// TopWords are the words used most, for the word cloud: most used first,
	// common function words left out (see stopwords.go).
	TopWords []WordCount `json:"top_words"`
}

// WordCount is how often one word was dictated.
type WordCount struct {
	Word  string `json:"word"`
	Count int    `json:"count"`
}

// topWordsMax is how many words the cloud gets.
const topWordsMax = 40

// LangCount is how many dictations were in one language.
type LangCount struct {
	Code  string `json:"code"`
	Count int    `json:"count"`
}

// Insights gathers the per-dictation figures for from..to, both local
// midnights and both included; a zero from means from the start. Uploads are
// left out: their timing and language say nothing about dictating.
func (s *Store) Insights(from, to time.Time) (Insights, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	end := to.AddDate(0, 0, 1).UnixMilli()
	start := int64(0)
	if !from.IsZero() {
		start = from.UnixMilli()
	}
	rows, err := s.db.Query(`SELECT ts, language, raw, cleaned, cleanup_used, cleanup_error, stt_ms, cleanup_ms, injected_ms
		FROM history WHERE source != 'upload' AND ts >= ? AND ts < ?`, start, end)
	if err != nil {
		return Insights{}, err
	}
	defer rows.Close()

	var in Insights
	var latency, stt, cleanup []int
	langs := map[string]int{}
	words := map[string]int{}
	for rows.Next() {
		var ts, sttMS, cleanupMS, injectedMS int64
		var lang, raw, cleaned, cleanupErr string
		var used bool
		if err := rows.Scan(&ts, &lang, &raw, &cleaned, &used, &cleanupErr, &sttMS, &cleanupMS, &injectedMS); err != nil {
			return Insights{}, err
		}
		in.Dictations++
		if injectedMS > 0 {
			latency = append(latency, int(injectedMS))
		}
		if sttMS > 0 {
			stt = append(stt, int(sttMS))
		}
		switch {
		case cleanupErr != "":
			in.CleanupFailed++
		case used:
			in.CleanupRuns++
			if cleanupMS > 0 {
				cleanup = append(cleanup, int(cleanupMS))
			}
			if strings.TrimSpace(cleaned) != strings.TrimSpace(raw) {
				in.CleanupChanged++
			}
		}
		if lang = strings.ToLower(strings.TrimSpace(lang)); lang != "" && lang != "auto" {
			langs[lang]++
		}
		// The words come from what was pasted: the cleaned text, or the raw
		// transcript when cleanup didn't run.
		text := cleaned
		if strings.TrimSpace(text) == "" {
			text = raw
		}
		countWords(text, words)
	}
	if err := rows.Err(); err != nil {
		return Insights{}, err
	}

	in.LatencyMedianMS = percentile(latency, 50)
	in.LatencyP95MS = percentile(latency, 95)
	in.SttMedianMS = percentile(stt, 50)
	in.CleanupMedianMS = percentile(cleanup, 50)
	for code, n := range langs {
		in.Languages = append(in.Languages, LangCount{code, n})
	}
	sort.Slice(in.Languages, func(i, j int) bool {
		if in.Languages[i].Count != in.Languages[j].Count {
			return in.Languages[i].Count > in.Languages[j].Count
		}
		return in.Languages[i].Code < in.Languages[j].Code
	})
	in.TopWords = topWords(words, topWordsMax)
	return in, nil
}

// countWords adds the words of text to counts: lower-cased, split on anything
// that isn't a letter or digit (an apostrophe inside a word is kept, so "zo'n"
// and "don't" stay whole), and skipping words of three letters or fewer,
// numbers and stopwords — what's left is what you actually talk about.
func countWords(text string, counts map[string]int) {
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '\'' && r != '’'
	}) {
		w = strings.Trim(w, "'’")
		if utf8.RuneCountInString(w) <= 3 || stopwords[w] || strings.IndexFunc(w, unicode.IsLetter) < 0 {
			continue
		}
		counts[w]++
	}
}

// topWords returns the n most counted words, ties in alphabetical order. A word
// said only once isn't a pattern, so it stays out.
func topWords(counts map[string]int, n int) []WordCount {
	var out []WordCount
	for w, c := range counts {
		if c > 1 {
			out = append(out, WordCount{w, c})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Word < out[j].Word
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// CalendarWeeks is how far back the calendar heatmap reaches.
const CalendarWeeks = 26

// Calendar is words per day for the calendar heatmap: Words[i] belongs to
// Start plus i days. It always ends today, whatever period the rest of the
// page shows — a heatmap of one week would be a single column.
type Calendar struct {
	Start string `json:"start"` // yyyy-mm-dd, a Monday
	Words []int  `json:"words"`
}

// Calendar reads the words per day from the permanent day sums, from the
// Monday CalendarWeeks-1 weeks before today's week up to today.
func (s *Store) Calendar(now time.Time) (Calendar, error) {
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	start := today.AddDate(0, 0, -((int(today.Weekday())+6)%7)-7*(CalendarWeeks-1))
	cal := Calendar{Start: start.Format("2006-01-02"), Words: make([]int, daysBetween(start, today)+1)}

	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT day, words FROM day_stats WHERE day >= ? AND day <= ?`,
		cal.Start, today.Format("2006-01-02"))
	if err != nil {
		return cal, err
	}
	defer rows.Close()
	for rows.Next() {
		var day string
		var words int
		if err := rows.Scan(&day, &words); err != nil {
			return cal, err
		}
		if d, err := time.ParseInLocation("2006-01-02", day, now.Location()); err == nil {
			if i := daysBetween(start, d); i >= 0 && i < len(cal.Words) {
				cal.Words[i] = words
			}
		}
	}
	return cal, rows.Err()
}

// percentile returns the p-th percentile (nearest rank) of v, 0 for none. It
// sorts v in place.
func percentile(v []int, p int) int {
	if len(v) == 0 {
		return 0
	}
	sort.Ints(v)
	i := (p*len(v)+99)/100 - 1
	if i < 0 {
		i = 0
	}
	return v[i]
}
