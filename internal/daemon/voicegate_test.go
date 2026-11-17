package daemon

import "testing"

// A quiet headset (floor ≈ 0.0003, speech peaking ≈ 0.02) and a noisy room
// (floor ≈ 0.01): speech counts on the first, the hiss alone not on the second.
func TestVoiceGate(t *testing.T) {
	var quiet voiceGate
	for range 50 {
		quiet.speaking(0.0003)
	}
	if !quiet.speaking(0.02) {
		t.Error("a quiet microphone's speech did not count")
	}
	if quiet.speaking(0.001) {
		t.Error("a quiet microphone's silence counted")
	}
	var noisy voiceGate
	for range 50 {
		noisy.speaking(0.01)
	}
	if noisy.speaking(0.012) {
		t.Error("room noise counted as speech")
	}
	if !noisy.speaking(0.05) {
		t.Error("speech over room noise did not count")
	}
}
