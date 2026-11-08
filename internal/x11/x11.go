//go:build linux

// Package x11 is what Vito needs from an X11 session — Linux Mint's Cinnamon
// runs on X11 by default, and there none of the Wayland routes exist: no
// GlobalShortcuts or RemoteDesktop portal, no wl-copy, no layer-shell. X11 has
// its own answers, older and simpler: a key grab on the root window (press and
// release both), the XTEST extension for keystrokes, and the clipboard as a
// selection Vito owns and serves itself, so no xclip or xdotool is needed.
//
// Each user opens its own connection; the X server keeps them apart.
package x11

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/randr"
	"github.com/jezek/xgb/shape"
	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgb/xtest"
)

func init() {
	// xgb logs every protocol error it is not asked about to stderr.
	xgb.Logger = log.New(io.Discard, "", 0)
}

// Session reports whether Vito runs in an X11 session rather than on Wayland.
// XWayland also sets DISPLAY, so a Wayland socket rules X11 out.
func Session() bool {
	if os.Getenv("DISPLAY") == "" {
		return false
	}
	if t := os.Getenv("XDG_SESSION_TYPE"); t != "" {
		return t == "x11"
	}
	return os.Getenv("WAYLAND_DISPLAY") == ""
}

// Conn is a connection with the bits every user needs.
type Conn struct {
	*xgb.Conn
	Root   xproto.Window
	Screen *xproto.ScreenInfo

	mu    sync.Mutex
	atoms map[string]xproto.Atom
}

// Open connects to the X server of $DISPLAY.
func Open() (*Conn, error) {
	if !Session() {
		return nil, errors.New("not an X11 session")
	}
	extOnce.Do(warmExtensions)
	c, err := xgb.NewConn()
	if err != nil {
		return nil, err
	}
	s := xproto.Setup(c).DefaultScreen(c)
	return &Conn{Conn: c, Root: s.Root, Screen: s, atoms: map[string]xproto.Atom{}}, nil
}

// Extensions: xgb's Init writes package-level maps that every connection's
// event reader reads, without a lock, so it must not run while another
// connection is alive. The first Open therefore readies all of them once, on a
// connection of its own, and later connections only take the opcodes.
var (
	extOnce sync.Once
	extOps  map[string]byte
)

func warmExtensions() {
	extOps = map[string]byte{}
	c, err := xgb.NewConn()
	if err != nil {
		return
	}
	defer c.Close()
	for name, init := range map[string]func(*xgb.Conn) error{"RANDR": randr.Init, "SHAPE": shape.Init, "XTEST": xtest.Init} {
		if init(c) == nil {
			c.ExtLock.RLock()
			extOps[name] = c.Extensions[name]
			c.ExtLock.RUnlock()
		}
	}
}

// Ext readies an extension ("RANDR", "SHAPE", "XTEST") on this connection.
func (c *Conn) Ext(name string) error {
	op, ok := extOps[name]
	if !ok {
		return fmt.Errorf("the X server has no %s extension", name)
	}
	c.ExtLock.Lock()
	c.Extensions[name] = op
	c.ExtLock.Unlock()
	return nil
}

// Atom interns a name, remembered per connection.
func (c *Conn) Atom(name string) xproto.Atom {
	c.mu.Lock()
	a, ok := c.atoms[name]
	c.mu.Unlock()
	if ok {
		return a
	}
	r, err := xproto.InternAtom(c.Conn, false, uint16(len(name)), name).Reply()
	if err != nil {
		return 0
	}
	c.mu.Lock()
	c.atoms[name] = r.Atom
	c.mu.Unlock()
	return r.Atom
}

// Property reads a window property whole; nil when it is not there.
func (c *Conn) Property(w xproto.Window, name string) *xproto.GetPropertyReply {
	r, err := xproto.GetProperty(c.Conn, false, w, c.Atom(name), xproto.GetPropertyTypeAny, 0, 1<<20).Reply()
	if err != nil || r.Format == 0 {
		return nil
	}
	return r
}

// Cardinals reads a 32-bit property (CARDINAL, WINDOW, ATOM lists).
func (c *Conn) Cardinals(w xproto.Window, name string) []uint32 {
	r := c.Property(w, name)
	if r == nil || r.Format != 32 {
		return nil
	}
	out := make([]uint32, r.ValueLen)
	for i := range out {
		out[i] = xgb.Get32(r.Value[i*4:])
	}
	return out
}

// Resource returns one entry of the X resource database (RESOURCE_MANAGER on
// the root window), such as "Xft.dpi"; "" when unset.
func (c *Conn) Resource(name string) string {
	r := c.Property(c.Root, "RESOURCE_MANAGER")
	if r == nil {
		return ""
	}
	for _, line := range strings.Split(string(r.Value), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if ok && strings.TrimSpace(k) == name {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// keymap is the server's keycode → keysyms table.
type keymap struct {
	min, max xproto.Keycode
	per      int
	syms     []xproto.Keysym
}

func (c *Conn) keymap() (keymap, error) {
	setup := xproto.Setup(c.Conn)
	n := int(setup.MaxKeycode) - int(setup.MinKeycode) + 1
	r, err := xproto.GetKeyboardMapping(c.Conn, setup.MinKeycode, byte(n)).Reply()
	if err != nil {
		return keymap{}, err
	}
	return keymap{min: setup.MinKeycode, max: setup.MaxKeycode, per: int(r.KeysymsPerKeycode), syms: r.Keysyms}, nil
}

// codes lists every keycode that gives sym, with the column it is in (0 plain,
// 1 shifted).
func (k keymap) codes(sym xproto.Keysym) (codes []xproto.Keycode, cols []int) {
	for i := 0; i*k.per < len(k.syms); i++ {
		for col := 0; col < k.per && col < 2; col++ {
			if k.syms[i*k.per+col] == sym {
				codes = append(codes, k.min+xproto.Keycode(i))
				cols = append(cols, col)
				break
			}
		}
	}
	return
}

// spare finds a keycode that gives nothing, to lend a character to.
func (k keymap) spare() (xproto.Keycode, bool) {
	for i := len(k.syms)/k.per - 1; i >= 0; i-- {
		empty := true
		for col := 0; col < k.per; col++ {
			if k.syms[i*k.per+col] != 0 {
				empty = false
				break
			}
		}
		if empty {
			return k.min + xproto.Keycode(i), true
		}
	}
	return 0, false
}
