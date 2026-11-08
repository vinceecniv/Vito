//go:build linux

package hotkey

import "testing"

func TestWithOption(t *testing.T) {
	for _, c := range []struct {
		in   string
		on   bool
		want string
	}{
		{"", true, "caps:menu"},
		{"grp:alt_shift_toggle", true, "grp:alt_shift_toggle,caps:menu"},
		{"caps:escape,compose:ralt", true, "compose:ralt,caps:menu"}, // one caps: option at a time
		{"caps:menu", true, "caps:menu"},
		{"compose:ralt,caps:menu", false, "compose:ralt"},
		{"caps:escape", false, "caps:escape"}, // not ours to remove
	} {
		if got := withOption(c.in, c.on); got != c.want {
			t.Errorf("withOption(%q, %v) = %q, want %q", c.in, c.on, got, c.want)
		}
	}
}
