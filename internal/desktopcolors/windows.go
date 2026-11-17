//go:build windows

package desktopcolors

import (
	"bytes"
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sys/windows/registry"
)

// Windows (ui.accent "windows") is the accent colour chosen under Settings →
// Personalisation → Colours, on Windows 11's own surfaces. Explorer keeps the
// accent with the shades it derives from it in AccentPalette: eight RGBA
// entries, from the lightest (Light3) through the accent itself to the
// darkest (Dark3), the last unused. Windows' own controls fill with Dark1 on
// the light side and Light2 on the dark side, so Vito does too.
const (
	accentKey  = `Software\Microsoft\Windows\CurrentVersion\Explorer\Accent`
	personaKey = `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`
)

var win struct {
	mu      sync.Mutex
	cached  *Palette
	key     string
	checked time.Time
}

// Windows returns the system's accent palette. Read at most once a second,
// and the same *Palette while nothing changed (the pill compares pointers).
func Windows() (*Palette, error) {
	win.mu.Lock()
	defer win.mu.Unlock()
	if time.Since(win.checked) < time.Second && win.cached != nil {
		return win.cached, nil
	}
	win.checked = time.Now()
	raw, light, err := readWindows()
	if err != nil {
		win.cached = nil
		return nil, err
	}
	key := fmt.Sprintf("%x/%v", raw, light)
	if win.cached != nil && key == win.key {
		return win.cached, nil
	}
	p, err := windowsPalette(raw, light)
	if err != nil {
		return nil, err
	}
	win.cached, win.key = p, key
	return p, nil
}

// MacOS: see macos.go.
func MacOS() (*Palette, error) { return nil, ErrNone }

// SystemKey changes whenever the system's accent or light/dark side does,
// for what watches the system anyway (the pill's theme watcher).
func SystemKey() string {
	raw, light, err := readWindows()
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%x/%v", raw, light)
}

func readWindows() (raw []byte, light bool, err error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, accentKey, registry.QUERY_VALUE)
	if err != nil {
		return nil, false, err
	}
	raw, _, err = k.GetBinaryValue("AccentPalette")
	k.Close()
	if err != nil {
		return nil, false, err
	}
	if k, err := registry.OpenKey(registry.CURRENT_USER, personaKey, registry.QUERY_VALUE); err == nil {
		v, _, err := k.GetIntegerValue("AppsUseLightTheme")
		k.Close()
		light = err == nil && v != 0
	}
	return bytes.Clone(raw), light, nil
}

// windowsPalette maps AccentPalette onto the Material roles, with the greys
// of Windows 11's Settings and Explorer for the surfaces (Mica's base, the
// cards on it, their strokes).
func windowsPalette(raw []byte, light bool) (*Palette, error) {
	if len(raw) < 28 {
		return nil, errors.New("AccentPalette: too short")
	}
	c := func(i int) string { return fmt.Sprintf("#%02x%02x%02x", raw[i*4], raw[i*4+1], raw[i*4+2]) }
	light3, light2, accent, dark1, dark2 := c(0), c(1), c(3), c(4), c(5)
	p := &Palette{Source: "windows", Mode: "dark",
		Light: map[string]string{
			"primary": dark1, "secondary": dark2, "tertiary": accent, "on_primary": "#ffffff",
			"surface": "#ffffff", "surface_container_lowest": "#ffffff", "surface_container_low": "#f9f9f9",
			"surface_container": "#f3f3f3", "surface_container_high": "#e5e5e5", "surface_container_highest": "#d1d1d1",
			"on_surface": "#1b1b1b", "on_surface_variant": "#5d5d5d", "outline": "#8a8a8a",
		},
		Dark: map[string]string{
			"primary": light2, "secondary": light3, "tertiary": accent, "on_primary": "#000000",
			"surface": "#2b2b2b", "surface_container_lowest": "#1c1c1c", "surface_container_low": "#202020",
			"surface_container": "#282828", "surface_container_high": "#3a3a3a", "surface_container_highest": "#454545",
			"on_surface": "#ffffff", "on_surface_variant": "#d0d0d0", "outline": "#9d9d9d",
		},
	}
	if light {
		p.Mode = "light"
	}
	return p, nil
}
