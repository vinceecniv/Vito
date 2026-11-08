//go:build linux

package x11

import (
	"fmt"
	"time"

	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgb/xtest"
)

// Keystrokes go through XTEST, as if typed: the window with the keyboard gets
// them, whichever it is.

const (
	symShiftL  = 0xffe1
	symControl = 0xffe3
	symReturn  = 0xff0d
	symTab     = 0xff09
	symV       = 0x76
)

// Keys opens a connection that can press keys.
func Keys() (*Conn, error) {
	c, err := Open()
	if err != nil {
		return nil, err
	}
	if err := c.Ext("XTEST"); err != nil {
		c.Close()
		return nil, fmt.Errorf("no XTEST extension: %w", err)
	}
	return c, nil
}

func (c *Conn) fake(code xproto.Keycode, down bool) {
	t := byte(xproto.KeyRelease)
	if down {
		t = xproto.KeyPress
	}
	xtest.FakeInput(c.Conn, t, byte(code), 0, c.Root, 0, 0, 0)
}

// sync waits until the server has handled everything sent so far.
func (c *Conn) sync() { xproto.GetInputFocus(c.Conn).Reply() }

// Paste presses Ctrl+V.
func (c *Conn) Paste() error { return c.combo(symControl, symV) }

// Enter presses Return.
func (c *Conn) Enter() error { return c.combo(symReturn) }

// combo holds every key but the last while it taps the last.
func (c *Conn) combo(syms ...xproto.Keysym) error {
	km, err := c.keymap()
	if err != nil {
		return err
	}
	codes := make([]xproto.Keycode, len(syms))
	for i, s := range syms {
		cs, _ := km.codes(s)
		if len(cs) == 0 {
			return fmt.Errorf("no key gives keysym %#x", s)
		}
		codes[i] = cs[0]
	}
	for _, k := range codes {
		c.fake(k, true)
	}
	for i := len(codes) - 1; i >= 0; i-- {
		c.fake(codes[i], false)
	}
	c.sync()
	return nil
}

// Type types text. A character the keyboard has no key for is lent a spare
// keycode for the moment, the way xdotool does it.
func (c *Conn) Type(text string) error {
	km, err := c.keymap()
	if err != nil {
		return err
	}
	shift, _ := km.codes(symShiftL)
	spare, haveSpare := km.spare()
	lent := false
	defer func() {
		if lent {
			c.sync()
			time.Sleep(50 * time.Millisecond) // the last character must arrive first
			xproto.ChangeKeyboardMapping(c.Conn, 1, spare, byte(km.per), make([]xproto.Keysym, km.per))
			c.sync()
		}
	}()
	for _, r := range text {
		sym := keysymOf(r)
		codes, cols := km.codes(sym)
		switch {
		case len(codes) > 0 && (cols[0] == 0 || len(shift) > 0):
			if cols[0] == 1 {
				c.fake(shift[0], true)
			}
			c.fake(codes[0], true)
			c.fake(codes[0], false)
			if cols[0] == 1 {
				c.fake(shift[0], false)
			}
		case haveSpare:
			syms := make([]xproto.Keysym, km.per)
			for i := range syms {
				syms[i] = sym
			}
			c.sync()
			xproto.ChangeKeyboardMapping(c.Conn, 1, spare, byte(km.per), syms)
			c.sync()
			// Applications reload the map when told it changed; a key sent
			// right after would still be read with the old one.
			time.Sleep(20 * time.Millisecond)
			lent = true
			c.fake(spare, true)
			c.fake(spare, false)
			c.sync()
			time.Sleep(20 * time.Millisecond)
		default:
			return fmt.Errorf("no key for %q", r)
		}
	}
	c.sync()
	return nil
}

// keysymOf: Latin-1 characters are their own keysym, the rest of Unicode sits
// at 0x01000000 plus the code point.
func keysymOf(r rune) xproto.Keysym {
	switch {
	case r == '\n':
		return symReturn
	case r == '\t':
		return symTab
	case r < 0x100:
		return xproto.Keysym(r)
	}
	return xproto.Keysym(0x01000000 + r)
}
