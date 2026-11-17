//go:build darwin

package desktopcolors

import (
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// macOS (ui.accent "macos") is the accent colour chosen under System Settings
// → Appearance, on macOS' own window greys. The preference holds a number
// (AppleAccentColor, absent for the default blue, "Multicolour"), not a
// colour, so the colours are AppKit's controlAccentColor for each choice, per
// side. Read with defaults(1): the global domain is a binary plist, and the
// value changes rarely, so a process every few seconds costs nothing.
// Written without a Mac; CI's macOS job compiles it.

// macAccents: AppleAccentColor → light side, dark side.
var macAccents = map[int][2]string{
	-1: {"#8c8c8c", "#989898"}, // graphite
	0:  {"#e0383e", "#ff5257"}, // red
	1:  {"#f7821b", "#f7821b"}, // orange
	2:  {"#ffc600", "#ffc600"}, // yellow
	3:  {"#62ba46", "#62ba46"}, // green
	4:  {"#007aff", "#0a84ff"}, // blue
	5:  {"#953d96", "#a550a7"}, // purple
	6:  {"#f74f9e", "#f74f9e"}, // pink
}

var mac struct {
	mu      sync.Mutex
	cached  *Palette
	key     string
	checked time.Time
}

// MacOS returns the system's accent palette, the same *Palette while
// nothing changed (the pill compares pointers).
func MacOS() (*Palette, error) {
	mac.mu.Lock()
	defer mac.mu.Unlock()
	if time.Since(mac.checked) < 3*time.Second && mac.cached != nil {
		return mac.cached, nil
	}
	mac.checked = time.Now()
	accent, light := readMac()
	key := strconv.Itoa(accent) + "/" + strconv.FormatBool(light)
	if mac.cached == nil || key != mac.key {
		mac.cached, mac.key = macPalette(accent, light), key
	}
	return mac.cached, nil
}

// Windows: see windows.go.
func Windows() (*Palette, error) { return nil, ErrNone }

// SystemKey changes whenever the accent or the light/dark side does.
func SystemKey() string {
	a, l := readMac()
	return strconv.Itoa(a) + "/" + strconv.FormatBool(l)
}

func readMac() (accent int, light bool) {
	accent = 4
	if out, err := exec.Command("defaults", "read", "-g", "AppleAccentColor").Output(); err == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(string(out))); err == nil {
			if _, ok := macAccents[n]; ok {
				accent = n
			}
		}
	}
	out, err := exec.Command("defaults", "read", "-g", "AppleInterfaceStyle").Output()
	light = err != nil || !strings.EqualFold(strings.TrimSpace(string(out)), "dark")
	return accent, light
}

func macPalette(accent int, light bool) *Palette {
	c := macAccents[accent]
	onL, onD := "#ffffff", "#ffffff"
	if accent == 2 { // yellow takes dark text
		onL, onD = "#000000", "#000000"
	}
	p := &Palette{Source: "macos", Mode: "dark",
		Light: map[string]string{
			"primary": c[0], "secondary": mixHex(c[0], "#000000", .2), "tertiary": c[0], "on_primary": onL,
			"surface": "#ffffff", "surface_container_lowest": "#ffffff", "surface_container_low": "#f6f6f6",
			"surface_container": "#ececec", "surface_container_high": "#dcdcdc", "surface_container_highest": "#c8c8c8",
			"on_surface": "#1d1d1f", "on_surface_variant": "#5e5e62", "outline": "#8e8e93",
		},
		Dark: map[string]string{
			"primary": c[1], "secondary": mixHex(c[1], "#ffffff", .25), "tertiary": c[1], "on_primary": onD,
			"surface": "#2c2c2e", "surface_container_lowest": "#1c1c1e", "surface_container_low": "#1e1e1e",
			"surface_container": "#262628", "surface_container_high": "#3a3a3c", "surface_container_highest": "#48484a",
			"on_surface": "#f5f5f7", "on_surface_variant": "#c7c7cc", "outline": "#8e8e93",
		},
	}
	if light {
		p.Mode = "light"
	}
	return p
}
