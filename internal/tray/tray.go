// Package tray shows a system-tray icon that gives quick access to Vito
// without a GUI toolkit: open the web settings page, start/stop or cancel a
// dictation, switch a few common settings, and quit. The real UI stays in the
// browser — the tray is a launcher, a live status dot, and quick toggles.
//
// It is best-effort: where no tray host is present (some Wayland setups,
// headless services) the icon simply never appears and the daemon keeps
// running. systray.Run owns the calling goroutine and blocks until Quit.
package tray

import (
	"log/slog"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"fyne.io/systray"

	"vito/internal/autostart"
	"vito/internal/config"
	"vito/internal/daemon"
)

// animator holds one static tray icon per state and swaps it on state change —
// nothing animates. It also rebuilds the set when the OS taskbar theme flips.
type animator struct {
	icons map[daemon.State][]byte
	mu    sync.Mutex
	state daemon.State
	dark  bool
}

func newAnimator(dark bool) *animator {
	return &animator{icons: buildIcons(dark), state: daemon.StateIdle, dark: dark}
}

// setDark rebuilds the icon set when the OS taskbar theme changes.
func (a *animator) setDark(dark bool) {
	a.mu.Lock()
	if dark == a.dark {
		a.mu.Unlock()
		return
	}
	a.dark = dark
	a.icons = buildIcons(dark)
	ic := a.icons[a.state]
	a.mu.Unlock()
	if len(ic) > 0 {
		systray.SetIcon(ic)
	}
}

// setState switches to s and shows its icon immediately.
func (a *animator) setState(s daemon.State) {
	a.mu.Lock()
	a.state = s
	ic := a.icons[s]
	a.mu.Unlock()
	if len(ic) > 0 {
		systray.SetIcon(ic)
	}
}

// quitChosen records that the user picked Quit from the menu. There is only
// ever one tray, so one package-level flag covers it.
var quitChosen atomic.Bool

// Run displays the tray icon and blocks until the user selects Quit or the tray
// host goes away, and reports which of the two happened. The caller has to tell
// them apart — "the user wants out" and "there is no tray on this system" both
// end up here, and only the first should stop the daemon.
//
// The answer deliberately comes from our own flag rather than systray's onExit
// callback: on macOS, Quit stops the app event loop with [NSApp stop:], which
// never fires applicationWillTerminate:, so onExit is not called at all there.
// Relying on it left the process alive with a dead Cocoa run loop — a menu bar
// icon that had vanished and an app macOS reported as "not responding".
func Run(d *daemon.Daemon, url, version string, log *slog.Logger) (userQuit bool) {
	quitChosen.Store(false)
	systray.Run(func() { onReady(d, url, version, log) }, func() {})
	return quitChosen.Load()
}

func onReady(d *daemon.Daemon, url, version string, log *slog.Logger) {
	anim := newAnimator(wantDark(d))
	anim.setState(daemon.StateIdle)
	go func() { // follow OS taskbar theme changes when Vito's theme is "system"
		for {
			time.Sleep(3 * time.Second)
			anim.setDark(wantDark(d))
		}
	}()
	// macOS renders the title as text beside the menu-bar icon, which doubles
	// the width of the status item to repeat what the icon already says. Other
	// hosts use it as a label where the icon may not be shown at all, so it is
	// only dropped here.
	if runtime.GOOS != "darwin" {
		systray.SetTitle("Vito Tray")
	}
	systray.SetTooltip("Vito Tray — idle")

	mVersion := systray.AddMenuItem("Vito "+version, "Versie")
	mVersion.Disable()
	systray.AddSeparator()
	mStatus := systray.AddMenuItem("● Idle", "Huidige status")
	mStatus.Disable()
	systray.AddSeparator()
	mSettings := systray.AddMenuItem("Instellingen openen…", "Open de web-UI in de browser")
	systray.AddSeparator()
	mToggle := systray.AddMenuItem("Start / stop dictaat", "Start of stop een opname")
	mCancel := systray.AddMenuItem("Annuleren", "Annuleer de huidige opname")
	systray.AddSeparator()

	// Media action submenu (radio-style checkboxes).
	mMedia := systray.AddMenuItem("Media tijdens inspreken", "Wat te doen met afspelende media")
	mMediaDuck := mMedia.AddSubMenuItemCheckbox("Volume dempen (duck)", "", false)
	mMediaPause := mMedia.AddSubMenuItemCheckbox("Pauzeren", "", false)
	mMediaOff := mMedia.AddSubMenuItemCheckbox("Uit", "", false)

	mCleanupDefault := systray.AddMenuItemCheckbox("Cleanup standaard aan", "Elke dictatie door Claude-cleanup", false)
	mAppendEnter := systray.AddMenuItemCheckbox("Enter na tekst", "Voegt automatisch een Enter toe (verzendt bv. chat/terminal-invoer)", false)
	mAutostart := systray.AddMenuItemCheckbox("Meestarten met OS", "Vito starten bij inloggen", false)
	if !autostart.Supported() {
		mAutostart.Disable()
	}
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Vito afsluiten", "Stop de daemon")

	setCheck := func(it *systray.MenuItem, on bool) {
		if on {
			it.Check()
		} else {
			it.Uncheck()
		}
	}
	// refresh reflects current config + OS autostart state onto the checkboxes.
	refresh := func() {
		cfg := d.Config()
		setCheck(mMediaDuck, cfg.Audio.MediaAction == "duck")
		setCheck(mMediaPause, cfg.Audio.MediaAction == "pause")
		setCheck(mMediaOff, cfg.Audio.MediaAction == "off")
		setCheck(mCleanupDefault, cfg.Cleanup.Enabled)
		setCheck(mAppendEnter, cfg.Injection.AppendEnter)
		if autostart.Supported() {
			if en, err := autostart.Enabled(); err == nil {
				setCheck(mAutostart, en)
			}
		}
	}
	refresh()

	// change applies a config mutation, persisting and re-reflecting it.
	change := func(mut func(*config.Config)) {
		cfg := d.Config()
		mut(&cfg)
		if err := d.SetConfig(cfg); err != nil {
			log.Warn("tray: apply setting failed", "err", err)
		}
		refresh()
	}

	// Live state on the status line; config changes (from web UI or tray) keep
	// the checkboxes in sync.
	d.AddEventListener(func(e daemon.Event) {
		switch e.Type {
		case "state":
			label, tip := statusText(e.State)
			mStatus.SetTitle(label)
			systray.SetTooltip("Vito Tray — " + tip)
			anim.setState(e.State)
		case "config", "privacy":
			refresh()
			anim.setDark(wantDark(d)) // theme may have changed in the web UI
		}
	})

	go func() {
		for {
			select {
			case <-mSettings.ClickedCh:
				if err := OpenURL(url); err != nil {
					log.Warn("tray: open settings failed", "url", url, "err", err)
				}
			case <-mToggle.ClickedCh:
				if _, err := d.Toggle(); err != nil {
					log.Debug("tray: toggle", "err", err)
				}
			case <-mCancel.ClickedCh:
				if err := d.Cancel(); err != nil {
					log.Debug("tray: cancel", "err", err)
				}
			case <-mMediaDuck.ClickedCh:
				change(func(c *config.Config) { c.Audio.MediaAction = "duck" })
			case <-mMediaPause.ClickedCh:
				change(func(c *config.Config) { c.Audio.MediaAction = "pause" })
			case <-mMediaOff.ClickedCh:
				change(func(c *config.Config) { c.Audio.MediaAction = "off" })
			case <-mCleanupDefault.ClickedCh:
				change(func(c *config.Config) { c.Cleanup.Enabled = !c.Cleanup.Enabled })
			case <-mAppendEnter.ClickedCh:
				change(func(c *config.Config) { c.Injection.AppendEnter = !c.Injection.AppendEnter })
			case <-mAutostart.ClickedCh:
				cur, _ := autostart.Enabled()
				if err := autostart.Set(!cur); err != nil {
					log.Warn("tray: autostart toggle failed", "err", err)
				}
				refresh()
			case <-mQuit.ClickedCh:
				quitChosen.Store(true)
				systray.Quit()
				return
			}
		}
	}()

	log.Info("system tray ready", "settings", url)
}

// wantDark decides the tray-icon variant: Vito's explicit light/dark theme wins;
// "system" (or unset) follows the OS taskbar theme.
func wantDark(d *daemon.Daemon) bool {
	switch d.Config().UI.Theme {
	case "dark":
		return true
	case "light":
		return false
	default:
		return darkTaskbar()
	}
}

func statusText(s daemon.State) (label, tip string) {
	switch s {
	case daemon.StateRecording:
		return "● Opname…", "recording"
	case daemon.StateProcessing:
		return "● Verwerken…", "processing"
	default:
		return "● Idle", "idle"
	}
}
