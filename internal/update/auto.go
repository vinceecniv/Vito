package update

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Auto keeps a release of Vito up to date without asking: once the check finds
// a newer version it downloads it in the background, waits until nobody is
// dictating and nothing has happened for a while, and then puts it in place
// and restarts into it — the way a browser updates itself.
//
// Where Vito did not install itself it leaves the job alone: a Flatpak, a
// distribution package or a binary in a read-only place is updated by whatever
// put it there.
type Auto struct {
	C       *Checker
	Log     *slog.Logger
	Enabled func() bool // the setting, and the update check under it
	Idle    func() bool // true while nothing is being recorded or processed
	// Prepare runs right before the new version is put in place, to leave the
	// machine as it was (restore ducked media, stop the local engine).
	Prepare func()

	// QuietFor is how long the daemon must have been idle before installing.
	QuietFor time.Duration
	// Poll is how often Run looks.
	Poll time.Duration

	mu        sync.Mutex
	state     AutoState
	version   string
	err       string
	staged    string // the verified download, waiting for a quiet moment
	stagedFor string // the version it holds
	idleSince time.Time
	retryAt   time.Time // after a failure, nothing is tried before this
	inst      installer
	reason    string // why there is no installer here
	now       func() time.Time
}

// AutoState is where the automatic update stands.
type AutoState string

const (
	AutoOff         AutoState = "off"         // the setting is off, or a hand build
	AutoUnmanaged   AutoState = "unmanaged"   // updated by something else (Flatpak, package manager)
	AutoIdle        AutoState = "idle"        // up to date, or waiting for the next check
	AutoDownloading AutoState = "downloading" //
	AutoReady       AutoState = "ready"       // downloaded and verified; installs when Vito is quiet
	AutoInstalling  AutoState = "installing"  //
	AutoError       AutoState = "error"       //
)

// AutoStatus is what /api/update reports.
type AutoStatus struct {
	State   AutoState `json:"state"`
	Version string    `json:"version,omitempty"`
	Error   string    `json:"error,omitempty"`
	Reason  string    `json:"reason,omitempty"` // for "unmanaged"
}

// installer puts a downloaded release in place on one platform.
type installer interface {
	// asset is the release file this platform installs from, "" when the
	// release has none for it.
	asset(rel *Release) string
	// install replaces the running version with the one at path and starts
	// it. On success it does not return: the process exits or is replaced.
	install(path string) error
}

// NewAuto wires an automatic updater to a checker.
func NewAuto(c *Checker, log *slog.Logger, enabled, idle func() bool, prepare func()) *Auto {
	a := &Auto{C: c, Log: log, Enabled: enabled, Idle: idle, Prepare: prepare,
		QuietFor: 3 * time.Minute, Poll: 20 * time.Second, now: time.Now}
	a.inst, a.reason = platformInstaller()
	a.state = AutoIdle
	return a
}

// Status reports the automatic update's state.
func (a *Auto) Status() AutoStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case !isCalendar(a.C.Current):
		return AutoStatus{State: AutoOff, Reason: "development build"}
	case a.inst == nil:
		return AutoStatus{State: AutoUnmanaged, Reason: a.reason}
	case !a.Enabled():
		return AutoStatus{State: AutoOff}
	}
	return AutoStatus{State: a.state, Version: a.version, Error: a.err}
}

func isCalendar(v string) bool { _, ok := parse(v); return ok }

// Run looks every Poll until ctx ends.
func (a *Auto) Run(ctx context.Context) {
	if !isCalendar(a.C.Current) || a.inst == nil {
		return // a hand build, or not ours to update
	}
	t := time.NewTicker(a.Poll)
	defer t.Stop()
	for {
		a.step(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// step does one round: check (the checker caches for a day), download when
// there is something new, install when it is quiet.
func (a *Auto) step(ctx context.Context) {
	now := a.now()
	idle := a.Idle()
	a.mu.Lock()
	if !idle {
		a.idleSince = time.Time{}
	} else if a.idleSince.IsZero() {
		a.idleSince = now
	}
	quiet := idle && now.Sub(a.idleSince) >= a.QuietFor
	a.mu.Unlock()

	if !a.Enabled() || now.Before(a.retryAt) {
		return
	}
	rel, err := a.C.Check(ctx, false)
	if err != nil {
		a.backoff()
		return
	}
	if rel == nil || !rel.Available {
		return
	}

	a.mu.Lock()
	staged := a.stagedFor == rel.Version && a.staged != ""
	a.mu.Unlock()
	if !staged {
		a.download(ctx, rel)
		return // install on a later round, after a fresh quiet check
	}
	if quiet {
		a.installStaged()
	}
}

func (a *Auto) download(ctx context.Context, rel *Release) {
	name := a.inst.asset(rel)
	if name == "" {
		a.set(AutoError, rel.Version, "this release has no download for this system")
		return
	}
	a.set(AutoDownloading, rel.Version, "")
	path, err := a.C.DownloadAsset(ctx, rel, name, nil)
	if err != nil {
		a.Log.Warn("automatic update: download failed", "version", rel.Version, "err", err)
		a.set(AutoError, rel.Version, err.Error())
		a.backoff()
		return
	}
	a.mu.Lock()
	if a.staged != "" {
		_ = os.RemoveAll(filepath.Dir(a.staged))
	}
	a.staged, a.stagedFor = path, rel.Version
	a.state, a.err = AutoReady, ""
	a.mu.Unlock()
	a.Log.Info("automatic update: downloaded, installs when Vito is idle", "version", rel.Version)
}

func (a *Auto) installStaged() {
	a.mu.Lock()
	path, ver := a.staged, a.stagedFor
	a.state = AutoInstalling
	a.mu.Unlock()
	a.Log.Info("automatic update: installing", "version", ver)
	MarkUpdating(a.C.Current)
	if a.Prepare != nil {
		a.Prepare()
	}
	if err := a.inst.install(path); err != nil {
		ClearMark()
		a.Log.Warn("automatic update: install failed", "version", ver, "err", err)
		a.mu.Lock()
		// Start over from the download next time; the file may be the problem.
		_ = os.RemoveAll(filepath.Dir(a.staged))
		a.staged, a.stagedFor = "", ""
		a.state, a.err = AutoError, err.Error()
		a.mu.Unlock()
		a.backoff()
	}
}

// retryAfter is the pause after a failed check, download or install: a
// network that is down or a broken release is not hammered every Poll.
const retryAfter = 30 * time.Minute

func (a *Auto) backoff() {
	a.mu.Lock()
	a.retryAt = a.now().Add(retryAfter)
	a.mu.Unlock()
}

func (a *Auto) set(st AutoState, ver, errText string) {
	a.mu.Lock()
	a.state, a.version, a.err = st, ver, errText
	a.mu.Unlock()
}

// --- "updated from" marker ---

// markPath is where the version that was replaced is noted, so the next start
// can tell the user what happened.
func markPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "vito", "updated-from")
}

// MarkUpdating notes the running version just before it is replaced.
func MarkUpdating(current string) {
	if p := markPath(); p != "" {
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte(current), 0o644)
	}
}

// ClearMark removes the note again, when the install did not happen.
func ClearMark() {
	if p := markPath(); p != "" {
		_ = os.Remove(p)
	}
}

// TakeUpdatedFrom returns the version this one replaced, once: the note is
// removed. "" when this start was not an update.
func TakeUpdatedFrom(current string) string {
	p := markPath()
	if p == "" {
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	_ = os.Remove(p)
	prev := strings.TrimSpace(string(b))
	if !Newer(current, prev) {
		return "" // the install did not take, or this is an older build
	}
	return prev
}
