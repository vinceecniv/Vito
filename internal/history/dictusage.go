package history

import (
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
