package history

import (
	"time"

	"vito/internal/achievements"
)

// AchievementInputs gathers the figures the achievement definitions are checked
// against: lifetime totals, a best day and best week, the longest streak of
// consecutive days, how many distinct languages you've dictated in, and a few
// one-off flags (dictated at night, before dawn, or after a long absence).
//
// The cost-saving figure isn't set here — the server fills it in, since it owns
// the rates and the display currency.
func (s *Store) AchievementInputs(wpm float64) (achievements.Stats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var st achievements.Stats
	// Lifetime totals and the best single day come straight from the aggregates.
	row := s.db.QueryRow(`SELECT
		COALESCE(SUM(words),0), COALESCE(SUM(sentences),0), COALESCE(SUM(activations),0),
		COALESCE(SUM(duration_ms),0), COALESCE(SUM(uploads),0), COALESCE(MAX(words),0),
		COALESCE(SUM(commands),0), COALESCE(SUM(clipboard_commands),0)
		FROM day_stats`)
	var durationMS, clipCommands int64
	if err := row.Scan(&st.Words, &st.Sentences, &st.Activations, &durationMS, &st.Uploads, &st.BestDayWords, &st.Commands, &clipCommands); err != nil {
		return st, err
	}
	// Which of the three modes you've used, for the hidden "Vito Wizard" badge:
	// a plain dictation (an activation that wasn't a command), a spoken Assist
	// command (a command that wasn't on the clipboard), and a clipboard command.
	st.UsedDictation = st.Activations > st.Commands
	st.UsedAssist = st.Commands > clipCommands
	st.UsedClipboardAssist = clipCommands > 0
	st.SpokenSeconds = durationMS / 1000
	if wpm <= 0 {
		wpm = 40
	}
	// Same basis as the "saved typing time" card: words at your typing speed,
	// minus the time you actually spent speaking.
	if saved := float64(st.Words)/wpm - float64(durationMS)/60000.0; saved > 0 {
		st.SavedMinutes = int64(saved + 0.5)
	}

	// Best week, longest streak and the comeback flag all need the day-by-day
	// series, so read the active days once and walk them.
	rows, err := s.db.Query(`SELECT day, words FROM day_stats WHERE words > 0 OR uploads > 0 ORDER BY day`)
	if err != nil {
		return st, err
	}
	type dayRow struct {
		d     time.Time
		words int64
	}
	var days []dayRow
	for rows.Next() {
		var ds string
		var w int64
		if err := rows.Scan(&ds, &w); err != nil {
			rows.Close()
			return st, err
		}
		if d, err := time.ParseInLocation("2006-01-02", ds, time.Local); err == nil {
			days = append(days, dayRow{d, w})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return st, err
	}

	// Longest run of consecutive calendar days; best sum over any 7-day window;
	// and whether there was ever a gap of 30+ days followed by more activity.
	var streak, longest int64
	var window []dayRow
	var prev time.Time
	for i, d := range days {
		if i > 0 {
			gap := int(d.d.Sub(prev).Hours()/24 + 0.5)
			if gap == 1 {
				streak++
			} else {
				if gap >= 30 {
					st.Comeback = true
				}
				streak = 1
			}
		} else {
			streak = 1
		}
		if streak > longest {
			longest = streak
		}
		// Drop days that fell out of the trailing 7-day window, then sum.
		window = append(window, d)
		for len(window) > 0 && d.d.Sub(window[0].d).Hours() > 6*24 {
			window = window[1:]
		}
		var sum int64
		for _, w := range window {
			sum += w.words
		}
		if sum > st.BestWeekWords {
			st.BestWeekWords = sum
		}
		prev = d.d
	}
	st.LongestStreak = longest

	// Distinct dictation languages, and the time-of-day flags, come from the
	// entry rows. Those are capped, so this is best-effort — but an achievement
	// is only recorded once, so catching it while the entry is still around is
	// enough. Privacy-mode dictations leave no entry and can't set these.
	langRow := s.db.QueryRow(`SELECT COUNT(DISTINCT language) FROM history
		WHERE source != 'upload' AND language != '' AND language != 'auto'`)
	_ = langRow.Scan(&st.Languages)

	hourRows, err := s.db.Query(`SELECT ts FROM history WHERE source != 'upload'`)
	if err == nil {
		for hourRows.Next() {
			var ts int64
			if hourRows.Scan(&ts) == nil {
				h := time.UnixMilli(ts).In(time.Local).Hour()
				if h < 5 {
					st.Night = true
				} else if h < 7 {
					st.Early = true
				}
			}
		}
		hourRows.Close()
	}
	return st, nil
}

// UnlockedAchievements returns the ids already recorded, with when.
func (s *Store) UnlockedAchievements() (map[string]time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT id, unlocked_at FROM achievements`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var id string
		var at int64
		if err := rows.Scan(&id, &at); err != nil {
			return nil, err
		}
		out[id] = time.UnixMilli(at)
	}
	return out, rows.Err()
}

// RecordAchievements writes any of the given ids that aren't already recorded,
// stamped now, and returns the ids that were newly added. Order is preserved so
// a caller can announce them in a sensible sequence.
func (s *Store) RecordAchievements(ids []string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UnixMilli()
	var added []string
	for _, id := range ids {
		res, err := s.db.Exec(`INSERT OR IGNORE INTO achievements (id, unlocked_at) VALUES (?, ?)`, id, now)
		if err != nil {
			return added, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			added = append(added, id)
		}
	}
	return added, nil
}

// SetManualAchievement ticks or un-ticks an honour-system badge: on inserts it
// (stamped now, if not already there), off removes it.
func (s *Store) SetManualAchievement(id string, on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if on {
		_, err := s.db.Exec(`INSERT OR IGNORE INTO achievements (id, unlocked_at) VALUES (?, ?)`, id, time.Now().UnixMilli())
		return err
	}
	_, err := s.db.Exec(`DELETE FROM achievements WHERE id = ?`, id)
	return err
}

// AchievementsSeeded reports whether the one-time bootstrap has run. A brand-new
// install has an empty table; a long-time user's first run under this feature
// records everything already earned in one silent pass, so they aren't buried
// under a hundred pop-ups.
func (s *Store) AchievementsSeeded() (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM achievements WHERE id = '_seeded'`).Scan(&n)
	return n > 0, err
}

// MarkAchievementsSeeded plants the sentinel row.
func (s *Store) MarkAchievementsSeeded() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`INSERT OR IGNORE INTO achievements (id, unlocked_at) VALUES ('_seeded', ?)`, time.Now().UnixMilli())
	return err
}
