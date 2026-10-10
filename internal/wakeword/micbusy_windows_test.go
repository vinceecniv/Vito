package wakeword

import "testing"

// Only checks that reading the registry works; the answer depends on what
// else is running.
func TestMicInUseByOthers(t *testing.T) {
	t.Logf("microphone in use by another program: %v", MicInUseByOthers())
}
