//go:build linux

package hotkey

import "testing"

func TestAppIDFromUnit(t *testing.T) {
	cases := map[string]string{
		"app-vito-24527.scope":                     "vito",
		"app-niri-vito-1234.scope":                 "vito",
		"app-gnome-talk.vito.Vito-99.scope":        "talk.vito.Vito",
		"app-gnome-my\\x2dapp-5.scope":             "my-app",
		"app-flatpak-talk.vito.Vito@12345.service": "talk.vito.Vito",
		"app-vito.service":                         "vito",
		"run-p24527-i7334.scope":                   "",
		"session-2.scope":                          "",
		"app-.scope":                               "",
	}
	for unit, want := range cases {
		if got := appIDFromUnit(unit); got != want {
			t.Errorf("appIDFromUnit(%q) = %q, want %q", unit, got, want)
		}
	}
}
