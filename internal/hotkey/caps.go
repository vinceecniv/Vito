package hotkey

import (
	"errors"
	"strings"
)

// isCaps reports whether a hotkey spec is Caps Lock on its own: the dictation
// key with hold, double-tap and lone tap (Gesture).
func isCaps(spec string) bool { return strings.EqualFold(strings.TrimSpace(spec), "capslock") }

// ErrNiriManual: Vito cannot change the niri config safely; the settings page
// then shows the lines to add by hand.
var ErrNiriManual = errors.New("change the niri config by hand")

// NiriCaps describes Caps Lock in a niri session; State is "" elsewhere.
type NiriCaps struct {
	State  string `json:"state"`            // on | off | partial
	Path   string `json:"path,omitempty"`   // the config file
	Manual string `json:"manual,omitempty"` // why Vito cannot change it itself
}
