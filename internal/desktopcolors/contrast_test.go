package desktopcolors

import "testing"

func TestReadableOnPrimary(t *testing.T) {
	p := readable(&Palette{
		Light: map[string]string{"primary": "#6750a4", "on_primary": "#ffffff"},
		Dark:  map[string]string{"primary": "#d0bcff", "on_primary": "#381e72"},
	})
	if p.Light["on_primary"] != "#ffffff" || p.Dark["on_primary"] != "#381e72" {
		t.Fatalf("a legible on_primary was replaced: %v / %v", p.Light["on_primary"], p.Dark["on_primary"])
	}
	// White on a light accent (as Omarchy's background or a fixed white can be).
	p = readable(&Palette{Light: map[string]string{"primary": "#a6e3a1", "on_primary": "#ffffff"},
		Dark: map[string]string{"primary": "#1e66f5", "on_primary": "#89b4fa"}})
	for _, s := range []map[string]string{p.Light, p.Dark} {
		if c := Contrast(s["on_primary"], s["primary"]); c < minContrast {
			t.Errorf("on_primary %s on %s: %.2f:1", s["on_primary"], s["primary"], c)
		}
	}
}
