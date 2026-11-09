package daemon

import (
	"context"
	"io"
	"time"

	"vito/internal/longrec"
)

// An audio file as a long recording (longrec.Import): the page hands over
// the audio as Vito's WAV, the blocks are transcribed one by one with the
// provider's file API and cleaned as they come, as during a recording.

// ImportLong makes a session of an audio file and starts transcribing it.
func (d *Daemon) ImportLong(title string, wav io.Reader) (longrec.Meta, error) {
	l := d.long
	if l == nil {
		return longrec.Meta{}, ErrNoLong
	}
	cfg := d.Config()
	if err := uploadReady(cfg.STT); err != nil {
		return longrec.Meta{}, err
	}
	sttCfg, err := d.resolveSTT(cfg.STT)
	if err != nil {
		return longrec.Meta{}, err
	}
	m, err := l.store.NewImport(title, wav)
	if err != nil {
		return m, err
	}
	m, _ = l.store.UpdateMeta(m.ID, func(m *longrec.Meta) {
		m.Provider, m.Language = sttProviderName(sttCfg), cfg.STT.Language
	})
	d.log.Info("long recording: audio file imported", "session", m.ID, "seconds", m.Seconds)
	d.runImport(m.ID)
	return m, nil
}

// runImport transcribes a session's missing blocks in the background.
func (d *Daemon) runImport(id string) {
	l := d.long
	ctx, cancel := context.WithCancel(context.Background())
	l.mu.Lock()
	if l.importing == nil {
		l.importing = map[string]context.CancelFunc{}
	}
	if l.importing[id] != nil {
		l.mu.Unlock()
		cancel()
		return
	}
	l.importing[id] = cancel
	l.mu.Unlock()
	d.emit(Event{Type: "long_doc", Session: id})
	go func() {
		defer cancel()
		done := func() {
			l.mu.Lock()
			delete(l.importing, id)
			l.mu.Unlock()
		}
		cfg := d.Config()
		sttCfg, err := d.resolveSTT(cfg.STT)
		if err != nil {
			done()
			d.log.Warn("long recording: audio file not transcribed", "session", id, "err", err)
			return
		}
		err = longrec.Import(ctx, l.store, id, fileTranscriber{sttCfg}, func(b longrec.Block, p longrec.ImportProgress) {
			if b.Text != "" {
				l.cleaner.Enqueue(id, b.Index)
			}
			d.emit(Event{Type: "long_doc", Session: id})
		})
		done()
		if err != nil {
			if ctx.Err() == nil {
				d.log.Warn("long recording: audio file not transcribed", "session", id, "err", err)
			}
			return
		}
		d.log.Info("long recording: audio file transcribed", "session", id)
		l.cleaner.Finished(id)
		d.emit(Event{Type: "long_doc", Session: id})
		d.emit(Event{Type: "long_imported", Session: id})
		d.compressLong(id)
	}()
}

// cancelImport stops an import (the session is being deleted).
func (d *Daemon) cancelImport(id string) {
	l := d.long
	l.mu.Lock()
	cancel := l.importing[id]
	l.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// resumeImports carries on with imports a stopped daemon left unfinished,
// once the speech service has had time to start (a local engine).
func (d *Daemon) resumeImports() {
	l := d.long
	list, _ := l.store.List()
	var ids []string
	for _, m := range list {
		if m.State == longrec.StateImporting {
			ids = append(ids, m.ID)
		}
	}
	if len(ids) == 0 {
		return
	}
	time.AfterFunc(20*time.Second, func() {
		for _, id := range ids {
			d.runImport(id)
		}
	})
}
