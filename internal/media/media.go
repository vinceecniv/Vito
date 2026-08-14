// Package media quietens whatever is playing on the system while a dictation
// is in progress and restores it afterwards, so speech is not competing with
// music or video for either your ears or the microphone.
//
// Two actions are supported, selected per session:
//
//	"duck"  — lower the volume of playing streams, then restore it (default)
//	"pause" — pause playing players, then resume them
//	"off"   — do nothing
//
// Ducking is the default because it is seamless (no play/pause toggle to get
// wrong) and reaches apps that expose no media controls, such as browser video.
//
// Everything here is strictly best-effort: media control must never delay or
// break a dictation. Start() runs the platform work in a goroutine so the
// recording start path stays latency-free; Restore() waits for that work to
// finish and then undoes exactly what Start() did. When nothing was playing (or
// the platform has no backend) both calls are no-ops.
//
// The OS-specific backends live in the platform files: playerctl/pactl on
// Linux, the media transport key / WASAPI session volume on Windows, the
// scriptable players / system output volume on macOS, and a no-op elsewhere.
package media

import "log/slog"

// Action is the effect applied to playing media for the duration of a session.
type Action string

const (
	ActionOff   Action = "off"
	ActionPause Action = "pause"
	ActionDuck  Action = "duck"
)

// Session is the handle returned by Start; call Restore on it exactly once.
// A nil *Session is valid and turns Restore into a no-op, so callers can store
// the result of a conditional Start without nil checks.
type Session struct {
	log    *slog.Logger
	action Action
	done   chan struct{}
	token  any // platform restore state, produced by suppressPlatform
}

// Start applies action to any currently-playing media and returns immediately;
// the actual work runs on a background goroutine that Restore synchronises
// with. action "off" (or anything unknown) returns a nil, no-op Session.
func Start(action string, log *slog.Logger) *Session {
	a := Action(action)
	if a != ActionPause && a != ActionDuck {
		return nil
	}
	s := &Session{log: log, action: a, done: make(chan struct{})}
	go func() {
		defer close(s.done)
		s.token = suppressPlatform(a, log)
	}()
	return s
}

// Restore undoes what Start did. It blocks until the background work has
// completed, so the two can never race. Safe on a nil Session and when nothing
// was affected.
func (s *Session) Restore() {
	if s == nil {
		return
	}
	<-s.done
	if s.token == nil {
		return
	}
	restorePlatform(s.action, s.token, s.log)
}
