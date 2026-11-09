package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"vito/internal/audio"
	"vito/internal/cleanup"
	"vito/internal/config"
	"vito/internal/longrec"
	"vito/internal/stt"
)

// long is the mode for long recordings (internal/longrec): at most one session
// at a time, beside — never instead of — dictation. It has its own capture,
// so the dictation key keeps working while it runs.
type long struct {
	files   longFiles
	store   *longrec.Store
	cleaner *longrec.Cleaner

	mu        sync.Mutex
	rec       *longrec.Recorder
	capture   *audio.Capture
	chunks    chan []byte
	feeding   sync.WaitGroup
	restoring map[string]bool // sessions RestoreLong is working on
	fresh     bool            // the running session began now (not continued): it may be discarded
}

// ErrNoLong: the sessions folder could not be opened at start-up.
var ErrNoLong = errors.New("long recordings are unavailable (no sessions folder)")

func (d *Daemon) initLong() {
	store, err := longrec.NewStore("")
	if err != nil {
		d.log.Error("long recordings unavailable", "err", err)
		return
	}
	for _, m := range store.Recover() {
		d.log.Warn("long recording interrupted; offered for restoring", "session", m.ID, "seconds", m.Seconds)
	}
	go store.Recount()
	l := &long{store: store}
	l.cleaner = longrec.NewCleaner(store, d.longCleanSettings, d.log, func(id string) {
		d.emit(Event{Type: "long_doc", Session: id})
	})
	d.long = l
	// Finished sessions still holding a WAV (from before, or a crash in the
	// middle of compressing) become MP3s.
	// Then every recording's own folder is brought up to date.
	go func() {
		store.CompressAll(l.busy)
		d.writeAllLongFiles()
	}()
}

// busy says a session's audio is in use: being recorded or restored.
func (l *long) busy(id string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.restoring[id] || (l.rec != nil && l.rec.ID() == id)
}

// compressLong turns a finished session's WAV into its MP3, unless it is in
// use again by then.
func (d *Daemon) compressLong(id string) {
	l := d.long
	if l == nil || l.busy(id) {
		return
	}
	if err := l.store.Compress(id); err != nil {
		d.log.Warn("long recording: audio not compressed; the WAV stays", "session", id, "err", err)
		return
	}
	d.log.Info("long recording: audio compressed", "session", id, "bytes", l.store.AudioBytes(id))
	d.emit(Event{Type: "long_doc", Session: id})
}

func (d *Daemon) longCleanSettings() longrec.CleanSettings {
	cfg := d.Config()
	return longrec.CleanSettings{
		Cleanup:     cfg.Cleanup,
		Language:    cfg.STT.Language,
		Corrections: cfg.Dictionary.Corrections,
		On:          cfg.Long.CleanEnabled() && cfg.Cleanup.Enabled && cleanup.Configured(cfg.Cleanup),
	}
}

// LongStore exposes the sessions to the HTTP layer.
func (d *Daemon) LongStore() (*longrec.Store, error) {
	if d.long == nil {
		return nil, ErrNoLong
	}
	return d.long.store, nil
}

// fileTranscriber transcribes a WAV with the configured provider's file API,
// whichever provider that is.
type fileTranscriber struct{ cfg config.STT }

func (f fileTranscriber) TranscribeFile(ctx context.Context, path string) (string, error) {
	out, err := stt.TranscribeUpload(ctx, f.cfg, path, nil)
	return out.Text, err
}

// StartLong begins a long recording.
func (d *Daemon) StartLong(title string) (longrec.Status, error) {
	return d.startLong(func(o longrec.Options) (*longrec.Recorder, error) { return longrec.Start(o, title) }, true, func() bool { return false })
}

// ContinueLong records on in an existing session.
func (d *Daemon) ContinueLong(id string) (longrec.Status, error) {
	return d.startLong(func(o longrec.Options) (*longrec.Recorder, error) {
		// Recording on appends to the WAV: a compressed session gets it back.
		if err := o.Store.EnsureWAV(id); err != nil {
			return nil, err
		}
		return longrec.Continue(o, id)
	}, false, func() bool { return d.long.restoring[id] })
}

// startLong opens the microphone for a session that open starts or continues.
// fresh: the session is new, so a microphone that will not open deletes it.
func (d *Daemon) startLong(open func(longrec.Options) (*longrec.Recorder, error), fresh bool, busy func() bool) (longrec.Status, error) {
	l := d.long
	if l == nil {
		return longrec.Status{}, ErrNoLong
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.rec != nil {
		return l.rec.Status(), fmt.Errorf("a long recording is already running")
	}
	if busy() {
		return longrec.Status{}, fmt.Errorf("this recording is being restored")
	}
	cfg := d.Config()
	if err := uploadReady(cfg.STT); err != nil {
		return longrec.Status{}, err
	}
	sttCfg, err := d.resolveSTT(cfg.STT)
	if err != nil {
		return longrec.Status{}, err
	}
	d.stopPlayback()
	rec, err := open(longrec.Options{
		Store: l.store,
		NewStream: func(onPartial func(string)) stt.Streamer {
			return stt.NewStream(sttCfg, d.log, onPartial)
		},
		File:          fileTranscriber{sttCfg},
		FinishTimeout: max(stt.FinishTimeout(sttCfg), 30*time.Second),
		Provider:      sttProviderName(sttCfg),
		Language:      cfg.STT.Language,
		Log:           d.log,
		OnUpdate: func(st longrec.Status) {
			d.emit(Event{Type: "long", Long: &st})
		},
		OnBlock: func(id string, b longrec.Block) {
			d.emit(Event{Type: "long_doc", Session: id})
			if strings.TrimSpace(b.Text) != "" {
				l.cleaner.Enqueue(id, b.Index)
			}
		},
	})
	if err != nil {
		return longrec.Status{}, err
	}
	l.chunks = make(chan []byte, 512)
	chunks := l.chunks
	l.feeding.Add(1)
	go func() {
		defer l.feeding.Done()
		for c := range chunks {
			rec.Write(c)
		}
	}()
	capture, err := audio.StartCapture(d.audioCtx, cfg.Audio.InputDevice, cfg.Audio.InputGain, func(chunk []byte) {
		select {
		case chunks <- chunk:
		default: // the writer stalled; drop rather than block the audio thread
		}
	})
	if err != nil {
		close(chunks)
		l.feeding.Wait()
		rec.Stop()
		if fresh {
			l.store.Delete(rec.ID())
		} else {
			l.cleaner.Finished(rec.ID())
		}
		return longrec.Status{}, fmt.Errorf("open microphone: %w", err)
	}
	l.rec, l.capture, l.fresh = rec, capture, fresh
	d.log.Info("long recording started", "session", rec.ID(), "provider", sttProviderName(sttCfg))
	return rec.Status(), nil
}

// running returns the running recorder, or an error.
func (d *Daemon) running() (*longrec.Recorder, error) {
	if d.long == nil {
		return nil, ErrNoLong
	}
	d.long.mu.Lock()
	defer d.long.mu.Unlock()
	if d.long.rec == nil {
		return nil, fmt.Errorf("no long recording is running")
	}
	return d.long.rec, nil
}

// LongStatus reports the running session, if any.
func (d *Daemon) LongStatus() (longrec.Status, bool) {
	r, err := d.running()
	if err != nil {
		return longrec.Status{}, false
	}
	return r.Status(), true
}

// PauseLong, ResumeLong, MarkLong and HeaderLong act on the running session.
func (d *Daemon) PauseLong() error {
	r, err := d.running()
	if err == nil {
		r.Pause()
	}
	return err
}

func (d *Daemon) ResumeLong() error {
	r, err := d.running()
	if err == nil {
		d.stopPlayback()
		r.Resume()
	}
	return err
}

func (d *Daemon) MarkLong() (longrec.Event, error) {
	r, err := d.running()
	if err != nil {
		return longrec.Event{}, err
	}
	e, err := r.Mark()
	if err == nil {
		d.emit(Event{Type: "long_doc", Session: r.ID()})
	}
	return e, err
}

func (d *Daemon) HeaderLong(title string) (longrec.Event, error) {
	r, err := d.running()
	if err != nil {
		return longrec.Event{}, err
	}
	e, err := r.Header(title)
	if err == nil {
		d.emit(Event{Type: "long_doc", Session: r.ID()})
	}
	return e, err
}

// TitleLong renames a session, running or not.
func (d *Daemon) TitleLong(id, title string) error {
	if d.long == nil {
		return ErrNoLong
	}
	if r, err := d.running(); err == nil && r.ID() == id {
		r.SetTitle(title)
	} else if _, err := d.long.store.UpdateMeta(id, func(m *longrec.Meta) { m.Title = strings.TrimSpace(title) }); err != nil {
		return err
	}
	d.emit(Event{Type: "long_doc", Session: id})
	return nil
}

// StopLong ends the running session. The microphone closes at once; the last
// block's transcript and the cleanup follow in the background.
func (d *Daemon) StopLong() (string, error) {
	l := d.long
	if l == nil {
		return "", ErrNoLong
	}
	l.mu.Lock()
	rec, capture, chunks := l.rec, l.capture, l.chunks
	l.rec, l.capture, l.chunks = nil, nil, nil
	l.mu.Unlock()
	if rec == nil {
		return "", fmt.Errorf("no long recording is running")
	}
	capture.Stop()
	close(chunks)
	l.feeding.Wait()
	id := rec.ID()
	go func() {
		m, err := rec.Stop()
		if err != nil {
			d.log.Error("long recording: stop", "session", id, "err", err)
		}
		d.log.Info("long recording stopped", "session", id, "seconds", m.Seconds, "words", m.Words)
		l.cleaner.Finished(id)
		d.emit(Event{Type: "long_doc", Session: id})
		d.compressLong(id)
	}()
	return id, nil
}

// DiscardLong throws a just-started session away — audio, text and all —
// without transcribing it. A continued session cannot be: its earlier part
// would go with it.
func (d *Daemon) DiscardLong() error {
	l := d.long
	if l == nil {
		return ErrNoLong
	}
	l.mu.Lock()
	rec, capture, chunks := l.rec, l.capture, l.chunks
	if rec == nil {
		l.mu.Unlock()
		return fmt.Errorf("no long recording is running")
	}
	if !l.fresh {
		l.mu.Unlock()
		return fmt.Errorf("a continued recording cannot be discarded; stop it instead")
	}
	l.rec, l.capture, l.chunks = nil, nil, nil
	l.mu.Unlock()
	capture.Stop()
	close(chunks)
	l.feeding.Wait()
	rec.Abort()
	id := rec.ID()
	d.trashLongFiles(id)
	if err := l.store.Delete(id); err != nil {
		return err
	}
	d.log.Info("long recording discarded", "session", id)
	d.emit(Event{Type: "long", Long: &longrec.Status{ID: id, State: longrec.StateDone}})
	return nil
}

// RestoreLong transcribes what an interrupted session had not yet, then
// cleans it. Runs in the background.
func (d *Daemon) RestoreLong(id string) error {
	if d.long == nil {
		return ErrNoLong
	}
	cfg := d.Config()
	if err := uploadReady(cfg.STT); err != nil {
		return err
	}
	sttCfg, err := d.resolveSTT(cfg.STT)
	if err != nil {
		return err
	}
	l := d.long
	l.mu.Lock()
	if l.restoring == nil {
		l.restoring = map[string]bool{}
	}
	if l.restoring[id] || (l.rec != nil && l.rec.ID() == id) {
		l.mu.Unlock()
		return fmt.Errorf("this recording is busy")
	}
	l.restoring[id] = true
	l.mu.Unlock()
	go func() {
		defer func() { l.mu.Lock(); delete(l.restoring, id); l.mu.Unlock() }()
		_, err := longrec.Restore(d.long.store, id, fileTranscriber{sttCfg}, func(longrec.Block) {
			d.emit(Event{Type: "long_doc", Session: id})
		})
		if err != nil {
			d.log.Warn("long recording: restore incomplete", "session", id, "err", err)
		}
		d.long.cleaner.EnqueueMissing(id)
		d.emit(Event{Type: "long_doc", Session: id})
		l.mu.Lock()
		delete(l.restoring, id)
		l.mu.Unlock()
		d.compressLong(id)
	}()
	return nil
}

// ProposeLongTitles has the cleanup model propose titles for headers that
// have none — one placed in a finished recording, say.
func (d *Daemon) ProposeLongTitles(id string) {
	if d.long != nil && d.longCleanSettings().On {
		d.long.cleaner.Finished(id)
	}
}

// SetLongProfile picks the cleanup rule set for a session's blocks from now
// on ("" is Vito's own rules for recordings).
func (d *Daemon) SetLongProfile(id, profile string) error {
	if d.long == nil {
		return ErrNoLong
	}
	if profile != "" {
		found := false
		for _, p := range d.Config().Cleanup.Prompts {
			found = found || p.ID == profile
		}
		for _, b := range cleanup.Builtins() {
			found = found || b.ID == profile
		}
		if !found {
			return fmt.Errorf("no such cleanup profile")
		}
	}
	_, err := d.long.store.UpdateMeta(id, func(m *longrec.Meta) { m.Profile = profile })
	if err == nil {
		d.emit(Event{Type: "long_doc", Session: id})
	}
	return err
}

// RecleanLong cleans a whole session again.
func (d *Daemon) RecleanLong(id string) error {
	if d.long == nil {
		return ErrNoLong
	}
	if !d.longCleanSettings().On {
		return fmt.Errorf("AI cleanup is off")
	}
	if err := d.long.cleaner.Reclean(id); err != nil {
		return err
	}
	d.emit(Event{Type: "long_doc", Session: id})
	return nil
}

// DeleteLong removes a session; never the running one.
func (d *Daemon) DeleteLong(id string) error {
	if d.long == nil {
		return ErrNoLong
	}
	if r, err := d.running(); err == nil && r.ID() == id {
		return fmt.Errorf("stop the recording first")
	}
	d.trashLongFiles(id)
	return d.long.store.Delete(id)
}

// shutdownLong keeps a running session restorable when Vito quits: the audio
// and blocks so far stay, the next start offers the rest.
func (d *Daemon) shutdownLong() {
	l := d.long
	if l == nil {
		return
	}
	l.mu.Lock()
	rec, capture, chunks := l.rec, l.capture, l.chunks
	l.rec = nil
	l.mu.Unlock()
	if rec == nil {
		return
	}
	capture.Stop()
	close(chunks)
	l.feeding.Wait()
	rec.Abort()
}
