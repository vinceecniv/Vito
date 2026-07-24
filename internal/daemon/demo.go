package daemon

import (
	"context"
	"strings"
	"time"

	"vito/internal/demo"
)

// RunDemo replays fabricated dictations as live events for as long as ctx runs,
// so the status page and the detached window show a transcript arriving word by
// word with the meter moving along.
//
// It runs for the whole life of the daemon and checks the setting each round
// rather than being started once at boot: demo mode can be switched on from the
// banner at any moment, and the transcript has to start typing there and then.
//
// It emits events and nothing else: no microphone is opened, no text is
// injected, nothing is written to history. If a real dictation is in progress it
// stays quiet and waits, so demo mode never fights the state machine — and the
// state it emits is the UI's view only, never d.state.
func (d *Daemon) RunDemo(ctx context.Context) {
	texts := demo.Transcript()
	if len(texts) == 0 {
		return
	}
	for i := 0; ; i++ {
		if !d.demoSleep(ctx, 4*time.Second) {
			return
		}
		if !d.Config().Demo || d.busy() {
			continue // switched off, or a real dictation is running
		}
		if !d.demoDictation(ctx, texts[i%len(texts)].Raw, texts[i%len(texts)].Cleaned) {
			return
		}
	}
}

func (d *Daemon) busy() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.state != StateIdle || d.micTesting
}

// demoDictation plays one dictation: recording with growing partials and a
// level that tracks the speech, then processing, then the finished text.
// Returns false when ctx ended.
func (d *Daemon) demoDictation(ctx context.Context, raw, cleaned string) bool {
	words := strings.Fields(raw)
	d.emit(Event{Type: "state", State: StateRecording})

	for i := range words {
		// Bail out mid-sentence when demo mode is switched off (or a real
		// dictation starts) — leaving the UI parked on "recording" would look
		// like a hung session.
		if !d.Config().Demo || d.busy() {
			d.emit(Event{Type: "level", Level: 0})
			d.emit(Event{Type: "state", State: StateIdle})
			return true
		}
		d.emit(Event{Type: "partial", Text: strings.Join(words[:i+1], " ")})
		// A level curve with the shape of speech: loud on the word, dropping in
		// the gap after it, so the meter and the PiP wave have something to do.
		d.emit(Event{Type: "level", Level: 55 + int(dayLevel(i))})
		if !d.demoSleep(ctx, 210*time.Millisecond) {
			return false
		}
		d.emit(Event{Type: "level", Level: 12 + int(dayLevel(i+3))/3})
		if !d.demoSleep(ctx, 90*time.Millisecond) {
			return false
		}
	}

	d.emit(Event{Type: "level", Level: 0})
	d.emit(Event{Type: "state", State: StateProcessing})
	if !d.demoSleep(ctx, 900*time.Millisecond) {
		return false
	}

	d.emit(Event{Type: "final", Text: cleaned, Raw: raw, Cleaned: cleaned, Timings: Timings{
		Recording: time.Duration(len(words)) * 300 * time.Millisecond,
		SttFinal:  420 * time.Millisecond,
		Cleanup:   680 * time.Millisecond,
		Injected:  760 * time.Millisecond,
	}})
	d.emit(Event{Type: "state", State: StateIdle})
	return d.demoSleep(ctx, 5*time.Second)
}

// dayLevel gives a repeatable 0..40 wobble so the meter looks alive without a
// random source (which would make the demo different on every run).
func dayLevel(i int) int32 {
	h := int32(i*2654435761) >> 3
	if h < 0 {
		h = -h
	}
	return h % 40
}

func (d *Daemon) demoSleep(ctx context.Context, dur time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(dur):
		return true
	}
}
