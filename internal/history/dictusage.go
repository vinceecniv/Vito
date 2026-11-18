package history

import (
	"vito/internal/dictionary"

	"strings"
	"time"
)

// DictUse is how often one dictionary entry was applied, and when last.
type DictUse struct {
	Uses   int64 `json:"uses"`
	LastAt int64 `json:"last_at"` // unix millis
}

// DictKey is the key an entry is counted under: its misheard word or
// trigger, trimmed, in lower case.
func DictKey(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// RecordDictUse counts one dictation's applied entries of a kind
// ("correction" or "snippet"), each key as often as it occurs.
func (s *Store) RecordDictUse(kind string, keys []string, at time.Time) error {
	if len(keys) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, k := range keys {
		if _, err := tx.Exec(`INSERT INTO dict_usage(kind, key, uses, last_at) VALUES(?, ?, 1, ?)
			ON CONFLICT(kind, key) DO UPDATE SET uses = uses + 1, last_at = MAX(last_at, excluded.last_at)`,
			kind, DictKey(k), at.UnixMilli()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DictUsage returns every count, by kind and key.
func (s *Store) DictUsage() (map[string]map[string]DictUse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT kind, key, uses, last_at FROM dict_usage`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]DictUse{"correction": {}, "snippet": {}}
	for rows.Next() {
		var kind, key string
		var u DictUse
		if err := rows.Scan(&kind, &key, &u.Uses, &u.LastAt); err != nil {
			return nil, err
		}
		if out[kind] == nil {
			out[kind] = map[string]DictUse{}
		}
		out[kind][key] = u
	}
	return out, rows.Err()
}

// DictPairs returns the raw and cleaned text of the newest dictations (at
// most limit), for dictionary.Suggest. Commands are left out.
func (s *Store) DictPairs(limit int) ([]dictionary.Pair, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT raw, COALESCE(cleaned, '') FROM history
		WHERE raw <> '' AND COALESCE(command_text, '') = '' ORDER BY ts DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []dictionary.Pair
	for rows.Next() {
		var p dictionary.Pair
		if err := rows.Scan(&p.Raw, &p.Cleaned); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DismissDictSuggestion remembers that the user turned a suggestion down.
func (s *Store) DismissDictSuggestion(wrong, right string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`INSERT OR REPLACE INTO dict_dismissed(wrong, right, at) VALUES(?, ?, ?)`,
		DictKey(wrong), DictKey(right), at.UnixMilli())
	return err
}

// DictDismissed returns the turned-down suggestions as "wrong\x00right".
func (s *Store) DictDismissed() (map[string]bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT wrong, right FROM dict_dismissed`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var w, r string
		if err := rows.Scan(&w, &r); err != nil {
			return nil, err
		}
		out[w+"\x00"+r] = true
	}
	return out, rows.Err()
}
