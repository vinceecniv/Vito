//go:build windows

package desktopcolors

import "testing"

func TestWindowsPalette(t *testing.T) {
	// Windows' default blue, as AccentPalette stores it.
	raw := []byte{0x99, 0xeb, 0xff, 0, 0x4c, 0xc2, 0xff, 0, 0x00, 0x91, 0xf8, 0, 0x00, 0x78, 0xd4, 0,
		0x00, 0x67, 0xc0, 0, 0x00, 0x3e, 0x92, 0, 0x00, 0x1a, 0x68, 0, 0xf7, 0x63, 0x0c, 0}
	p, err := windowsPalette(raw, true)
	if err != nil {
		t.Fatal(err)
	}
	if p.Mode != "light" || p.Light["primary"] != "#0067c0" || p.Dark["primary"] != "#4cc2ff" || p.Light["tertiary"] != "#0078d4" {
		t.Fatalf("got %s %v / %v", p.Mode, p.Light["primary"], p.Dark["primary"])
	}
	for _, side := range []map[string]string{p.Light, p.Dark} {
		for _, r := range Roles {
			if !reHex.MatchString(side[r]) {
				t.Errorf("role %s: %q", r, side[r])
			}
		}
	}
	if _, err := windowsPalette(raw[:20], false); err == nil {
		t.Error("a short value should fail")
	}
}
