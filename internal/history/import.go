package history

import (
	"strings"
	"time"
)

// dayCols are the day_stats columns that travel between computers (sync) or
// from the browser (a hand-over): everything a day counts, so statistics,
// costs, Vito Assist and uploads all add up across them.
var dayCols = []string{"words", "sentences", "activations", "duration_ms",
	"cleanup_in_tokens", "cleanup_out_tokens", "command_in_tokens", "command_out_tokens",
	"commands", "clipboard_commands", "uploads", "upload_duration_ms", "upload_words"}

// importedDaysSchema remembers, per source and day, what Import took over:
// so a repeat adds only what is new, and so this computer's own sums can be
// told apart from what came from elsewhere (OwnDaySums).
const importedDaysSchema = `CREATE TABLE IF NOT EXISTS imported_days (
  source TEXT NOT NULL, day TEXT NOT NULL,
  words INTEGER NOT NULL DEFAULT 0, sentences INTEGER NOT NULL DEFAULT 0,
  activations INTEGER NOT NULL DEFAULT 0, duration_ms INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (source, day))`

// What the user did by hand that sync carries to the other computers: a
// deletion, and a star switched on or off, each with when. Pruning by the
// row cap or the retention setting is not here — that is each computer's own.
const syncChangesSchema = `CREATE TABLE IF NOT EXISTS sync_deleted (id TEXT PRIMARY KEY, at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS sync_favorite (id TEXT PRIMARY KEY, favorite INTEGER NOT NULL, at INTEGER NOT NULL)`

// migrateImport brings the import and sync tables up to date; NewStore runs it.
func migrateImport(exec func(string) error) {
	_ = exec(importedDaysSchema)
	for _, c := range dayCols[4:] {
		_ = exec("ALTER TABLE imported_days ADD COLUMN " + c + " INTEGER NOT NULL DEFAULT 0")
	}
	for _, q := range strings.Split(syncChangesSchema, ";") {
		_ = exec(q)
	}
}

// syncPrefix marks what came from another computer through cloud sync. A
// browser hand-over ("web:…") is not marked so: those dictations moved here
// and are this computer's to share from now on.
const syncPrefix = "sync:"

// DaySums is one day of another store's permanent sums: the browser version
// keeps them next to its entries, as day_stats does here. The field names are
// the day_stats columns.
type DaySums struct {
	Words             int64 `json:"words"`
	Sentences         int64 `json:"sentences"`
	Activations       int64 `json:"activations"`
	DurationMS        int64 `json:"duration_ms"`
	CleanupInTokens   int64 `json:"cleanup_in_tokens,omitempty"`
	CleanupOutTokens  int64 `json:"cleanup_out_tokens,omitempty"`
	CommandInTokens   int64 `json:"command_in_tokens,omitempty"`
	CommandOutTokens  int64 `json:"command_out_tokens,omitempty"`
	Commands          int64 `json:"commands,omitempty"`
	ClipboardCommands int64 `json:"clipboard_commands,omitempty"`
	Uploads           int64 `json:"uploads,omitempty"`
	UploadDurationMS  int64 `json:"upload_duration_ms,omitempty"`
	UploadWords       int64 `json:"upload_words,omitempty"`
}

// fields lines the struct up with dayCols.
func (d *DaySums) fields() []*int64 {
	return []*int64{&d.Words, &d.Sentences, &d.Activations, &d.DurationMS,
		&d.CleanupInTokens, &d.CleanupOutTokens, &d.CommandInTokens, &d.CommandOutTokens,
		&d.Commands, &d.ClipboardCommands, &d.Uploads, &d.UploadDurationMS, &d.UploadWords}
}

func (d *DaySums) values() []any {
	var out []any
	for _, p := range d.fields() {
		out = append(out, *p)
	}
	return out
}

func (d *DaySums) scanArgs() []any {
	var out []any
	for _, p := range d.fields() {
		out = append(out, p)
	}
	return out
}

// Import takes over dictations made elsewhere — the browser version, when
// someone moves to the app, or another computer through sync. Entries with an
// id not yet here are stored with their own time, unless the user deleted
// them here. Each day's sums are then raised to what the other side counted,
// which also brings in the days whose entries it had already pruned.
//
// source names where the data comes from; what was taken from it is
// remembered per day, so importing the same source again only adds what is
// new since. It returns how many entries were added.
func (s *Store) Import(source string, entries []Entry, days map[string]DaySums) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	added := 0
	for _, e := range entries {
		if e.ID == "" || e.Timestamp.IsZero() || e.Source == SourceUpload {
			continue
		}
		var gone int
		if s.db.QueryRow(`SELECT 1 FROM sync_deleted WHERE id=?`, e.ID).Scan(&gone) == nil {
			continue // deleted here on purpose: don't bring it back
		}
		e.fillDefaults()
		res, err := s.db.Exec(
			`INSERT OR IGNORE INTO history
			 (id,ts,duration_ms,language,source,raw,cleaned,cleanup_used,cleanup_error,stt_ms,cleanup_ms,injected_ms,words,sentences,favorite,origin,command_text)
			 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			e.ID, e.Timestamp.UnixMilli(), e.DurationMS, e.Language, e.Source, e.Raw, e.Cleaned,
			b2i(e.CleanupUsed), e.CleanupError, e.SttMS, e.CleanupMS, e.InjectedMS, e.Words, e.Sentences, b2i(e.Favorite), source, e.CommandText)
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
	cols := strings.Join(dayCols, ",")
	marks := strings.TrimSuffix(strings.Repeat("?,", len(dayCols)), ",")
	var addSet, keepSet []string
	for _, c := range dayCols {
		addSet = append(addSet, c+"="+c+"+excluded."+c)
		keepSet = append(keepSet, c+"=excluded."+c)
	}
	for day, want := range days {
		if _, err := time.Parse("2006-01-02", day); err != nil {
			continue
		}
		var had DaySums
		_ = s.db.QueryRow(`SELECT `+cols+` FROM imported_days WHERE source=? AND day=?`, source, day).Scan(had.scanArgs()...)
		var d, keep DaySums
		changed := false
		wf, hf, df, kf := want.fields(), had.fields(), d.fields(), keep.fields()
		for i := range wf {
			*df[i] = max(0, *wf[i]-*hf[i])
			*kf[i] = max(*wf[i], *hf[i])
			changed = changed || *df[i] != 0
		}
		if !changed {
			continue
		}
		if _, err := s.db.Exec(`INSERT INTO day_stats (day,`+cols+`) VALUES (?,`+marks+`)
			ON CONFLICT(day) DO UPDATE SET `+strings.Join(addSet, ","), append([]any{day}, d.values()...)...); err != nil {
			return added, err
		}
		if _, err := s.db.Exec(`INSERT INTO imported_days (source,day,`+cols+`) VALUES (?,?,`+marks+`)
			ON CONFLICT(source, day) DO UPDATE SET `+strings.Join(keepSet, ","), append([]any{source, day}, keep.values()...)...); err != nil {
			return added, err
		}
	}
	s.enforceCap()
	return added, nil
}

// OwnEntries returns the entries dictated on this computer (or handed over to
// it) from from up to but not including to, oldest first: what sync shares.
func (s *Store) OwnEntries(from, to time.Time) ([]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT id,ts,duration_ms,language,source,raw,cleaned,cleanup_used,cleanup_error,stt_ms,cleanup_ms,injected_ms,words,sentences,command_text,favorite
		FROM history WHERE origin NOT LIKE ? AND source != ? AND ts >= ? AND ts < ? ORDER BY ts`,
		syncPrefix+"%", SourceUpload, from.UnixMilli(), to.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// OwnDaySums is day_stats without what sync brought in from other computers:
// the figures this computer shares.
func (s *Store) OwnDaySums() (map[string]DaySums, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var sel []string
	for _, c := range dayCols {
		sel = append(sel, "d."+c+" - COALESCE(SUM(i."+c+"),0)")
	}
	rows, err := s.db.Query(`SELECT d.day, `+strings.Join(sel, ", ")+`
		FROM day_stats d LEFT JOIN imported_days i ON i.day = d.day AND i.source LIKE ?
		GROUP BY d.day`, syncPrefix+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]DaySums{}
	for rows.Next() {
		var day string
		var d DaySums
		if err := rows.Scan(append([]any{&day}, d.scanArgs()...)...); err != nil {
			return nil, err
		}
		if d != (DaySums{}) {
			out[day] = d
		}
	}
	return out, rows.Err()
}

// SyncSource is the Import source for another computer's data.
func SyncSource(device string) string { return syncPrefix + device }

// noteDeleted records deletions by the user, for sync. Caller holds s.mu.
func (s *Store) noteDeleted(ids []string) {
	now := time.Now().UnixMilli()
	for _, id := range ids {
		_, _ = s.db.Exec(`INSERT OR REPLACE INTO sync_deleted (id, at) VALUES (?,?)`, id, now)
	}
}

// Deleted returns every entry the user deleted here (or sync deleted for
// them), id to when.
func (s *Store) Deleted() (map[string]int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.idTimes(`SELECT id, at FROM sync_deleted`)
}

// ApplyDeleted deletes what another computer's user deleted, and remembers it,
// so the entry isn't imported again from a third.
func (s *Store) ApplyDeleted(gone map[string]int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, at := range gone {
		if _, err := s.db.Exec(`DELETE FROM history WHERE id=?`, id); err != nil {
			return err
		}
		if _, err := s.db.Exec(`INSERT OR IGNORE INTO sync_deleted (id, at) VALUES (?,?)`, id, at); err != nil {
			return err
		}
	}
	return nil
}

// FavoriteChange is a star set or cleared, and when.
type FavoriteChange struct {
	Favorite bool  `json:"favorite"`
	At       int64 `json:"at"`
}

// FavoriteChanges returns the stars set or cleared by hand, id to change.
func (s *Store) FavoriteChanges() (map[string]FavoriteChange, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT id, favorite, at FROM sync_favorite`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]FavoriteChange{}
	for rows.Next() {
		var id string
		var fav int
		var at int64
		if err := rows.Scan(&id, &fav, &at); err != nil {
			return nil, err
		}
		out[id] = FavoriteChange{fav != 0, at}
	}
	return out, rows.Err()
}

// ApplyFavorites takes over another computer's star changes where they are
// newer than this one's: per entry, the latest change wins.
func (s *Store) ApplyFavorites(changes map[string]FavoriteChange) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, c := range changes {
		var at int64
		if s.db.QueryRow(`SELECT at FROM sync_favorite WHERE id=?`, id).Scan(&at) == nil && at >= c.At {
			continue
		}
		if _, err := s.db.Exec(`UPDATE history SET favorite=? WHERE id=?`, b2i(c.Favorite), id); err != nil {
			return err
		}
		if _, err := s.db.Exec(`INSERT OR REPLACE INTO sync_favorite (id, favorite, at) VALUES (?,?,?)`, id, b2i(c.Favorite), c.At); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) idTimes(q string) (map[string]int64, error) {
	rows, err := s.db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var id string
		var at int64
		if err := rows.Scan(&id, &at); err != nil {
			return nil, err
		}
		out[id] = at
	}
	return out, rows.Err()
}
