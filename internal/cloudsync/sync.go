// Package cloudsync keeps Vito's dictations, statistics, achievements and
// dictionary the same on every computer, through an app folder in the user's
// own Dropbox or OneDrive. There is no Vito server in between.
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

// DeviceDoc is devices/<id>.json.
type DeviceDoc struct {
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
	Provider  string         `json:"provider,omitempty"`
	Account   string         `json:"account,omitempty"`
	Device    string         `json:"device,omitempty"`
	Syncing   bool           `json:"syncing"`
	Last      int64          `json:"last,omitempty"` // unix ms of the last good sync
	Error     string         `json:"error,omitempty"`
	NeedsAuth bool           `json:"needs_auth,omitempty"`
	Devices   []DeviceInfo   `json:"devices,omitempty"`
	Providers []ProviderInfo `json:"providers"`
}

type DeviceInfo struct {
	Name    string `json:"name"`
	Updated int64  `json:"updated"`
}

type ProviderInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Available bool   `json:"available"` // this build has an app registration for it
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
	st.Provider, st.Account, st.Device = c.Provider, c.Account, c.DeviceName
	st.Providers = nil
	for _, p := range Providers() {
		st.Providers = append(st.Providers, ProviderInfo{p.ID, p.Name, p.ClientID != ""})
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
		if e.config().Sync.Provider != "" {
			if err := e.SyncNow(ctx); err != nil {
				e.log.Info("sync failed", "err", err)
			}
		}
		t.Reset(5 * time.Minute)
	}
}

// Connect stores a freshly authorised provider and syncs.
func (e *Engine) Connect(ctx context.Context, p *Provider, refresh string) error {
	r := Open(p, refresh, nil)
	account, _ := r.Account(ctx)
	host, _ := os.Hostname()
	err := e.update(func(c *config.Config) {
		c.Sync.Provider, c.Sync.RefreshToken, c.Sync.Account = p.ID, refresh, account
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

// Disconnect forgets the provider. What is in the cloud and what was synced
// here stays.
func (e *Engine) Disconnect() error {
	err := e.update(func(c *config.Config) { c.Sync.Provider, c.Sync.RefreshToken, c.Sync.Account = "", "", "" })
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
	p := ProviderByID(c.Provider)
	if p == nil || c.RefreshToken == "" {
		return errors.New("sync is not connected")
	}
	e.set(func(s *Status) { s.Syncing = true })
	remote := Open(p, c.RefreshToken, func(tok string) {
		_ = e.update(func(cc *config.Config) { cc.Sync.RefreshToken = tok })
	})
	devices, err := e.sync(ctx, remote, c)
	e.set(func(s *Status) {
		s.Syncing = false
		s.Error, s.NeedsAuth = "", false
		if err != nil {
			s.Error = err.Error()
			var ae *AuthError
			s.NeedsAuth = errors.As(err, &ae)
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
			}
			c.Sync.DictionaryAt = time.Now().UnixMilli()
		})
		st.Merged = true
		return
	}
	var newest *DeviceDoc
	for i := range others {
		if newest == nil || others[i].DictionaryAt > newest.DictionaryAt {
			newest = &others[i]
		}
	}
	if newest != nil && newest.DictionaryAt > e.config().Sync.DictionaryAt {
		_ = e.update(func(c *config.Config) {
			c.Dictionary = newest.Dictionary
			c.Sync.DictionaryAt = newest.DictionaryAt
		})
	}
}

// ownDoc builds this computer's device file and its months of history.
func (e *Engine) ownDoc(c config.Sync) (DeviceDoc, map[string][]history.Entry, error) {
	cfg := e.config()
	doc := DeviceDoc{Device: c.DeviceID, Name: c.DeviceName, Updated: time.Now().UnixMilli(),
		Dictionary: cfg.Dictionary, DictionaryAt: cfg.Sync.DictionaryAt, Months: map[string]string{}}
	var err error
	if doc.Days, err = e.hist.OwnDaySums(); err != nil {
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
