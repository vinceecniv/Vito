package daemon

import (
	"fmt"
	"time"
)

// A cancelled dictation is not gone at once: its recording stays on this
// machine for keepCancelled, so a cancel by mistake can be undone (the pill
// offers it, and the island). Nothing is transcribed or written to the
// history unless it is restored; then it runs as any dictation does, pasted
// where the keyboard is. A new dictation, the time running out or Vito
// quitting deletes it, so a cancel meant as one still ends with nothing kept.
// Silence and a mistaken tap of the key are not kept: nothing was said.

const (
	keepCancelled = 5 * time.Minute
	// minKept: a shorter recording holds nothing worth restoring.
	minKept = 700 * time.Millisecond
)

// keep holds on to s, closed and torn down, in place of an earlier one.
func (d *Daemon) keep(s *session) bool {
	if s.spool.Duration() < minKept {
		return false
	}
	d.keptMu.Lock()
	old := d.takeKeptLocked()
	d.kept = s
	d.keptTimer = time.AfterFunc(keepCancelled, func() {
		d.keptMu.Lock()
		gone := d.kept == s
		if gone {
			d.takeKeptLocked()
		}
		d.keptMu.Unlock()
		if gone {
			s.spool.Remove()
			d.emit(Event{Type: "kept_gone"})
		}
	})
	d.keptMu.Unlock()
	if old != nil {
		old.spool.Remove()
	}
	return true
}

func (d *Daemon) takeKeptLocked() *session {
	s := d.kept
	d.kept = nil
	if d.keptTimer != nil {
		d.keptTimer.Stop()
		d.keptTimer = nil
	}
	return s
}

// dropKept deletes the kept dictation, if there is one.
func (d *Daemon) dropKept() {
	d.keptMu.Lock()
	s := d.takeKeptLocked()
	d.keptMu.Unlock()
	if s != nil {
		s.spool.Remove()
		d.emit(Event{Type: "kept_gone"})
	}
}

// Cancelled reports the cancelled dictation that can still be restored, with
// the words heard of it so far ("" when the service sends none while you speak).
func (d *Daemon) Cancelled() (heard string, ok bool) {
	d.keptMu.Lock()
	defer d.keptMu.Unlock()
	if d.kept == nil {
		return "", false
	}
	heard, _ = d.kept.heard.Load().(string)
	return heard, true
}

// RestoreCancelled transcribes the kept dictation and pastes it like any other.
func (d *Daemon) RestoreCancelled() error {
	d.mu.Lock()
	if d.state != StateIdle {
		d.mu.Unlock()
		return fmt.Errorf("cannot restore while %s", d.state)
	}
	d.keptMu.Lock()
	s := d.takeKeptLocked()
	d.keptMu.Unlock()
	if s == nil {
		d.mu.Unlock()
		return fmt.Errorf("no cancelled dictation to restore")
	}
	d.state = StateProcessing
	d.status.LastError = ""
	d.mu.Unlock()
	d.emit(Event{Type: "kept_gone"})
	d.emit(Event{Type: "state", State: StateProcessing})

	// The managed local engine may sit on another port by now.
	sttCfg, err := d.resolveSTT(s.cfg.STT)
	if err != nil {
		d.finishWithError(s, err, s.spool.Duration())
		return err
	}
	s.stt = sttCfg
	s.restored = true
	d.log.Info("restoring a cancelled dictation", "duration", s.spool.Duration())
	go d.finish(s)
	return nil
}
