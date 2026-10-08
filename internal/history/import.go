package history

import "time"

// DaySums is one day of another store's permanent sums: the browser version
// keeps them next to its entries, as day_stats does here.
type DaySums struct {
	Words       int   `json:"words"`
	Sentences   int   `json:"sentences"`
	Activations int   `json:"activations"`
	DurationMS  int64 `json:"duration_ms"`
}

// Import takes over dictations made elsewhere — the browser version, when
// someone moves to the app. Entries with an id not yet here are stored with
// their own time. Each day's sums are then raised to what the other side
// counted, which also brings in the days whose entries it had already
// pruned.
//
// source names where the data comes from; what was taken from it is
// remembered per day, so importing the same browser again only adds what is
// new since. It returns how many entries were added.
func (s *Store) Import(source string, entries []Entry, days map[string]DaySums) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS imported_days (
		source TEXT NOT NULL, day TEXT NOT NULL,
		words INTEGER NOT NULL DEFAULT 0, sentences INTEGER NOT NULL DEFAULT 0,
		activations INTEGER NOT NULL DEFAULT 0, duration_ms INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (source, day))`); err != nil {
		return 0, err
	}

	added := 0
	for _, e := range entries {
		if e.ID == "" || e.Timestamp.IsZero() || e.Source == SourceUpload {
			continue
		}
		e.fillDefaults()
		res, err := s.db.Exec(
			`INSERT OR IGNORE INTO history
			 (id,ts,duration_ms,language,source,raw,cleaned,cleanup_used,cleanup_error,stt_ms,cleanup_ms,injected_ms,words,sentences,favorite)
			 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			e.ID, e.Timestamp.UnixMilli(), e.DurationMS, e.Language, e.Source, e.Raw, e.Cleaned,
			b2i(e.CleanupUsed), e.CleanupError, e.SttMS, e.CleanupMS, e.InjectedMS, e.Words, e.Sentences, b2i(e.Favorite))
		if err != nil {
			return added, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			added++
		}
	}

	// The day sums cover every dictation there, entries or not, so they are
	// what is added — less whatever an earlier import from the same source
	// already brought in.
	for day, want := range days {
		if _, err := time.Parse("2006-01-02", day); err != nil {
			continue
		}
		var had DaySums
		_ = s.db.QueryRow(`SELECT words, sentences, activations, duration_ms FROM imported_days WHERE source=? AND day=?`,
			source, day).Scan(&had.Words, &had.Sentences, &had.Activations, &had.DurationMS)
		d := DaySums{
			Words:       max(0, want.Words-had.Words),
			Sentences:   max(0, want.Sentences-had.Sentences),
			Activations: max(0, want.Activations-had.Activations),
			DurationMS:  max(0, want.DurationMS-had.DurationMS),
		}
		if d == (DaySums{}) {
			continue
		}
		if _, err := s.db.Exec(
			`INSERT INTO day_stats (day, words, sentences, activations, duration_ms) VALUES (?,?,?,?,?)
			 ON CONFLICT(day) DO UPDATE SET words=words+excluded.words, sentences=sentences+excluded.sentences,
			   activations=activations+excluded.activations, duration_ms=duration_ms+excluded.duration_ms`,
			day, d.Words, d.Sentences, d.Activations, d.DurationMS); err != nil {
			return added, err
		}
		if _, err := s.db.Exec(
			`INSERT INTO imported_days (source, day, words, sentences, activations, duration_ms) VALUES (?,?,?,?,?,?)
			 ON CONFLICT(source, day) DO UPDATE SET words=excluded.words, sentences=excluded.sentences,
			   activations=excluded.activations, duration_ms=excluded.duration_ms`,
			source, day, max(want.Words, had.Words), max(want.Sentences, had.Sentences),
			max(want.Activations, had.Activations), max(want.DurationMS, had.DurationMS)); err != nil {
			return added, err
		}
	}
	s.enforceCap()
	return added, nil
}
