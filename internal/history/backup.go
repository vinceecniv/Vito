package history

import "time"

// Snapshot and Restore move the whole of a user's stored data — every history
// row, every permanent day aggregate, and every earned achievement — in and out
// of the database as plain values, so a backup can carry the lot. Audio
// recordings are deliberately not part of this: they live as separate WAV files
// and would dwarf everything else.

// DayStat is one row of the permanent per-day aggregate table.
type DayStat struct {
	Day              string `json:"day"`
	Words            int64  `json:"words"`
	Sentences        int64  `json:"sentences"`
	Activations      int64  `json:"activations"`
	DurationMS       int64  `json:"duration_ms"`
	CleanupInTokens  int64  `json:"cleanup_in_tokens"`
	CleanupOutTokens int64  `json:"cleanup_out_tokens"`
	Uploads          int64  `json:"uploads"`
	UploadDurationMS int64  `json:"upload_duration_ms"`
	UploadWords      int64  `json:"upload_words"`
}

// Unlock is one earned achievement with the millis it was first recorded.
type Unlock struct {
	ID         string `json:"id"`
	UnlockedAt int64  `json:"unlocked_at"`
}

// BackupData is the database half of a backup: the raw rows of the three tables
// a user's data lives in.
type BackupData struct {
	History      []Entry   `json:"history"`
	DayStats     []DayStat `json:"day_stats"`
	Achievements []Unlock  `json:"achievements"`
}

// Snapshot reads every row of the three data tables into plain values.
func (s *Store) Snapshot() (BackupData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var b BackupData

	rows, err := s.db.Query(`SELECT id,ts,duration_ms,language,source,raw,cleaned,cleanup_used,
		stt_ms,cleanup_ms,injected_ms,words,sentences,cleanup_in_tokens,cleanup_out_tokens,favorite
		FROM history ORDER BY ts`)
	if err != nil {
		return b, err
	}
	for rows.Next() {
		var e Entry
		var ts int64
		var used, favorite int
		if err := rows.Scan(&e.ID, &ts, &e.DurationMS, &e.Language, &e.Source, &e.Raw, &e.Cleaned,
			&used, &e.SttMS, &e.CleanupMS, &e.InjectedMS, &e.Words, &e.Sentences,
			&e.CleanupInTokens, &e.CleanupOutTokens, &favorite); err != nil {
			rows.Close()
			return b, err
		}
		e.Timestamp = time.UnixMilli(ts)
		e.CleanupUsed = used != 0
		e.Favorite = favorite != 0
		b.History = append(b.History, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return b, err
	}

	drows, err := s.db.Query(`SELECT day,words,sentences,activations,duration_ms,
		cleanup_in_tokens,cleanup_out_tokens,uploads,upload_duration_ms,upload_words
		FROM day_stats ORDER BY day`)
	if err != nil {
		return b, err
	}
	for drows.Next() {
		var d DayStat
		if err := drows.Scan(&d.Day, &d.Words, &d.Sentences, &d.Activations, &d.DurationMS,
			&d.CleanupInTokens, &d.CleanupOutTokens, &d.Uploads, &d.UploadDurationMS, &d.UploadWords); err != nil {
			drows.Close()
			return b, err
		}
		b.DayStats = append(b.DayStats, d)
	}
	drows.Close()
	if err := drows.Err(); err != nil {
		return b, err
	}

	arows, err := s.db.Query(`SELECT id,unlocked_at FROM achievements ORDER BY unlocked_at`)
	if err != nil {
		return b, err
	}
	for arows.Next() {
		var u Unlock
		if err := arows.Scan(&u.ID, &u.UnlockedAt); err != nil {
			arows.Close()
			return b, err
		}
		b.Achievements = append(b.Achievements, u)
	}
	arows.Close()
	return b, arows.Err()
}

// Restore replaces all stored data with the given snapshot, in one transaction:
// either the whole set is swapped in or nothing changes. The history row cap is
// re-applied afterwards, in case the backup predates a lower cap.
func (s *Store) Restore(b BackupData) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, table := range []string{"history", "day_stats", "achievements"} {
		if _, err := tx.Exec("DELETE FROM " + table); err != nil {
			return err
		}
	}
	for _, e := range b.History {
		if _, err := tx.Exec(
			`INSERT OR REPLACE INTO history
			 (id,ts,duration_ms,language,source,raw,cleaned,cleanup_used,stt_ms,cleanup_ms,injected_ms,words,sentences,cleanup_in_tokens,cleanup_out_tokens,favorite)
			 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			e.ID, e.Timestamp.UnixMilli(), e.DurationMS, e.Language, e.Source, e.Raw, e.Cleaned,
			b2i(e.CleanupUsed), e.SttMS, e.CleanupMS, e.InjectedMS, e.Words, e.Sentences,
			e.CleanupInTokens, e.CleanupOutTokens, b2i(e.Favorite)); err != nil {
			return err
		}
	}
	for _, d := range b.DayStats {
		if _, err := tx.Exec(
			`INSERT OR REPLACE INTO day_stats
			 (day,words,sentences,activations,duration_ms,cleanup_in_tokens,cleanup_out_tokens,uploads,upload_duration_ms,upload_words)
			 VALUES (?,?,?,?,?,?,?,?,?,?)`,
			d.Day, d.Words, d.Sentences, d.Activations, d.DurationMS,
			d.CleanupInTokens, d.CleanupOutTokens, d.Uploads, d.UploadDurationMS, d.UploadWords); err != nil {
			return err
		}
	}
	for _, u := range b.Achievements {
		if u.ID == "" {
			continue
		}
		if _, err := tx.Exec(`INSERT OR REPLACE INTO achievements (id,unlocked_at) VALUES (?,?)`,
			u.ID, u.UnlockedAt); err != nil {
			return err
		}
	}
	if s.maxEntries > 0 {
		if _, err := tx.Exec(
			`DELETE FROM history WHERE favorite=0 AND id NOT IN
			 (SELECT id FROM history WHERE favorite=0 ORDER BY ts DESC LIMIT ?)`, s.maxEntries); err != nil {
			return err
		}
	}
	return tx.Commit()
}
