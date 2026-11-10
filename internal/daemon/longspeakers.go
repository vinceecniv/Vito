package daemon

import (
	"context"
	"errors"
	"time"

	"vito/internal/longrec"
	"vito/internal/stt"
)

// Speaker recognition for a finished long recording (docs/long-recordings.md,
// phase 2): the whole audio once more to Soniox or AssemblyAI with diarization,
// in the background; the turns then replace the blocks in the document and
// are cleaned in chunks.

// SpeakerProvider is the service that would recognise speakers now; "" when
// no Soniox or AssemblyAI key is set.
func (d *Daemon) SpeakerProvider() string { return stt.SpeakerProvider(d.Config().STT) }

// RecognizeSpeakers starts speaker recognition for a finished session.
func (d *Daemon) RecognizeSpeakers(id string) (longrec.Meta, error) {
	l := d.long
	if l == nil {
		return longrec.Meta{}, ErrNoLong
	}
	m, err := l.store.Meta(id)
	if err != nil {
		return m, err
	}
	provider := d.SpeakerProvider()
	switch {
	case provider == "":
		return m, errors.New("speaker recognition needs a Soniox or AssemblyAI key (Settings → Speech recognition)")
	case m.State != longrec.StateDone:
		return m, errors.New("the recording is not finished yet")
	case l.store.AudioBytes(id) == 0:
		return m, errors.New("the audio of this recording was removed")
	case l.busy(id):
		return m, errors.New("the recording is busy; try again in a moment")
	}
	m, err = l.store.UpdateMeta(id, func(m *longrec.Meta) { m.Speakers, m.SpeakersError = longrec.SpeakersRunning, "" })
	if err != nil {
		return m, err
	}
	d.runSpeakers(id)
	return m, nil
}

func (d *Daemon) runSpeakers(id string) {
	l := d.long
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	l.mu.Lock()
	if l.speakers == nil {
		l.speakers = map[string]context.CancelFunc{}
	}
	if l.speakers[id] != nil {
		l.mu.Unlock()
		cancel()
		return
	}
	l.speakers[id] = cancel
	l.mu.Unlock()
	d.emit(Event{Type: "long_doc", Session: id})
	go func() {
		defer cancel()
		cfg := d.Config()
		provider := stt.SpeakerProvider(cfg.STT)
		m, _ := l.store.Meta(id)
		d.log.Info("long recording: recognising speakers", "session", id, "provider", provider, "seconds", m.Seconds)
		turns, err := stt.TranscribeSpeakers(ctx, cfg.STT, provider, l.store.AudioPath(id))
		if err == nil && len(turns) == 0 {
			err = errors.New("no speech was recognised")
		}
		if err == nil {
			err = l.store.SaveSpeakers(id, provider, m.Seconds, turns)
		}
		l.mu.Lock()
		delete(l.speakers, id)
		l.mu.Unlock()
		if _, merr := l.store.Meta(id); merr != nil {
			return // deleted meanwhile
		}
		if err != nil {
			if ctx.Err() == context.Canceled {
				return
			}
			d.log.Warn("long recording: speakers not recognised", "session", id, "err", err)
			l.store.UpdateMeta(id, func(m *longrec.Meta) { m.Speakers, m.SpeakersError = longrec.SpeakersFailed, err.Error() })
			d.emit(Event{Type: "long_doc", Session: id})
			return
		}
		d.log.Info("long recording: speakers recognised", "session", id, "turns", len(turns))
		words := l.store.Words(id)
		l.store.UpdateMeta(id, func(m *longrec.Meta) {
			m.Speakers, m.SpeakersError = longrec.SpeakersDone, ""
			m.Words = words
		})
		// Speaker recognition is billed like a file upload: count it in the
		// session's cost.
		l.store.UpdateMeta(id, func(m *longrec.Meta) { m.SpeakerSeconds += m.Seconds; m.SpeakerRate = stt.SpeakerRateUSD(provider) })
		l.cleaner.EnqueueMissing(id)
		d.emit(Event{Type: "long_doc", Session: id})
	}()
}

// cancelSpeakers stops a running recognition (the session is deleted).
func (d *Daemon) cancelSpeakers(id string) {
	l := d.long
	l.mu.Lock()
	cancel := l.speakers[id]
	l.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// RemoveSpeakers goes back to the text without speakers.
func (d *Daemon) RemoveSpeakers(id string) error {
	l := d.long
	if l == nil {
		return ErrNoLong
	}
	d.cancelSpeakers(id)
	if err := l.store.RemoveSpeakers(id); err != nil {
		return err
	}
	l.cleaner.EnqueueMissing(id) // blocks never cleaned while turns were read
	words := l.store.Words(id)
	l.store.UpdateMeta(id, func(m *longrec.Meta) { m.Words = words })
	d.emit(Event{Type: "long_doc", Session: id})
	return nil
}

// NameSpeaker names a speaker everywhere.
func (d *Daemon) NameSpeaker(id, speaker, name string) error {
	l := d.long
	if l == nil {
		return ErrNoLong
	}
	if err := l.store.NameSpeaker(id, speaker, name); err != nil {
		return err
	}
	d.emit(Event{Type: "long_doc", Session: id})
	return nil
}

// resumeSpeakers starts recognitions a stopped daemon left running again.
func (d *Daemon) resumeSpeakers() {
	l := d.long
	list, _ := l.store.List()
	for _, m := range list {
		if m.Speakers == longrec.SpeakersRunning {
			id := m.ID
			time.AfterFunc(20*time.Second, func() { d.runSpeakers(id) })
		}
	}
}
