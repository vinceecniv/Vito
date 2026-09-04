// Package history stores dictation entries in a local SQLite database and
// derives usage statistics from them. Raw and cleaned text are both kept so
// cleanup quality stays auditable. Settings and the dictionary stay in the
// JSON config; only history + stats live in the DB (append-heavy, queryable).
package history

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no cgo)
)

// SourceUpload marks an entry that came from a file the user handed to Vito
// instead of from the microphone.
const SourceUpload = "upload"

type Entry struct {
	ID          string    `json:"id"`
	Timestamp   time.Time `json:"timestamp"`
	DurationMS  int64     `json:"duration_ms"`
	Language    string    `json:"language"`
	Source      string    `json:"source"` // stream | async
	Raw         string    `json:"raw"`
	Cleaned     string    `json:"cleaned,omitempty"` // empty when cleanup was off or fell back
	CleanupUsed bool      `json:"cleanup_used"`
	// CleanupError is why the AI pass failed, when one was attempted and errored
	// (empty otherwise, including when cleanup was off). The raw transcript went
	// in instead, and a toast that has since disappeared is the only other place
	// that reason ever existed — keeping it on the entry is what lets the history
	// explain an unpolished dictation hours later.
	CleanupError string `json:"cleanup_error,omitempty"`
	SttMS        int64  `json:"stt_ms"`
	CleanupMS    int64  `json:"cleanup_ms"`
	InjectedMS   int64  `json:"injected_ms"`
	Words        int    `json:"words"`                  // word count of the injected text
	Sentences    int    `json:"sentences"`              // sentence count of the injected text
	Command      bool   `json:"command,omitempty"`      // a Vito Assist voice command ("Vito, …") drove this dictation
	CommandText  string `json:"command_text,omitempty"` // the instruction itself ("vertaal naar Duits"), for the history label
	// ClipboardCommand marks a Vito Assist command that worked on the clipboard
	// (vs. a spoken follow-up). Not stored per row — only folded into the daily
	// counter, which is enough to tell the two modes apart for achievements.
	ClipboardCommand bool `json:"-"`
	// Cleanup token usage, for cost estimation (0 when no cleanup ran). When a
	// Vito Assist command drove this dictation the cleaner's tokens go to the
	// command fields below instead — it's Q&A/transformation, not tidy-up — so
	// the two cost lines stay honest and never double-count.
	CleanupInTokens  int64 `json:"cleanup_in_tokens,omitempty"`
	CleanupOutTokens int64 `json:"cleanup_out_tokens,omitempty"`
	CommandInTokens  int64 `json:"command_in_tokens,omitempty"`
	CommandOutTokens int64 `json:"command_out_tokens,omitempty"`
	// Favorite marks an entry the user starred: it is kept out of the automatic
	// pruning (the row cap and the age-based auto-delete) and floats to the top of
	// search results.
	Favorite bool `json:"favorite"`
}

// maxCleanupError caps the stored failure reason.
const maxCleanupError = 2000

// Text returns what was actually injected.
func (e Entry) Text() string {
	if e.CleanupUsed && e.Cleaned != "" {
		return e.Cleaned
	}
	return e.Raw
}

type Store struct {
	mu            sync.Mutex
	db            *sql.DB
	maxEntries    int
	retentionDays int // 0 = keep forever; older non-favorite entries are pruned
}

// NewStore opens (creating if needed) the SQLite database in the config dir and
// imports any legacy history.jsonl on first run.
func NewStore(maxEntries, retentionDays int) (*Store, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return nil, err
	}
	base := filepath.Join(dir, "vito")
	if err := os.MkdirAll(base, 0o700); err != nil {
		return nil, err
	}
	dbPath := filepath.Join(base, "vito.db")
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // serialise writers; SQLite is single-writer
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	// Best-effort migration for day_stats created before these columns existed.
	for _, col := range []string{"cleanup_in_tokens", "cleanup_out_tokens", "uploads", "upload_duration_ms", "upload_words", "commands", "command_in_tokens", "command_out_tokens", "clipboard_commands"} {
		_, _ = db.Exec("ALTER TABLE day_stats ADD COLUMN " + col + " INTEGER NOT NULL DEFAULT 0")
	}
	// Same, for the history table's per-entry token columns (added later than the
	// table). Rows written before this stay at 0 — the tokens were never kept
	// per entry back then, only in the daily aggregate.
	for _, col := range []string{"cleanup_in_tokens", "cleanup_out_tokens", "command_in_tokens", "command_out_tokens"} {
		_, _ = db.Exec("ALTER TABLE history ADD COLUMN " + col + " INTEGER NOT NULL DEFAULT 0")
	}
	// Favorite flag (added later than the table); rows written before this stay 0.
	_, _ = db.Exec("ALTER TABLE history ADD COLUMN favorite INTEGER NOT NULL DEFAULT 0")
	// The Vito Assist instruction column (added later than the table).
	_, _ = db.Exec("ALTER TABLE history ADD COLUMN command_text TEXT NOT NULL DEFAULT ''")
	// The failed-cleanup reason (added later than the table); older rows keep ''
	// — the reason was never stored back then, not even for the ones that failed.
	_, _ = db.Exec("ALTER TABLE history ADD COLUMN cleanup_error TEXT NOT NULL DEFAULT ''")
	s := &Store{db: db, maxEntries: maxEntries, retentionDays: retentionDays}
	s.importLegacy(filepath.Join(base, "history.jsonl"))
	s.backfillDayStats()
	return s, nil
}

// SetRetentionDays updates the age-based auto-delete window live (0 = off), so a
// settings change takes effect without a restart.
func (s *Store) SetRetentionDays(days int) {
	s.mu.Lock()
	s.retentionDays = days
	s.mu.Unlock()
}

// backfillDayStats seeds the aggregate table from existing history once (e.g.
// after upgrading), so stats already present before this table existed appear.
func (s *Store) backfillDayStats() {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM day_stats`).Scan(&n); err != nil || n > 0 {
		return // already populated (or query failed) — don't double-count
	}
	_, _ = s.db.Exec(`
		INSERT INTO day_stats (day, words, sentences, activations, duration_ms)
		SELECT date(ts/1000, 'unixepoch', 'localtime'),
		       COALESCE(SUM(words),0), COALESCE(SUM(sentences),0),
		       COUNT(*), COALESCE(SUM(duration_ms),0)
		FROM history GROUP BY 1`)
}

const schema = `
CREATE TABLE IF NOT EXISTS history (
  id           TEXT PRIMARY KEY,
  ts           INTEGER NOT NULL,   -- unix millis
  duration_ms  INTEGER,
  language     TEXT,
  source       TEXT,
  raw          TEXT,
  cleaned      TEXT,
  cleanup_used INTEGER,
  -- Why the AI pass failed, when it did ('' = it didn't, or never ran). Stored
  -- so the history can explain a dictation that came out unpolished.
  cleanup_error TEXT NOT NULL DEFAULT '',
  stt_ms       INTEGER,
  cleanup_ms   INTEGER,
  injected_ms  INTEGER,
  words        INTEGER,
  sentences    INTEGER,
  -- The Vito Assist instruction that drove this dictation ('' = a plain
  -- dictation), so the history can label it and show the original command.
  command_text TEXT NOT NULL DEFAULT '',
  -- Per-entry cleanup token usage. day_stats keeps the same numbers summed per
  -- day; storing them here too lets the hourly chart cost a single hour the same
  -- way the cost card and the daily bars do, instead of leaving out the AI pass.
  cleanup_in_tokens  INTEGER NOT NULL DEFAULT 0,
  cleanup_out_tokens INTEGER NOT NULL DEFAULT 0,
  -- Same, but for a Vito Assist command's cleaner call (Q&A/transform tokens),
  -- kept apart so the cost breakdown can bill "assist" separately from cleanup.
  command_in_tokens  INTEGER NOT NULL DEFAULT 0,
  command_out_tokens INTEGER NOT NULL DEFAULT 0,
  -- Starred by the user: kept out of the row cap and the age-based auto-delete,
  -- and floated to the top of search results.
  favorite     INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_history_ts ON history(ts);

-- Achievements the user has earned, with the moment each was first recorded.
-- Deciding what is earned is deterministic from the aggregates above; this only
-- remembers when, so the UI can show a date and celebrate a new one once.
CREATE TABLE IF NOT EXISTS achievements (
  id          TEXT PRIMARY KEY,
  unlocked_at INTEGER NOT NULL   -- unix millis
);

-- Permanent per-day aggregates (numbers only, no transcript text). Never pruned,
-- so usage statistics survive the history row cap and can be viewed far back.
CREATE TABLE IF NOT EXISTS day_stats (
  day               TEXT PRIMARY KEY,   -- local calendar day, yyyy-mm-dd
  words             INTEGER NOT NULL DEFAULT 0,
  sentences         INTEGER NOT NULL DEFAULT 0,
  activations       INTEGER NOT NULL DEFAULT 0,
  duration_ms       INTEGER NOT NULL DEFAULT 0,
  cleanup_in_tokens  INTEGER NOT NULL DEFAULT 0,
  cleanup_out_tokens INTEGER NOT NULL DEFAULT 0,
  -- Vito Assist command tokens, billed apart from cleanup (see the history table).
  command_in_tokens  INTEGER NOT NULL DEFAULT 0,
  command_out_tokens INTEGER NOT NULL DEFAULT 0,
  -- Transcribed uploads are counted apart from dictations. They are billed the
  -- same way (so the cost card must see them) but they are not something you
  -- spoke, so letting them into "words dictated", "time spoken" or "typing time
  -- saved" would make those numbers mean something else entirely.
  uploads            INTEGER NOT NULL DEFAULT 0,
  upload_duration_ms INTEGER NOT NULL DEFAULT 0,
  upload_words       INTEGER NOT NULL DEFAULT 0,
  commands           INTEGER NOT NULL DEFAULT 0,  -- dictations driven by a Vito Assist voice command
  clipboard_commands INTEGER NOT NULL DEFAULT 0   -- of those, the ones that worked on the clipboard
);
`

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Append(e Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.ID == "" {
		e.ID = NewID()
	}
	e.fillDefaults()
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO history
		 (id,ts,duration_ms,language,source,raw,cleaned,cleanup_used,cleanup_error,stt_ms,cleanup_ms,injected_ms,words,sentences,cleanup_in_tokens,cleanup_out_tokens,command_in_tokens,command_out_tokens,command_text)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		e.ID, e.Timestamp.UnixMilli(), e.DurationMS, e.Language, e.Source, e.Raw, e.Cleaned,
		b2i(e.CleanupUsed), e.CleanupError, e.SttMS, e.CleanupMS, e.InjectedMS, e.Words, e.Sentences, e.CleanupInTokens, e.CleanupOutTokens, e.CommandInTokens, e.CommandOutTokens, e.CommandText)
	if err != nil {
		return err
	}
	s.foldDayStats(e)
	s.enforceCap()
	return nil
}

// enforceCap trims the history to the newest maxEntries rows, never touching
// favorites or the permanent day_stats aggregates. Caller holds s.mu.
func (s *Store) enforceCap() {
	if s.maxEntries <= 0 {
		return
	}
	// Keep the newest maxEntries rows, but never delete a favorite: they are
	// excluded from both the deletion and the "rows to keep" count.
	_, _ = s.db.Exec(
		`DELETE FROM history WHERE favorite=0 AND id NOT IN
		 (SELECT id FROM history WHERE favorite=0 ORDER BY ts DESC LIMIT ?)`, s.maxEntries)
}

// AppendUpload stores a transcribed audio file. It keeps the entry like any
// other, but folds into the upload counters rather than the dictation ones.
func (s *Store) AppendUpload(e Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.ID == "" {
		e.ID = NewID()
	}
	e.Source = SourceUpload
	e.fillDefaults()
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO history
		 (id,ts,duration_ms,language,source,raw,cleaned,cleanup_used,cleanup_error,stt_ms,cleanup_ms,injected_ms,words,sentences,cleanup_in_tokens,cleanup_out_tokens)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		e.ID, e.Timestamp.UnixMilli(), e.DurationMS, e.Language, e.Source, e.Raw, e.Cleaned,
		b2i(e.CleanupUsed), e.CleanupError, e.SttMS, e.CleanupMS, e.InjectedMS, e.Words, e.Sentences, e.CleanupInTokens, e.CleanupOutTokens)
	if err != nil {
		return err
	}
	day := e.Timestamp.Local().Format("2006-01-02")
	_, _ = s.db.Exec(
		`INSERT INTO day_stats (day, uploads, upload_duration_ms, upload_words, cleanup_in_tokens, cleanup_out_tokens)
		 VALUES (?,1,?,?,?,?)
		 ON CONFLICT(day) DO UPDATE SET
		   uploads=uploads+1, upload_duration_ms=upload_duration_ms+excluded.upload_duration_ms,
		   upload_words=upload_words+excluded.upload_words,
		   cleanup_in_tokens=cleanup_in_tokens+excluded.cleanup_in_tokens,
		   cleanup_out_tokens=cleanup_out_tokens+excluded.cleanup_out_tokens`,
		day, e.DurationMS, e.Words, e.CleanupInTokens, e.CleanupOutTokens)
	s.enforceCap()
	return nil
}

// AppendStatsOnly records a dictation in the permanent per-day aggregates without
// storing its transcript. Used in privacy mode so the Status-page statistics keep
// working while the spoken content is never written to disk.
func (s *Store) AppendStatsOnly(e Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e.fillDefaults()
	s.foldDayStats(e)
	return nil
}

// fillDefaults sets a timestamp and derives the word/sentence counts from the
// text when the caller didn't supply them.
func (e *Entry) fillDefaults() {
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now()
	}
	// A provider error can carry its whole response body; keep enough to diagnose
	// with and no more, so one bad day can't bloat the database.
	if len(e.CleanupError) > maxCleanupError {
		e.CleanupError = e.CleanupError[:maxCleanupError] + "…"
	}
	text := e.Text()
	if e.Words == 0 {
		e.Words = len(strings.Fields(text))
	}
	if e.Sentences == 0 {
		e.Sentences = countSentences(text)
	}
}

// foldDayStats adds one dictation to the permanent per-day aggregate (retained
// even when the raw history rows are later pruned or cleared). Caller holds s.mu.
func (s *Store) foldDayStats(e Entry) {
	day := e.Timestamp.Local().Format("2006-01-02")
	cmd, clip := 0, 0
	if e.Command {
		cmd = 1
	}
	if e.ClipboardCommand {
		clip = 1
	}
	_, _ = s.db.Exec(
		`INSERT INTO day_stats (day, words, sentences, activations, duration_ms, cleanup_in_tokens, cleanup_out_tokens, commands, command_in_tokens, command_out_tokens, clipboard_commands)
		 VALUES (?,?,?,1,?,?,?,?,?,?,?)
		 ON CONFLICT(day) DO UPDATE SET
		   words=words+excluded.words, sentences=sentences+excluded.sentences,
		   activations=activations+1, duration_ms=duration_ms+excluded.duration_ms,
		   cleanup_in_tokens=cleanup_in_tokens+excluded.cleanup_in_tokens,
		   cleanup_out_tokens=cleanup_out_tokens+excluded.cleanup_out_tokens,
		   commands=commands+excluded.commands,
		   command_in_tokens=command_in_tokens+excluded.command_in_tokens,
		   command_out_tokens=command_out_tokens+excluded.command_out_tokens,
		   clipboard_commands=clipboard_commands+excluded.clipboard_commands`,
		day, e.Words, e.Sentences, e.DurationMS, e.CleanupInTokens, e.CleanupOutTokens, cmd, e.CommandInTokens, e.CommandOutTokens, clip)
}

// FirstDataDay returns the earliest local day (yyyy-mm-dd) with any aggregate
// data, or "" when there is none yet.
func (s *Store) FirstDataDay() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var d sql.NullString
	if err := s.db.QueryRow(`SELECT MIN(day) FROM day_stats`).Scan(&d); err != nil {
		return "", err
	}
	return d.String, nil
}

// CostTotals sums the billable quantities (dictation duration and cleanup token
// usage) over [fromDay, toDay] (inclusive, local calendar-day strings), plus the
// earliest day with data in that range so callers can project per active day.
// An empty fromDay means all-time.
// Uploads are billed at a different (lower) rate than dictations, so their
// duration comes back separately.
func (s *Store) CostTotals(fromDay, toDay string) (durationMS, uploadMS, inTokens, outTokens, cmdInTokens, cmdOutTokens int64, firstDay string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	where := "day <= ?"
	args := []any{toDay}
	if fromDay != "" {
		where = "day >= ? AND day <= ?"
		args = []any{fromDay, toDay}
	}
	row := s.db.QueryRow(
		`SELECT COALESCE(SUM(duration_ms),0), COALESCE(SUM(upload_duration_ms),0),
		        COALESCE(SUM(cleanup_in_tokens),0), COALESCE(SUM(cleanup_out_tokens),0),
		        COALESCE(SUM(command_in_tokens),0), COALESCE(SUM(command_out_tokens),0),
		        COALESCE(MIN(day),'')
		 FROM day_stats WHERE `+where, args...)
	err = row.Scan(&durationMS, &uploadMS, &inTokens, &outTokens, &cmdInTokens, &cmdOutTokens, &firstDay)
	return
}

// HourTotals breaks one local calendar day down by hour, from the entry rows
// (day_stats only keeps daily sums). Only the day currently on screen is ever
// asked for, so the row cap is not a concern — but note that privacy-mode
// dictations keep their statistics without an entry, so they are missing here.
func (s *Store) HourTotals(day time.Time) (words, sentences, activations [24]int, durationMS [24]int64, inTokens, outTokens, cmdInTokens, cmdOutTokens [24]int64, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, day.Location())
	// Cleanup and command tokens are kept apart so each hour can be priced at the
	// right rate (a command may run on Assist's own, heavier model).
	rows, err := s.db.Query(`SELECT ts, words, sentences, duration_ms,
		cleanup_in_tokens, cleanup_out_tokens, command_in_tokens, command_out_tokens
		FROM history WHERE ts >= ? AND ts < ?`,
		start.UnixMilli(), start.AddDate(0, 0, 1).UnixMilli())
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var ts, dur, inTok, outTok, cmdIn, cmdOut int64
		var w, sn int
		if err = rows.Scan(&ts, &w, &sn, &dur, &inTok, &outTok, &cmdIn, &cmdOut); err != nil {
			return
		}
		h := time.UnixMilli(ts).In(day.Location()).Hour()
		words[h] += w
		sentences[h] += sn
		activations[h]++
		durationMS[h] += dur
		inTokens[h] += inTok
		outTokens[h] += outTok
		cmdInTokens[h] += cmdIn
		cmdOutTokens[h] += cmdOut
	}
	err = rows.Err()
	return
}

// DayTotals sums the aggregate table over [fromDay, toDay] (inclusive, local
// calendar-day strings). It also reports the earliest day with data in that
// range, so callers can average per active day. An empty fromDay means all-time.
func (s *Store) DayTotals(fromDay, toDay string) (words, sentences, activations int, durationMS int64, firstDay string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	where := "day <= ?"
	args := []any{toDay}
	if fromDay != "" {
		where = "day >= ? AND day <= ?"
		args = []any{fromDay, toDay}
	}
	row := s.db.QueryRow(
		`SELECT COALESCE(SUM(words),0), COALESCE(SUM(sentences),0),
		        COALESCE(SUM(activations),0), COALESCE(SUM(duration_ms),0),
		        COALESCE(MIN(day),'')
		 FROM day_stats WHERE `+where, args...)
	err = row.Scan(&words, &sentences, &activations, &durationMS, &firstDay)
	return
}

// CommandTotal sums Vito Assist commands over an inclusive day range (empty
// fromDay = everything up to toDay). Kept separate from DayTotals so the chart
// callers that don't need it aren't disturbed.
func (s *Store) CommandTotal(fromDay, toDay string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	where := "day <= ?"
	args := []any{toDay}
	if fromDay != "" {
		where = "day >= ? AND day <= ?"
		args = []any{fromDay, toDay}
	}
	var n int
	err := s.db.QueryRow(`SELECT COALESCE(SUM(commands),0) FROM day_stats WHERE `+where, args...).Scan(&n)
	return n, err
}

// List returns entries newest-first, optionally filtered by a case-insensitive
// substring query and/or to favorites only, capped at limit (0 = no cap). When a
// search query is given, favorites are listed before other matches; the plain
// (unsearched) view stays purely chronological.
func (s *Store) List(query string, favoritesOnly bool, limit, offset int) ([]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := `SELECT id,ts,duration_ms,language,source,raw,cleaned,cleanup_used,cleanup_error,stt_ms,cleanup_ms,injected_ms,words,sentences,command_text,favorite
	      FROM history`
	where, args := listWhere(query, favoritesOnly)
	q += where
	if strings.TrimSpace(query) != "" {
		q += ` ORDER BY favorite DESC, ts DESC`
	} else {
		q += ` ORDER BY ts DESC`
	}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
		// SQLite only honours OFFSET alongside a LIMIT.
		if offset > 0 {
			q += ` OFFSET ?`
			args = append(args, offset)
		}
	}
	rows, err := s.db.Query(q, args...)
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

// Count reports how many history entries match the (optional) search and
// favorites filter, so the UI can show a total alongside the page it has loaded.
func (s *Store) Count(query string, favoritesOnly bool) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	where, args := listWhere(query, favoritesOnly)
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM history`+where, args...).Scan(&n)
	return n, err
}

// listWhere builds the shared WHERE clause (and its args) for List and Count.
func listWhere(query string, favoritesOnly bool) (string, []any) {
	var conds []string
	var args []any
	if qq := strings.TrimSpace(query); qq != "" {
		conds = append(conds, "(raw LIKE ? OR cleaned LIKE ?)")
		like := "%" + qq + "%"
		args = append(args, like, like)
	}
	if favoritesOnly {
		conds = append(conds, "favorite=1")
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

func (s *Store) Get(id string) (Entry, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row := s.db.QueryRow(
		`SELECT id,ts,duration_ms,language,source,raw,cleaned,cleanup_used,cleanup_error,stt_ms,cleanup_ms,injected_ms,words,sentences,command_text,favorite
		 FROM history WHERE id=?`, id)
	e, err := scanEntry(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, err
	}
	return e, true, nil
}

// Clear removes every non-favorite entry (starred entries are the user's kept
// ones, so "clear all" leaves them) and returns the ids it deleted, so the caller
// can remove just those recordings and keep the favorites' audio.
func (s *Store) Clear() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids, err := s.idsWhere(`favorite=0`)
	if err != nil {
		return nil, err
	}
	if _, err := s.db.Exec(`DELETE FROM history WHERE favorite=0`); err != nil {
		return nil, err
	}
	return ids, nil
}

// Delete removes a single entry by id. Explicit per-entry deletion overrides the
// favorite flag — the star only guards against the automatic and bulk pruning.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`DELETE FROM history WHERE id=?`, id)
	return err
}

// FavoriteIDs returns the ids of all starred entries, so callers (the recordings
// pruner) can keep their audio regardless of the last-N cap.
func (s *Store) FavoriteIDs() (map[string]bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids, err := s.idsWhere(`favorite=1`)
	if err != nil {
		return nil, err
	}
	keep := make(map[string]bool, len(ids))
	for _, id := range ids {
		keep[id] = true
	}
	return keep, nil
}

// SetFavorite stars or unstars an entry.
func (s *Store) SetFavorite(id string, on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE history SET favorite=? WHERE id=?`, b2i(on), id)
	return err
}

// PruneOlderThan deletes non-favorite entries older than `days` days and returns
// the ids removed (so the caller can drop their recordings). A non-positive
// `days` keeps everything. Favorites are never pruned by age.
func (s *Store) PruneOlderThan(days int) ([]string, error) {
	if days <= 0 {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().AddDate(0, 0, -days).UnixMilli()
	ids, err := s.idsWhere(`favorite=0 AND ts < ?`, cutoff)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	if _, err := s.db.Exec(`DELETE FROM history WHERE favorite=0 AND ts < ?`, cutoff); err != nil {
		return nil, err
	}
	return ids, nil
}

// idsWhere returns the ids of history rows matching a WHERE condition. Caller
// holds s.mu.
func (s *Store) idsWhere(cond string, args ...any) ([]string, error) {
	rows, err := s.db.Query(`SELECT id FROM history WHERE `+cond, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

type scanner interface{ Scan(...any) error }

func scanEntry(sc scanner) (Entry, error) {
	var e Entry
	var ts int64
	var cleanup, favorite int
	if err := sc.Scan(&e.ID, &ts, &e.DurationMS, &e.Language, &e.Source, &e.Raw, &e.Cleaned,
		&cleanup, &e.CleanupError, &e.SttMS, &e.CleanupMS, &e.InjectedMS, &e.Words, &e.Sentences, &e.CommandText, &favorite); err != nil {
		return Entry{}, err
	}
	e.Timestamp = time.UnixMilli(ts)
	e.CleanupUsed = cleanup != 0
	e.Command = e.CommandText != ""
	e.Favorite = favorite != 0
	return e, nil
}

// importLegacy imports a pre-SQLite history.jsonl once, then renames it aside.
func (s *Store) importLegacy(jsonlPath string) {
	f, err := os.Open(jsonlPath)
	if err != nil {
		return
	}
	defer f.Close()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM history`).Scan(&n); err != nil || n > 0 {
		return // DB already has data; don't re-import
	}
	dec := json.NewDecoder(f)
	for {
		var e Entry
		if err := dec.Decode(&e); err != nil {
			break
		}
		_ = s.Append(e)
	}
	_ = os.Rename(jsonlPath, jsonlPath+".imported")
}

// NewID mints an entry id. Exported so a caller can name related artefacts
// (a kept audio recording) after the entry before it is stored.
func NewID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// countSentences counts sentence-terminating groups (., !, ?), collapsing runs.
func countSentences(text string) int {
	n, inTerm := 0, false
	for _, r := range text {
		if r == '.' || r == '!' || r == '?' {
			if !inTerm {
				n++
				inTerm = true
			}
		} else {
			inTerm = false
		}
	}
	if n == 0 && strings.TrimSpace(text) != "" {
		n = 1
	}
	return n
}
