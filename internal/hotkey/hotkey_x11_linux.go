//go:build linux

package hotkey

import (
	"errors"
	"sync"

	"vito/internal/x11"
)

// On X11 (Linux Mint's Cinnamon, an X session of any desktop) none of the
// Wayland reasoning applies: an application may grab a key on the root window,
// and the server reports its release too. So there Vito owns its hotkey the way
// it does on Windows: the keys in its own settings, changed live (Rebind), Caps
// Lock reaching it as Menu. The GlobalShortcuts portal is not asked.

// PortalX11 is the Portal state of an X11 session: Vito grabbed the keys itself.
const PortalX11 = "x11"

type x11Grabs struct {
	mu     sync.Mutex
	toggle *x11.Grabber
	cancel *x11.Grabber
}

// bindX11 grabs the two keys, letting go of earlier grabs first.
func (m *Manager) bindX11(toggleSpec, cancelSpec string) {
	m.x11.mu.Lock()
	defer m.x11.mu.Unlock()
	for _, g := range []*x11.Grabber{m.x11.toggle, m.x11.cancel} {
		if g != nil {
			g.Close()
		}
	}
	m.x11.toggle, m.x11.cancel = nil, nil

	grab := func(spec string, down, up event, hasUp bool) (*x11.Grabber, BindInfo) {
		info := BindInfo{Spec: spec}
		if spec == "" {
			return nil, info
		}
		key := spec
		if isCaps(spec) {
			key = "menu"
		} else if mods, _, err := x11.ParseSpec(spec); err == nil && mods == 0 {
			// A key alone would be taken from every application.
			info.ErrCode = "invalid"
			return nil, info
		}
		g, err := x11.Grab(key)
		if err != nil {
			info.ErrCode = "invalid"
			if errors.Is(err, x11.ErrHeld) {
				info.ErrCode = "in_use"
			}
			m.log.Info("could not grab the hotkey", "spec", spec, "err", err)
			return nil, info
		}
		info.Registered = true
		go func() {
			for ev := range g.Events() {
				switch {
				case ev.Down:
					m.events <- down
				case hasUp:
					m.events <- up
				}
			}
		}()
		return g, info
	}
	var tInfo, cInfo BindInfo
	m.x11.toggle, tInfo = grab(toggleSpec, evToggleDown, evToggleUp, true)
	m.x11.cancel, cInfo = grab(cancelSpec, evCancel, evCancel, false)
	m.mu.Lock()
	m.toggle, m.cancel, m.supported = tInfo, cInfo, true
	m.state = PortalX11
	m.mu.Unlock()
	m.log.Info("hotkeys grabbed on X11", "toggle", toggleSpec, "ok", tInfo.Registered, "cancel", cancelSpec, "ok", cInfo.Registered)
}
