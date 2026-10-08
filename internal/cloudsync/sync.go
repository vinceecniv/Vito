// Package cloudsync keeps Vito's dictations, statistics, achievements and
// dictionary the same on every computer, through a folder the user's own sync
// app (Dropbox, OneDrive, Nextcloud, iCloud Drive, Syncthing, a network share)
// keeps the same everywhere. Vito only reads and writes files there; there is
// no Vito server and no account in between.
//
// Every computer writes only its own files and reads everyone else's, so two
// computers never write the same file and nothing needs locking:
//
//	devices/<id>.json            who it is, its own day sums, achievements,
//	                             dictionary, and a signature per month of history
//	history/<id>/<yyyy-mm>.json  its own dictations of that month
//
// What a computer took over from another is marked as such in its history
// (history.SyncSource), so it never passes it on as its own. Entries are
// matched by id and day sums per source, which makes taking the same file over
// twice harmless. The dictionary is the one exception to "only your own": the
// most recent change, on whichever computer, wins — after a first connection
// has merged them.
package cloudsync

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"vito/internal/config"
	"vito/internal/history"
)

// readme is put in the sync folder, for whoever finds it there.
const readme = `This folder is kept by Vito (https://vito.talk) to keep your dictations,
statistics, achievements and dictionary the same on every computer.

Each computer writes only its own files here and reads the others'. Your sync
app moves them between computers; Vito itself talks to no server.

Don't edit these files. To stop syncing, use Settings → Backup in Vito.
`

// DeviceDoc is devices/<id>.json.
type DeviceDoc struct {
	// Deleted and Favorites are what the user did by hand here: entries
	// deleted, and stars set or cleared, with when. The other computers do
	// the same; for a star the latest change wins.
	Deleted   map[string]int64                  `json:"deleted,omitempty"`
	Favorites map[string]history.FavoriteChange `json:"favorites,omitempty"`
	// Prompts are the user's own cleanup rule sets; newest change wins, as
	// for the dictionary. Which set is active stays a per-computer setting.
	Prompts      []config.Prompt            `json:"prompts,omitempty"`
	PromptsAt    int64                      `json:"prompts_at,omitempty"`
	Device       string                     `json:"device"`
	Name         string                     `json:"name"`
	Updated      int64                      `json:"updated"` // unix ms
	Days         map[string]history.DaySums `json:"days"`
	Achievements []string                   `json:"achievements"`
	Dictionary   config.Dictionary          `json:"dictionary"`
	DictionaryAt int64                      `json:"dictionary_at"`
	// Months holds a signature per month of history; a changed signature is
	// the only reason to download that month again.
	Months map[string]string `json:"months"`
}

// Status is what the settings page shows.
type Status struct {
	Folder     string       `json:"folder,omitempty"`
	Device     string       `json:"device,omitempty"`
	Syncing    bool         `json:"syncing"`
	Last       int64        `json:"last,omitempty"` // unix ms of the last good sync
	Error      string       `json:"error,omitempty"`
	Devices    []DeviceInfo `json:"devices,omitempty"`
	Candidates []Candidate  `json:"candidates"` // sync folders found on this computer
}

type DeviceInfo struct {
	Name    string `json:"name"`
	Updated int64  `json:"updated"`
}

// state is what this computer remembers between syncs, next to the config.
type state struct {
	Pushed map[string]string            `json:"pushed"` // month -> signature uploaded
	Seen   map[string]map[string]string `json:"seen"`   // device -> month -> signature taken over
	Merged bool                         `json:"merged"` // the first-connection dictionary merge is done
}

// Engine runs the sync.
type Engine struct {
	log    *slog.Logger
	hist   *history.Store
	config func() config.Config
	// update changes the config, saves it and applies it.
	update   func(func(*config.Config)) error
	onChange func(Status)

	// StateFile overrides where the sync state is kept (tests).
	StateFile string

	mu      sync.Mutex
	st      Status
	kick    chan struct{}
	running sync.Mutex // one sync at a time
}

func New(log *slog.Logger, hist *history.Store, cfg func() config.Config, update func(func(*config.Config)) error, onChange func(Status)) *Engine {
	return &Engine{log: log, hist: hist, config: cfg, update: update, onChange: onChange, kick: make(chan struct{}, 1)}
}

func (e *Engine) Status() Status {
	e.mu.Lock()
	st := e.st
	e.mu.Unlock()
	c := e.config().Sync
	st.Folder, st.Device = c.Folder, c.DeviceName
	if st.Folder == "" {
		st.Candidates = Candidates()
	}
	return st
}

func (e *Engine) set(f func(*Status)) {
	e.mu.Lock()
	f(&e.st)
	e.mu.Unlock()
	if e.onChange != nil {
		e.onChange(e.Status())
	}
}

// Kick asks for a sync soon, e.g. after a dictation.
func (e *Engine) Kick() {
	select {
	case e.kick <- struct{}{}:
	default:
	}
}

// Run syncs every five minutes and shortly after a Kick, while connected.
func (e *Engine) Run(ctx context.Context) {
	t := time.NewTimer(15 * time.Second)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-e.kick:
			// Let a burst of dictations settle into one sync.
			select {
			case <-ctx.Done():
				return
			case <-time.After(20 * time.Second):
			}
		}
		if e.config().Sync.Folder != "" {
			if err := e.SyncNow(ctx); err != nil {
				e.log.Info("sync failed", "err", err)
			}
		}
		t.Reset(5 * time.Minute)
	}
}

// Connect starts syncing through folder, after checking Vito can write there.
func (e *Engine) Connect(folder string) error {
	folder = filepath.Clean(strings.TrimSpace(folder))
	r, err := openFolder(folder)
	if err != nil {
		return err
	}
	if err := r.Put(context.Background(), "README.txt", []byte(readme)); err != nil {
		return fmt.Errorf("Vito can't write in %s: %w", folder, err)
	}
	host, _ := os.Hostname()
	err = e.update(func(c *config.Config) {
		c.Sync.Folder = folder
		if c.Sync.DeviceID == "" {
			b := make([]byte, 8)
			_, _ = rand.Read(b)
			c.Sync.DeviceID = hex.EncodeToString(b)
		}
		if c.Sync.DeviceName == "" {
			c.Sync.DeviceName = host
		}
	})
	if err != nil {
		return err
	}
	_ = os.Remove(e.statePath()) // a new connection starts afresh
	e.set(func(s *Status) { *s = Status{} })
	go func() { _ = e.SyncNow(context.Background()) }()
	return nil
}

// Disconnect stops syncing. What is in the folder and what was synced here
// stays.
func (e *Engine) Disconnect() error {
	err := e.update(func(c *config.Config) { c.Sync.Folder = "" })
	e.set(func(s *Status) { *s = Status{} })
	return err
}

func (e *Engine) statePath() string {
	if e.StateFile != "" {
		return e.StateFile
	}
	p, err := config.Path()
	if err != nil {
		return filepath.Join(os.TempDir(), "vito-sync-state.json")
	}
	return filepath.Join(filepath.Dir(p), "sync-state.json")
}

func (e *Engine) loadState() state {
	var s state
	if b, err := os.ReadFile(e.statePath()); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	if s.Pushed == nil {
		s.Pushed = map[string]string{}
	}
	if s.Seen == nil {
		s.Seen = map[string]map[string]string{}
	}
	return s
}

func (e *Engine) saveState(s state) {
	b, _ := json.Marshal(s)
	_ = os.WriteFile(e.statePath(), b, 0o600)
}

// SyncNow pushes this computer's changes and takes over the others'.
func (e *Engine) SyncNow(ctx context.Context) error {
	e.running.Lock()
	defer e.running.Unlock()
	c := e.config().Sync
	if c.Folder == "" {
		return errors.New("sync is not set up")
	}
	e.set(func(s *Status) { s.Syncing = true })
	var devices []DeviceInfo
	remote, err := openFolder(c.Folder)
	if err == nil {
		devices, err = e.sync(ctx, remote, c)
	}
	e.set(func(s *Status) {
		s.Syncing = false
		s.Error = ""
		if err != nil {
			s.Error = err.Error()
			return
		}
		s.Last = time.Now().UnixMilli()
		s.Devices = devices
	})
	return err
}

func (e *Engine) sync(ctx context.Context, r Remote, c config.Sync) ([]DeviceInfo, error) {
	st := e.loadState()
	own := c.DeviceID

	// Take over first: a first connection merges the dictionaries, and what
	// this computer then publishes already includes that.
	names, err := r.List(ctx, "devices")
	if err != nil {
		return nil, err
	}
	var devices []DeviceInfo
	var others []DeviceDoc
	for _, n := range names {
		id := strings.TrimSuffix(n, ".json")
		if id == own || id == n {
			continue
		}
		b, err := r.Get(ctx, "devices/"+n)
		if err != nil {
			continue
		}
		var d DeviceDoc
		if json.Unmarshal(b, &d) != nil || d.Device != id {
			continue
		}
		others = append(others, d)
		devices = append(devices, DeviceInfo{d.Name, d.Updated})
		if err := e.takeOver(ctx, r, &st, d); err != nil {
			return nil, err
		}
	}
	e.applyDictionary(&st, others)

	// Then publish: changed months first, the device file last, so a reader
	// never sees a signature for a month file that isn't there yet.
	doc, months, err := e.ownDoc(c)
	if err != nil {
		return nil, err
	}
	for month, entries := range months {
		sig := doc.Months[month]
		if st.Pushed[month] == sig {
			continue
		}
		b, _ := json.Marshal(entries)
		if err := r.Put(ctx, "history/"+own+"/"+month+".json", b); err != nil {
			return nil, err
		}
		st.Pushed[month] = sig
	}
	b, _ := json.Marshal(doc)
	if err := r.Put(ctx, "devices/"+own+".json", b); err != nil {
		return nil, err
	}
	e.saveState(st)
	sort.Slice(devices, func(i, j int) bool { return devices[i].Updated > devices[j].Updated })
	return devices, nil
}

// takeOver imports another computer's months that changed since last time,
// its day sums and its achievements.
func (e *Engine) takeOver(ctx context.Context, r Remote, st *state, d DeviceDoc) error {
	seen := st.Seen[d.Device]
	if seen == nil {
		seen = map[string]string{}
	}
	// Deletions first, so the import below doesn't bring them back.
	if err := e.hist.ApplyDeleted(d.Deleted); err != nil {
		return err
	}
	var entries []history.Entry
	for month, sig := range d.Months {
		if seen[month] == sig {
			continue
		}
		b, err := r.Get(ctx, "history/"+d.Device+"/"+month+".json")
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		var es []history.Entry
		if err := json.Unmarshal(b, &es); err != nil {
			continue
		}
		entries = append(entries, es...)
		seen[month] = sig
	}
	if _, err := e.hist.Import(history.SyncSource(d.Device), entries, d.Days); err != nil {
		return err
	}
	if err := e.hist.ApplyFavorites(d.Favorites); err != nil {
		return err
	}
	if len(d.Achievements) > 0 {
		_, _ = e.hist.RecordAchievements(d.Achievements)
	}
	st.Seen[d.Device] = seen
	return nil
}

// applyDictionary makes the newest dictionary everyone's. The first time a
// computer connects it merges instead, so connecting a second computer never
// throws away the words of either.
func (e *Engine) applyDictionary(st *state, others []DeviceDoc) {
	if !st.Merged {
		_ = e.update(func(c *config.Config) {
			for _, d := range others {
				c.Dictionary = MergeDictionary(c.Dictionary, d.Dictionary)
				c.Cleanup.Prompts = mergePrompts(c.Cleanup.Prompts, d.Prompts)
			}
			now := time.Now().UnixMilli()
			c.Sync.DictionaryAt, c.Sync.PromptsAt = now, now
		})
		st.Merged = true
		return
	}
	var dict, rules *DeviceDoc
	for i := range others {
		if dict == nil || others[i].DictionaryAt > dict.DictionaryAt {
			dict = &others[i]
		}
		if rules == nil || others[i].PromptsAt > rules.PromptsAt {
			rules = &others[i]
		}
	}
	cur := e.config().Sync
	if dict != nil && dict.DictionaryAt > cur.DictionaryAt {
		_ = e.update(func(c *config.Config) {
			c.Dictionary = dict.Dictionary
			c.Sync.DictionaryAt = dict.DictionaryAt
		})
	}
	if rules != nil && rules.PromptsAt > cur.PromptsAt {
		_ = e.update(func(c *config.Config) {
			c.Cleanup.Prompts = append([]config.Prompt(nil), rules.Prompts...)
			c.Sync.PromptsAt = rules.PromptsAt
		})
	}
}

// ownDoc builds this computer's device file and its months of history.
func (e *Engine) ownDoc(c config.Sync) (DeviceDoc, map[string][]history.Entry, error) {
	cfg := e.config()
	doc := DeviceDoc{Device: c.DeviceID, Name: c.DeviceName, Updated: time.Now().UnixMilli(),
		Dictionary: cfg.Dictionary, DictionaryAt: cfg.Sync.DictionaryAt,
		Prompts: cfg.Cleanup.Prompts, PromptsAt: cfg.Sync.PromptsAt, Months: map[string]string{}}
	var err error
	if doc.Days, err = e.hist.OwnDaySums(); err != nil {
		return doc, nil, err
	}
	if doc.Deleted, err = e.hist.Deleted(); err != nil {
		return doc, nil, err
	}
	if doc.Favorites, err = e.hist.FavoriteChanges(); err != nil {
		return doc, nil, err
	}
	if un, err := e.hist.UnlockedAchievements(); err == nil {
		for id := range un {
			doc.Achievements = append(doc.Achievements, id)
		}
		sort.Strings(doc.Achievements)
	}
	entries, err := e.hist.OwnEntries(time.Time{}, time.Now().Add(time.Hour))
	if err != nil {
		return doc, nil, err
	}
	months := map[string][]history.Entry{}
	for _, en := range entries {
		m := en.Timestamp.Format("2006-01")
		months[m] = append(months[m], en)
	}
	for m, es := range months {
		h := fnv.New64a()
		for _, en := range es {
			fmt.Fprintf(h, "%s|%t|%d;", en.ID, en.Favorite, len(en.Raw)+len(en.Cleaned))
		}
		doc.Months[m] = fmt.Sprintf("%d-%x", len(es), h.Sum64())
	}
	return doc, months, nil
}

// mergePrompts adds b's rule sets that a doesn't have (by id).
func mergePrompts(a, b []config.Prompt) []config.Prompt {
	out := append([]config.Prompt(nil), a...)
	have := map[string]bool{}
	for _, p := range out {
		have[p.ID] = true
	}
	for _, p := range b {
		if p.ID != "" && !have[p.ID] {
			have[p.ID] = true
			out = append(out, p)
		}
	}
	return out
}

// MergeDictionary adds b's keyterms and corrections to a's, skipping what a
// already has (ignoring case).
func MergeDictionary(a, b config.Dictionary) config.Dictionary {
	out := config.Dictionary{
		Keyterms:    append([]string(nil), a.Keyterms...),
		Corrections: append([]config.Correction(nil), a.Corrections...),
	}
	seen := map[string]bool{}
	for _, k := range out.Keyterms {
		seen[strings.ToLower(strings.TrimSpace(k))] = true
	}
	for _, k := range b.Keyterms {
		if key := strings.ToLower(strings.TrimSpace(k)); key != "" && !seen[key] {
			seen[key] = true
			out.Keyterms = append(out.Keyterms, k)
		}
	}
	wrong := map[string]bool{}
	for _, c := range out.Corrections {
		wrong[strings.ToLower(strings.TrimSpace(c.Wrong))] = true
	}
	for _, c := range b.Corrections {
		if key := strings.ToLower(strings.TrimSpace(c.Wrong)); key != "" && !wrong[key] {
			wrong[key] = true
			out.Corrections = append(out.Corrections, c)
		}
	}
	return out
}
