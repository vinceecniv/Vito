package cloudsync

import (
	"context"
	"io"
	"io/fs"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"vito/internal/config"
	"vito/internal/history"
)

// memRemote is a cloud drive in memory.
type memRemote struct {
	mu    sync.Mutex
	files map[string][]byte
}

func (m *memRemote) Get(_ context.Context, p string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.files[p]
	if !ok {
		return nil, ErrNotFound
	}
	return b, nil
}
func (m *memRemote) Put(_ context.Context, p string, b []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.files[p] = append([]byte(nil), b...)
	return nil
}
func (m *memRemote) List(_ context.Context, dir string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for p := range m.files {
		if rest, ok := strings.CutPrefix(p, dir+"/"); ok && !strings.Contains(rest, "/") {
			out = append(out, rest)
		}
	}
	return out, nil
}

type computer struct {
	e   *Engine
	h   *history.Store
	cfg config.Config
}

func newComputer(t *testing.T, id string) *computer {
	dir := t.TempDir()
	t.Setenv("AppData", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	h, err := history.NewStore(500, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	c := &computer{h: h, cfg: *config.Default()}
	c.cfg.Sync = config.Sync{Folder: "test", DeviceID: id, DeviceName: id}
	c.e = New(slog.New(slog.NewTextHandler(io.Discard, nil)), h,
		func() config.Config { return c.cfg },
		func(f func(*config.Config)) error { f(&c.cfg); return nil }, nil)
	c.e.StateFile = filepath.Join(dir, "state.json")
	return c
}

func (c *computer) sync(t *testing.T, r Remote) {
	t.Helper()
	if _, err := c.e.sync(context.Background(), r, c.cfg.Sync); err != nil {
		t.Fatal(err)
	}
}

func (c *computer) totals(t *testing.T) (entries, words, activations int) {
	t.Helper()
	n, _ := c.h.Count("", false)
	today := time.Now().Format("2006-01-02")
	w, _, a, _, _, _ := c.h.DayTotals(today, today)
	return n, w, a
}

func TestTwoComputers(t *testing.T) {
	r := &memRemote{files: map[string][]byte{}}
	now := time.Now()
	a := newComputer(t, "aaaa")
	a.h.Append(history.Entry{ID: "a1", Timestamp: now, Raw: "een twee drie", DurationMS: 1000})
	a.h.Append(history.Entry{ID: "a2", Timestamp: now, Raw: "vier", DurationMS: 1000})
	a.cfg.Dictionary.Keyterms = []string{"Vito"}
	b := newComputer(t, "bbbb")
	b.h.Append(history.Entry{ID: "b1", Timestamp: now, Raw: "vijf zes", DurationMS: 1000})
	b.cfg.Dictionary.Keyterms = []string{"Docker"}

	for range 2 { // a second round must change nothing
		a.sync(t, r)
		b.sync(t, r)
		a.sync(t, r)
		for name, c := range map[string]*computer{"a": a, "b": b} {
			if n, w, act := c.totals(t); n != 3 || w != 6 || act != 3 {
				t.Fatalf("%s: %d entries, %d words, %d activations; want 3, 6, 3", name, n, w, act)
			}
			if !slices.Contains(c.cfg.Dictionary.Keyterms, "Vito") || !slices.Contains(c.cfg.Dictionary.Keyterms, "Docker") {
				t.Fatalf("%s: dictionary %v, want both words", name, c.cfg.Dictionary.Keyterms)
			}
		}
	}

	// A word removed on one computer is removed on the other: the newest
	// dictionary wins once the first merge is done.
	a.cfg.Dictionary.Keyterms = []string{"Vito"}
	a.cfg.Sync.DictionaryAt = time.Now().UnixMilli() + 1000
	a.sync(t, r)
	b.sync(t, r)
	if slices.Contains(b.cfg.Dictionary.Keyterms, "Docker") {
		t.Fatalf("b still has the removed word: %v", b.cfg.Dictionary.Keyterms)
	}

	// B dictates again: only that comes across.
	b.h.Append(history.Entry{ID: "b2", Timestamp: now, Raw: "zeven", DurationMS: 1000})
	b.sync(t, r)
	a.sync(t, r)
	if n, w, act := a.totals(t); n != 4 || w != 7 || act != 4 {
		t.Fatalf("a after b's new dictation: %d entries, %d words, %d activations; want 4, 7, 4", n, w, act)
	}
}

// The same two computers through a real folder, as a sync app would share it.
func TestTwoComputersThroughAFolder(t *testing.T) {
	shared := t.TempDir()
	r, err := openFolder(shared)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	a := newComputer(t, "aaaa")
	a.h.Append(history.Entry{ID: "a1", Timestamp: now, Raw: "een twee drie", DurationMS: 1000})
	b := newComputer(t, "bbbb")
	b.h.Append(history.Entry{ID: "b1", Timestamp: now, Raw: "vier", DurationMS: 1000})
	a.sync(t, r)
	b.sync(t, r)
	a.sync(t, r)
	for name, c := range map[string]*computer{"a": a, "b": b} {
		if n, w, _ := c.totals(t); n != 2 || w != 4 {
			t.Fatalf("%s: %d entries, %d words; want 2, 4", name, n, w)
		}
	}
	// Nothing half-written is left for the sync app to pick up.
	_ = filepath.WalkDir(filepath.Join(shared, SubFolder), func(p string, d fs.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(p, ".tmp") {
			t.Fatalf("temporary file left behind: %s", p)
		}
		return nil
	})
}

// What the user does by hand travels too: deleting, starring, their own
// cleanup rules, and the costs and Assist counts in the day sums.
func TestSyncCarriesDeletesStarsRulesAndCosts(t *testing.T) {
	r := &memRemote{files: map[string][]byte{}}
	now := time.Now()
	a := newComputer(t, "aaaa")
	a.h.Append(history.Entry{ID: "a1", Timestamp: now, Raw: "een", DurationMS: 1000})
	a.h.Append(history.Entry{ID: "a2", Timestamp: now, Raw: "vertaald", DurationMS: 1000, Command: true, CommandText: "vertaal",
		CommandInTokens: 300, CommandOutTokens: 40})
	a.h.Append(history.Entry{ID: "a3", Timestamp: now, Raw: "drie", DurationMS: 1000, CleanupInTokens: 200, CleanupOutTokens: 20})
	b := newComputer(t, "bbbb")
	sync3 := func() { a.sync(t, r); b.sync(t, r); a.sync(t, r) }
	sync3()

	// Costs and Assist: b counts a's tokens and command.
	today := now.Format("2006-01-02")
	if _, _, in, out, cin, cout, _, _ := b.h.CostTotals(today, today); in != 200 || out != 20 || cin != 300 || cout != 40 {
		t.Fatalf("b's token totals %d %d %d %d, want 200 20 300 40", in, out, cin, cout)
	}
	if n, _ := b.h.CommandTotal(today, today); n != 1 {
		t.Fatalf("b counts %d commands, want 1", n)
	}

	// b deletes a1 and stars a3; a follows, and a1 does not come back.
	if err := b.h.Delete("a1"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if err := b.h.SetFavorite("a3", true); err != nil {
		t.Fatal(err)
	}
	// b changes the rule sets.
	b.cfg.Cleanup.Prompts = []config.Prompt{{ID: "p1", Name: "Kort", Rules: "Maak het kort."}}
	b.cfg.Sync.PromptsAt = time.Now().UnixMilli() + 1000
	b.sync(t, r)
	a.sync(t, r)
	b.sync(t, r)
	for name, c := range map[string]*computer{"a": a, "b": b} {
		if _, ok, _ := c.h.Get("a1"); ok {
			t.Fatalf("%s still has the deleted entry", name)
		}
		if favs, _ := c.h.FavoriteIDs(); !favs["a3"] {
			t.Fatalf("%s lost the star on a3", name)
		}
	}
	if len(a.cfg.Cleanup.Prompts) != 1 || a.cfg.Cleanup.Prompts[0].Rules != "Maak het kort." {
		t.Fatalf("a's rule sets %+v", a.cfg.Cleanup.Prompts)
	}
	// Un-starring on a wins over b's older star.
	time.Sleep(5 * time.Millisecond)
	a.h.SetFavorite("a3", false)
	sync3()
	if favs, _ := b.h.FavoriteIDs(); favs["a3"] {
		t.Fatal("b kept a star a removed later")
	}
}
