package history

import (
	"sort"
	"strings"
	"time"
)

// Insights are the statistics that need the individual dictations rather than
// the per-day sums: how long you wait for the text, what the cleanup does to
// it, which languages you dictate in, and when. They come from the history
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

	// Heatmap[weekday][hour] counts dictations; weekday 0 is Monday.
	Heatmap [7][24]int `json:"heatmap"`
}

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
		t := time.UnixMilli(ts).In(to.Location())
		in.Heatmap[(int(t.Weekday())+6)%7][t.Hour()]++
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
	return in, nil
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
