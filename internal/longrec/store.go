// Package longrec is Vito's mode for long recordings — lectures, meetings,
// long dictation sessions — beside ordinary dictation (docs/long-recordings.md).
//
// A session lives in a folder of its own: the audio as one WAV, and
// append-only logs of the transcribed blocks, their cleaned text and the
// events (marks, headers, pauses). Nothing is rewritten in place, so a crash
// loses at most the block in progress, which is recovered from the audio.
package longrec

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Session states.
const (
	StateRecording   = "recording"
	StatePaused      = "paused"
	StateInterrupted = "interrupted" // the daemon stopped mid-session; restore or discard
	StateDone        = "done"
)

// Meta is a session's session.json.
type Meta struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	Started  time.Time `json:"started"`
	State    string    `json:"state"`
	Seconds  float64   `json:"seconds"` // audio length, pauses not counted
	Provider string    `json:"provider,omitempty"`
	Language string    `json:"language,omitempty"`
	Source   string    `json:"source,omitempty"` // "upload" for an audio file
	Words    int       `json:"words,omitempty"`
}

// Block is one line of blocks.jsonl: a piece of the recording between two
// pauses, as the speech service wrote it.
type Block struct {
	Index int     `json:"i"`
	Start float64 `json:"start"` // seconds of audio
	End   float64 `json:"end"`
	Text  string  `json:"text"`
	Error string  `json:"error,omitempty"` // transcription failed; the audio is still there
}

// Clean is one line of clean.jsonl: a block's cleaned text. A later line for
// the same block (a re-clean) wins.
type Clean struct {
	Index int    `json:"i"`
	Text  string `json:"text"`
	Error string `json:"error,omitempty"` // cleanup failed; the raw text stands
}

// Event is one line of events.jsonl.
type Event struct {
	ID    string  `json:"id"`
	Type  string  `json:"type"` // mark, header, title, note, pause, resume, delete
	At    float64 `json:"at"`   // seconds of audio
	Of    string  `json:"of,omitempty"`
	Title string  `json:"title,omitempty"`
	Auto  bool    `json:"auto,omitempty"` // a title the cleanup pass proposed
	Text  string  `json:"text,omitempty"` // a note
}

// Store is the folder that holds every session.
type Store struct {
	dir string
	mu  sync.Mutex // serialises appends and meta writes
}

// NewStore opens <UserConfigDir>/vito/sessions, or dir when given (tests).
func NewStore(dir string) (*Store, error) {
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return nil, err
		}
		dir = filepath.Join(base, "vito", "sessions")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

func newID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return time.Now().Format("20060102-150405") + "-" + hex.EncodeToString(b[:])
}

// ValidID guards every id that arrives from outside before it names a folder.
func ValidID(id string) bool {
	if len(id) < 8 || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r == '-') {
			return false
		}
	}
	return true
}

func (s *Store) path(id, name string) string { return filepath.Join(s.dir, id, name) }

// AudioPath is the session's WAV.
func (s *Store) AudioPath(id string) string { return s.path(id, "audio.wav") }

func (s *Store) create(m Meta) error {
	if err := os.MkdirAll(filepath.Join(s.dir, m.ID), 0o700); err != nil {
		return err
	}
	return s.writeMeta(m)
}

func (s *Store) writeMeta(m Meta) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	p := s.path(m.ID, "session.json")
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Meta reads one session's session.json.
func (s *Store) Meta(id string) (Meta, error) {
	var m Meta
	if !ValidID(id) {
		return m, fmt.Errorf("invalid session id")
	}
	b, err := os.ReadFile(s.path(id, "session.json"))
	if err != nil {
		return m, err
	}
	err = json.Unmarshal(b, &m)
	return m, err
}

// UpdateMeta changes a session's session.json.
func (s *Store) UpdateMeta(id string, change func(*Meta)) (Meta, error) {
	m, err := s.Meta(id)
	if err != nil {
		return m, err
	}
	change(&m)
	return m, s.writeMeta(m)
}

func (s *Store) appendLine(id, name string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.OpenFile(s.path(id, name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// readLines decodes a .jsonl file line by line; a torn last line (a crash
// mid-write) is skipped.
func readLines[T any](path string) ([]T, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []T
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var v T
		if json.Unmarshal([]byte(line), &v) == nil {
			out = append(out, v)
		}
	}
	return out, sc.Err()
}

// AddBlock appends a transcribed block.
func (s *Store) AddBlock(id string, b Block) error { return s.appendLine(id, "blocks.jsonl", b) }

// AddClean appends a block's cleaned text.
func (s *Store) AddClean(id string, c Clean) error { return s.appendLine(id, "clean.jsonl", c) }

// AddEvent appends an event, giving it an id when it has none.
func (s *Store) AddEvent(id string, e Event) (Event, error) {
	if e.ID == "" {
		var b [4]byte
		_, _ = rand.Read(b[:])
		e.ID = hex.EncodeToString(b[:])
	}
	return e, s.appendLine(id, "events.jsonl", e)
}

// Blocks reads the transcribed blocks, in order, the last line per index.
func (s *Store) Blocks(id string) ([]Block, error) {
	list, err := readLines[Block](s.path(id, "blocks.jsonl"))
	if err != nil {
		return nil, err
	}
	byIdx := map[int]Block{}
	for _, b := range list {
		byIdx[b.Index] = b
	}
	out := make([]Block, 0, len(byIdx))
	for _, b := range byIdx {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	return out, nil
}

// Cleans reads the cleaned text per block index, the last line winning.
func (s *Store) Cleans(id string) (map[int]Clean, error) {
	list, err := readLines[Clean](s.path(id, "clean.jsonl"))
	if err != nil {
		return nil, err
	}
	out := map[int]Clean{}
	for _, c := range list {
		out[c.Index] = c
	}
	return out, nil
}

// ResetCleans forgets every cleaned block, for a re-clean of the whole text.
func (s *Store) ResetCleans(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := os.Remove(s.path(id, "clean.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Events reads the events in the order they were written.
func (s *Store) Events(id string) ([]Event, error) {
	return readLines[Event](s.path(id, "events.jsonl"))
}

// List returns every session, newest first.
func (s *Store) List() ([]Meta, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var out []Meta
	for _, e := range entries {
		if !e.IsDir() || !ValidID(e.Name()) {
			continue
		}
		if m, err := s.Meta(e.Name()); err == nil {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.After(out[j].Started) })
	return out, nil
}

// Delete removes a session with everything in it.
func (s *Store) Delete(id string) error {
	if !ValidID(id) {
		return fmt.Errorf("invalid session id")
	}
	return os.RemoveAll(filepath.Join(s.dir, id))
}

// Recover marks every session the daemon left recording or paused as
// interrupted, its audio's header repaired, and returns them. Run it once at
// start-up, before a new session can begin.
func (s *Store) Recover() []Meta {
	list, _ := s.List()
	var out []Meta
	for _, m := range list {
		if m.State != StateRecording && m.State != StatePaused {
			continue
		}
		sec, err := repairWAV(s.AudioPath(m.ID))
		if err != nil {
			sec = m.Seconds
		}
		m, err = s.UpdateMeta(m.ID, func(m *Meta) {
			m.State = StateInterrupted
			m.Seconds = sec
		})
		if err == nil {
			out = append(out, m)
		}
	}
	return out
}
