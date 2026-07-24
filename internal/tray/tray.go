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
	"sync"
	"time"

	"fyne.io/systray"

	"vito/internal/autostart"
	"vito/internal/config"
	"vito/internal/daemon"
)

// animator cycles the tray icon frames for the current state. Idle is a single
// frame (set once); recording and processing loop a pulsing status dot at their
// own cadence until the state changes.
type animator struct {
	anims map[daemon.State]stateAnim
	mu    sync.Mutex
	state daemon.State
	idx   int
	dark  bool
}

func newAnimator(dark bool) *animator {
	return &animator{anims: buildFrames(dark), state: daemon.StateIdle, dark: dark}
}

// setDark rebuilds the icon set when the OS taskbar theme changes.
func (a *animator) setDark(dark bool) {
	a.mu.Lock()
	if dark == a.dark {
		a.mu.Unlock()
		return
	}
	a.dark = dark
	a.anims = buildFrames(dark)
	a.idx = 0
	fr := a.anims[a.state].frames
	a.mu.Unlock()
	if len(fr) > 0 {
		systray.SetIcon(fr[0])
	}
}

// setState switches to s and shows its first frame immediately.
func (a *animator) setState(s daemon.State) {
	a.mu.Lock()
	a.state, a.idx = s, 0
	fr := a.anims[s].frames
	a.mu.Unlock()
	if len(fr) > 0 {
		systray.SetIcon(fr[0])
	}
}

// run advances multi-frame states at their per-state interval; single-frame
// states just idle so a static icon costs nothing.
func (a *animator) run() {
	for {
		a.mu.Lock()
		sa := a.anims[a.state]
		interval := sa.interval
		if interval <= 0 {
			interval = 400 * time.Millisecond
		}
		if len(sa.frames) > 1 {
			a.idx = (a.idx + 1) % len(sa.frames)
			systray.SetIcon(sa.frames[a.idx])
		}
		a.mu.Unlock()
		time.Sleep(interval)
	}
}

// Run displays the tray icon and blocks until the user selects Quit (or the
// tray host goes away). onQuit runs after systray tears down; the caller should
// exit the process there.
func Run(d *daemon.Daemon, url string, log *slog.Logger, onQuit func()) {
	systray.Run(func() { onReady(d, url, log) }, onQuit)
}

func onReady(d *daemon.Daemon, url string, log *slog.Logger) {
	anim := newAnimator(wantDark(d))
	anim.setState(daemon.StateIdle)
	go anim.run()
	go func() { // follow OS taskbar theme changes when Vito's theme is "system"
		for {
			time.Sleep(3 * time.Second)
			anim.setDark(wantDark(d))
		}
	}()
	systray.SetTitle("Vito Tray")
	systray.SetTooltip("Vito Tray — idle")

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
