//go:build linux

package x11

import (
	"errors"
	"sync"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

// The clipboard on X11 is not a store but a promise: whoever owns the
// CLIPBOARD selection hands the text over when an application asks. So Vito
// keeps a small hidden window that owns it for as long as nobody else takes it.

var clip struct {
	sync.Mutex
	c    *Conn
	win  xproto.Window
	text string
	own  bool
}

// SetClipboard puts text on the clipboard.
func SetClipboard(text string) error {
	clip.Lock()
	defer clip.Unlock()
	if clip.c == nil {
		c, err := Open()
		if err != nil {
			return err
		}
		id, err := c.NewId()
		if err != nil {
			c.Close()
			return err
		}
		win := xproto.Window(id)
		if err := xproto.CreateWindowChecked(c.Conn, 0, win, c.Root, -10, -10, 1, 1, 0,
			xproto.WindowClassInputOnly, 0, 0, nil).Check(); err != nil {
			c.Close()
			return err
		}
		clip.c, clip.win = c, win
		go serveClipboard(c, win)
	}
	clip.text, clip.own = text, true
	c := clip.c
	sel := c.Atom("CLIPBOARD")
	if err := xproto.SetSelectionOwnerChecked(c.Conn, clip.win, sel, xproto.TimeCurrentTime).Check(); err != nil {
		return err
	}
	r, err := xproto.GetSelectionOwner(c.Conn, sel).Reply()
	if err != nil || r.Owner != clip.win {
		return errors.New("could not take the clipboard")
	}
	return nil
}

// serveClipboard answers the applications that paste.
func serveClipboard(c *Conn, win xproto.Window) {
	for {
		ev, err := c.WaitForEvent()
		if ev == nil && err == nil {
			clip.Lock()
			clip.c, clip.own = nil, false
			clip.Unlock()
			return
		}
		switch e := ev.(type) {
		case xproto.SelectionClearEvent:
			clip.Lock()
			clip.own = false
			clip.Unlock()
		case xproto.SelectionRequestEvent:
			answer(c, e)
		}
	}
}

func answer(c *Conn, e xproto.SelectionRequestEvent) {
	clip.Lock()
	text, own := clip.text, clip.own
	clip.Unlock()
	prop := e.Property
	if prop == xproto.AtomNone {
		prop = e.Target // an obsolete client
	}
	utf8 := c.Atom("UTF8_STRING")
	switch {
	case !own:
		prop = xproto.AtomNone
	case e.Target == c.Atom("TARGETS"):
		targets := []xproto.Atom{c.Atom("TARGETS"), utf8, c.Atom("text/plain;charset=utf-8"), c.Atom("STRING"), c.Atom("TEXT")}
		buf := make([]byte, 4*len(targets))
		for i, a := range targets {
			xgb.Put32(buf[i*4:], uint32(a))
		}
		xproto.ChangeProperty(c.Conn, xproto.PropModeReplace, e.Requestor, prop, xproto.AtomAtom, 32, uint32(len(targets)), buf)
	case e.Target == utf8 || e.Target == c.Atom("text/plain;charset=utf-8") || e.Target == c.Atom("TEXT") || e.Target == xproto.AtomString:
		typ := utf8
		if e.Target == xproto.AtomString {
			typ = xproto.AtomString
		}
		xproto.ChangeProperty(c.Conn, xproto.PropModeReplace, e.Requestor, prop, typ, 8, uint32(len(text)), []byte(text))
	default:
		prop = xproto.AtomNone
	}
	ev := xproto.SelectionNotifyEvent{Time: e.Time, Requestor: e.Requestor, Selection: e.Selection, Target: e.Target, Property: prop}
	xproto.SendEvent(c.Conn, false, e.Requestor, 0, string(ev.Bytes()))
}

// ReadClipboard returns the clipboard's text, if it holds text.
func ReadClipboard() (string, bool) {
	clip.Lock()
	if clip.own {
		t := clip.text
		clip.Unlock()
		return t, true
	}
	clip.Unlock()
	c, err := Open()
	if err != nil {
		return "", false
	}
	defer c.Close()
	id, err := c.NewId()
	if err != nil {
		return "", false
	}
	win := xproto.Window(id)
	xproto.CreateWindow(c.Conn, 0, win, c.Root, -10, -10, 1, 1, 0, xproto.WindowClassInputOnly, 0, 0, nil)
	prop := c.Atom("VITO_CLIP")
	xproto.ConvertSelection(c.Conn, win, c.Atom("CLIPBOARD"), c.Atom("UTF8_STRING"), prop, xproto.TimeCurrentTime)
	got := make(chan bool, 1)
	go func() {
		for {
			ev, err := c.WaitForEvent()
			if ev == nil && err == nil {
				got <- false
				return
			}
			if e, ok := ev.(xproto.SelectionNotifyEvent); ok {
				got <- e.Property != xproto.AtomNone
				return
			}
		}
	}()
	select {
	case ok := <-got:
		if !ok {
			return "", false
		}
	case <-time.After(time.Second):
		return "", false
	}
	r, err := xproto.GetProperty(c.Conn, true, win, prop, xproto.GetPropertyTypeAny, 0, 1<<24).Reply()
	if err != nil || r.Format != 8 || r.Type == c.Atom("INCR") {
		return "", false
	}
	return string(r.Value), true
}
