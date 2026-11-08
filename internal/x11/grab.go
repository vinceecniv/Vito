//go:build linux

package x11

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jezek/xgb/xproto"
)

// A key grab on the root window gets a key combination wherever the keyboard
// is, press and release both — what hold-to-dictate needs, and what the
// Wayland portals took years to offer.

// ErrHeld: another program has grabbed the key already.
var ErrHeld = errors.New("another program holds the key")

// KeyEvent is a grabbed key going down or up; Index is its place in the list
// given to Grab.
type KeyEvent struct {
	Index int
	Down  bool
}

// Grabber holds grabs until Close.
type Grabber struct {
	c      *Conn
	keys   []grabKey
	events chan KeyEvent
	done   chan struct{}
	once   sync.Once
}

type grabKey struct {
	mods  uint16
	sym   xproto.Keysym
	codes []xproto.Keycode
}

// Lock-type modifiers the user may have on; a grab must hold with each of them.
var lockMods = []uint16{0, xproto.ModMaskLock, xproto.ModMask2, xproto.ModMaskLock | xproto.ModMask2}

// Grab takes the given key specs ("ctrl+shift+d", "menu"). A spec that cannot
// be grabbed — another program holds it — is an error naming it.
func Grab(specs ...string) (*Grabber, error) {
	c, err := Open()
	if err != nil {
		return nil, err
	}
	g := &Grabber{c: c, events: make(chan KeyEvent, 16), done: make(chan struct{})}
	for _, s := range specs {
		mods, sym, err := ParseSpec(s)
		if err != nil {
			c.Close()
			return nil, err
		}
		g.keys = append(g.keys, grabKey{mods: mods, sym: sym})
	}
	if err := g.grab(); err != nil {
		c.Close()
		return nil, err
	}
	go g.read()
	return g, nil
}

// Events delivers presses and releases; it closes with the connection.
func (g *Grabber) Events() <-chan KeyEvent { return g.events }

// Close lets the keys go.
func (g *Grabber) Close() {
	g.once.Do(func() {
		close(g.done)
		g.c.Close()
	})
}

// grab (re)takes every key on every keycode that gives its keysym: with
// caps:menu, Menu comes from two keys.
func (g *Grabber) grab() error {
	km, err := g.c.keymap()
	if err != nil {
		return err
	}
	for i := range g.keys {
		k := &g.keys[i]
		for _, code := range k.codes {
			for _, l := range lockMods {
				xproto.UngrabKey(g.c.Conn, code, g.c.Root, k.mods|l)
			}
		}
		k.codes = k.codes[:0]
		codes, _ := km.codes(k.sym)
		if len(codes) == 0 {
			return fmt.Errorf("no key on this keyboard gives %s", symName(k.sym))
		}
		for _, code := range codes {
			for _, l := range lockMods {
				if err := xproto.GrabKeyChecked(g.c.Conn, true, g.c.Root, k.mods|l, code,
					xproto.GrabModeAsync, xproto.GrabModeAsync).Check(); err != nil {
					return fmt.Errorf("%w: %s", ErrHeld, symName(k.sym))
				}
			}
			k.codes = append(k.codes, code)
		}
	}
	return nil
}

func (g *Grabber) match(code xproto.Keycode, state uint16) int {
	for i, k := range g.keys {
		for _, c := range k.codes {
			// A release reports the modifiers still held then, which may be
			// fewer; the keycode alone decides there.
			if c == code {
				return i
			}
		}
	}
	return -1
}

// read turns the server's events into KeyEvents. Auto-repeat on X11 is a
// release and a press with the same timestamp; that pair is dropped, so a held
// key reads as one press and one release.
func (g *Grabber) read() {
	defer close(g.events)
	raw := make(chan any, 32)
	go func() {
		defer close(raw)
		for {
			ev, err := g.c.WaitForEvent()
			if ev == nil && err == nil {
				return
			}
			if ev != nil {
				raw <- ev
			}
		}
	}()
	var pending *xproto.KeyReleaseEvent
	var timer <-chan time.Time
	for {
		select {
		case <-g.done:
			return
		case <-timer:
			if pending != nil {
				if i := g.match(pending.Detail, pending.State); i >= 0 {
					g.events <- KeyEvent{Index: i}
				}
				pending, timer = nil, nil
			}
		case ev, ok := <-raw:
			if !ok {
				return
			}
			switch e := ev.(type) {
			case xproto.KeyPressEvent:
				if pending != nil && pending.Detail == e.Detail && pending.Time == e.Time {
					pending, timer = nil, nil // auto-repeat
					continue
				}
				if pending != nil {
					if i := g.match(pending.Detail, pending.State); i >= 0 {
						g.events <- KeyEvent{Index: i}
					}
					pending, timer = nil, nil
				}
				if i := g.match(e.Detail, e.State); i >= 0 {
					g.events <- KeyEvent{Index: i, Down: true}
				}
			case xproto.KeyReleaseEvent:
				if pending != nil {
					if i := g.match(pending.Detail, pending.State); i >= 0 {
						g.events <- KeyEvent{Index: i}
					}
				}
				rel := e
				pending, timer = &rel, time.After(30*time.Millisecond)
			case xproto.MappingNotifyEvent:
				// The keyboard map changed (a layout switch, caps:menu taking
				// effect): the keys may sit on other keycodes now.
				g.grab()
			}
		}
	}
}

// ParseSpec reads Vito's key spec: modifiers joined by "+", then one key.
func ParseSpec(spec string) (mods uint16, sym xproto.Keysym, err error) {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(spec)), "+")
	key := strings.TrimSpace(parts[len(parts)-1])
	for _, p := range parts[:len(parts)-1] {
		switch strings.TrimSpace(p) {
		case "ctrl", "control":
			mods |= xproto.ModMaskControl
		case "alt":
			mods |= xproto.ModMask1
		case "shift":
			mods |= xproto.ModMaskShift
		case "win", "super", "meta", "logo", "cmd":
			mods |= xproto.ModMask4
		default:
			return 0, 0, fmt.Errorf("unknown modifier %q", p)
		}
	}
	named := map[string]xproto.Keysym{
		"space": 0x20, "enter": 0xff0d, "return": 0xff0d, "tab": 0xff09, "esc": 0xff1b, "escape": 0xff1b,
		"backspace": 0xff08, "delete": 0xffff, "insert": 0xff63, "home": 0xff50, "end": 0xff57,
		"pageup": 0xff55, "pagedown": 0xff56, "left": 0xff51, "up": 0xff52, "right": 0xff53, "down": 0xff54,
		"menu": 0xff67, "pause": 0xff13, "scrolllock": 0xff14, "printscreen": 0xff61, "print": 0xff61,
	}
	switch {
	case key == "":
		return 0, 0, fmt.Errorf("empty hotkey")
	case named[key] != 0:
		sym = named[key]
	case len(key) == 1 && key[0] > 0x20 && key[0] < 0x7f:
		sym = xproto.Keysym(key[0])
	case len(key) >= 2 && key[0] == 'f':
		var n int
		if _, err := fmt.Sscanf(key, "f%d", &n); err != nil || n < 1 || n > 24 {
			return 0, 0, fmt.Errorf("unknown key %q", key)
		}
		sym = xproto.Keysym(0xffbe + n - 1)
	default:
		return 0, 0, fmt.Errorf("unknown key %q", key)
	}
	return mods, sym, nil
}

func symName(s xproto.Keysym) string {
	switch s {
	case 0xff67:
		return "the Menu key"
	case 0x20:
		return "Space"
	}
	if s > 0x20 && s < 0x7f {
		return strings.ToUpper(string(rune(s)))
	}
	return fmt.Sprintf("keysym %#x", uint32(s))
}
