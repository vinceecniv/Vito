//go:build linux

package pill

import (
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

// GNOME has no layer-shell, so there the pill is drawn by Vito's own GNOME
// Shell extension (gnome/extension.js, installed by gnomeext.go): Vito sends
// it the pictures over D-Bus and it puts them up as shell chrome, which never
// takes the keyboard. This file is Vito's end of that conversation; to the rest
// of the pill it is just another window.

const (
	gnomeName  = "talk.vito.Pill"
	gnomePath  = "/talk/vito/Pill"
	gnomeIface = "talk.vito.Pill"
)

type gnomeClient struct {
	conn *dbus.Conn
	obj  dbus.BusObject

	mu      sync.Mutex
	windows map[uint32]*gnomeWindow
	focus   []func(monitor int32) // FocusChanged listeners
	outputs []func()              // MonitorsChanged listeners
}

var (
	gnomeMu     sync.Mutex
	gnomeShared *gnomeClient
	gnomeSeen   time.Time // when the extension was last found on the bus
)

// gnomePill returns the client when the extension is running, else nil.
func gnomePill() *gnomeClient {
	gnomeMu.Lock()
	defer gnomeMu.Unlock()
	if gnomeShared == nil {
		conn, err := dbus.SessionBus()
		if err != nil {
			return nil
		}
		c := &gnomeClient{conn: conn, obj: conn.Object(gnomeName, gnomePath), windows: map[uint32]*gnomeWindow{}}
		if err := conn.AddMatchSignal(dbus.WithMatchInterface(gnomeIface)); err != nil {
			return nil
		}
		conn.AddMatchSignal(dbus.WithMatchSender("org.freedesktop.DBus"), dbus.WithMatchMember("NameOwnerChanged"),
			dbus.WithMatchArg(0, gnomeName))
		ch := make(chan *dbus.Signal, 64)
		conn.Signal(ch)
		go c.signals(ch)
		gnomeShared = c
	}
	// Asking the bus every time would cost a round trip per frame's checks.
	if time.Since(gnomeSeen) > 3*time.Second {
		var has bool
		if err := gnomeShared.conn.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, gnomeName).Store(&has); err != nil || !has {
			return nil
		}
		gnomeSeen = time.Now()
	}
	return gnomeShared
}

func (c *gnomeClient) signals(ch <-chan *dbus.Signal) {
	for sig := range ch {
		switch {
		case sig.Name == "org.freedesktop.DBus.NameOwnerChanged":
			if len(sig.Body) == 3 && sig.Body[0] == gnomeName && sig.Body[2] == "" {
				// The extension went away (disabled, the shell restarted).
				gnomeMu.Lock()
				gnomeSeen = time.Time{}
				gnomeMu.Unlock()
				c.mu.Lock()
				ws := c.windows
				c.windows = map[uint32]*gnomeWindow{}
				c.mu.Unlock()
				for _, w := range ws {
					w.end()
				}
			}
		case !strings.HasPrefix(sig.Name, gnomeIface+"."):
		case sig.Name == gnomeIface+".Pointer" && len(sig.Body) == 5:
			id, _ := sig.Body[0].(uint32)
			kind, _ := sig.Body[1].(uint32)
			x, _ := sig.Body[2].(float64)
			y, _ := sig.Body[3].(float64)
			button, _ := sig.Body[4].(uint32)
			c.mu.Lock()
			w := c.windows[id]
			c.mu.Unlock()
			if w != nil {
				select {
				case w.pointers <- pointerEvent{kind: pointerKind(kind), x: x, y: y, button: button}:
				default:
				}
			}
		case sig.Name == gnomeIface+".Closed" && len(sig.Body) == 1:
			id, _ := sig.Body[0].(uint32)
			c.mu.Lock()
			w := c.windows[id]
			delete(c.windows, id)
			c.mu.Unlock()
			if w != nil {
				w.end()
			}
		case sig.Name == gnomeIface+".FocusChanged" && len(sig.Body) == 1:
			m, _ := sig.Body[0].(int32)
			c.mu.Lock()
			fs := append([]func(int32){}, c.focus...)
			c.mu.Unlock()
			for _, f := range fs {
				f(m)
			}
		case sig.Name == gnomeIface+".MonitorsChanged":
			c.mu.Lock()
			fs := append([]func(){}, c.outputs...)
			c.mu.Unlock()
			for _, f := range fs {
				f()
			}
		}
	}
}

type gnomeMonitor struct {
	Index, Width, Height, X int32
	Scale                   float64
}

func (c *gnomeClient) monitors() ([]gnomeMonitor, error) {
	var list []gnomeMonitor
	err := c.obj.Call(gnomeIface+".Monitors", 0).Store(&list)
	return list, err
}

// gnomeConnector names a GNOME monitor: the shell's index is all the extension
// knows. Output ids are that index plus one, 0 being "the screen in use".
func gnomeConnector(index int32) string { return "gnome-" + strconv.Itoa(int(index)) }

type box4 struct{ X, Y, W, H int32 }

func toBox(b [4]int) box4 { return box4{int32(b[0]), int32(b[1]), int32(b[2]), int32(b[3])} }

func (c *gnomeClient) open(kind string, output uint32, lw, lh, margin int, input [4]int) (window, error) {
	var id uint32
	var on int32
	if err := c.obj.Call(gnomeIface+".Open", 0, kind, int32(output)-1, int32(lw), int32(lh), int32(margin), toBox(input)).Store(&id, &on); err != nil {
		return nil, err
	}
	w := &gnomeWindow{c: c, id: id, monitor: on, lw: lw, lh: lh, pointers: make(chan pointerEvent, 32), lost: make(chan struct{})}
	c.mu.Lock()
	c.windows[id] = w
	c.mu.Unlock()
	return w, nil
}

type gnomeWindow struct {
	c        *gnomeClient
	id       uint32
	monitor  int32
	lw, lh   int
	pointers chan pointerEvent
	lost     chan struct{}
	once     sync.Once
}

func (w *gnomeWindow) end() { w.once.Do(func() { close(w.lost) }) }

func (w *gnomeWindow) call(method string, args ...any) error {
	select {
	case <-w.lost:
		return errors.New("the GNOME extension closed the pill")
	default:
	}
	return w.c.obj.Call(gnomeIface+"."+method, 0, append([]any{w.id}, args...)...).Err
}

func (w *gnomeWindow) present(pixels []byte) error {
	return w.call("Present", int32(w.lw*scale), int32(w.lh*scale), pixels)
}

func (w *gnomeWindow) connector() string            { return gnomeConnector(w.monitor) }
func (w *gnomeWindow) gone() <-chan struct{}        { return w.lost }
func (w *gnomeWindow) pointer() <-chan pointerEvent { return w.pointers }

// setInput: the extension takes one box, so the one around them all.
func (w *gnomeWindow) setInput(boxes ...[4]int) { w.call("SetInput", toBox(inputArea(boxes).union())) }

func (w *gnomeWindow) close() {
	w.c.mu.Lock()
	delete(w.c.windows, w.id)
	w.c.mu.Unlock()
	w.call("Close")
	w.end()
}

// mover: the extension places in logical pixels of the monitor.
func (w *gnomeWindow) setOffset(x, up, screenW int) { w.call("Place", false, int32(x), int32(up)) }
func (w *gnomeWindow) setPlace(left, top, _ int)    { w.call("Place", true, int32(left), int32(top)) }
func (w *gnomeWindow) setCursor(shape uint32)       { w.call("SetCursor", shape) }

// relative: the extension sends the pointer's motion during a drag.
func (w *gnomeWindow) relative() bool { return true }

// watchGnomeOutputs reports the monitors now and on every change.
func watchGnomeOutputs(c *gnomeClient, stop <-chan struct{}, changed func(outputs []uint32)) error {
	report := func() error {
		list, err := c.monitors()
		if err != nil {
			return err
		}
		ids := make([]uint32, 0, len(list))
		for _, m := range list {
			ids = append(ids, uint32(m.Index)+1)
		}
		changed(ids)
		return nil
	}
	again := make(chan struct{}, 1)
	c.mu.Lock()
	c.outputs = append(c.outputs, func() {
		select {
		case again <- struct{}{}:
		default:
		}
	})
	n := len(c.outputs) - 1
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.outputs[n] = func() {}
		c.mu.Unlock()
	}()
	if err := report(); err != nil {
		return err
	}
	for {
		select {
		case <-stop:
			return nil
		case <-again:
			if err := report(); err != nil {
				return err
			}
		case <-time.After(5 * time.Second):
			if gnomePill() == nil {
				return errors.New("the GNOME extension went away")
			}
		}
	}
}

// gnomeFocus tells the screen in use and whether a window has the keyboard.
var gnomeFocus = focusSource{
	available: func() bool { return gnomePill() != nil },
	watch: func(stop <-chan struct{}, changed func(output string)) {
		c := gnomePill()
		if c == nil {
			return
		}
		var m int32
		var win bool
		if c.obj.Call(gnomeIface+".Focus", 0).Store(&m, &win) == nil {
			changed(gnomeConnector(m))
		}
		got := make(chan int32, 4)
		c.mu.Lock()
		c.focus = append(c.focus, func(m int32) {
			select {
			case got <- m:
			default:
			}
		})
		n := len(c.focus) - 1
		c.mu.Unlock()
		defer func() {
			c.mu.Lock()
			c.focus[n] = func(int32) {}
			c.mu.Unlock()
		}()
		for {
			select {
			case <-stop:
				return
			case m := <-got:
				changed(gnomeConnector(m))
			}
		}
	},
	window: func() (focused, known bool) {
		c := gnomePill()
		if c == nil {
			return false, false
		}
		var m int32
		var win bool
		if err := c.obj.Call(gnomeIface+".Focus", 0).Store(&m, &win); err != nil {
			return false, false
		}
		return win, true
	},
	screen: func(connector string) (w, h int, ok bool) {
		c := gnomePill()
		if c == nil {
			return 0, 0, false
		}
		list, err := c.monitors()
		if err != nil {
			return 0, 0, false
		}
		for _, m := range list {
			if gnomeConnector(m.Index) == connector {
				return int(m.Width), int(m.Height), true
			}
		}
		return 0, 0, false
	},
}

func init() { focusSources = append(focusSources, gnomeFocus) }

// gnomeAvailable: the extension is running.
func gnomeAvailable() bool { return gnomePill() != nil }

// gnomeOutputNamed finds the monitor gnomeConnector named, 0 when it is gone.
func gnomeOutputNamed(name string) uint32 {
	c := gnomePill()
	if c == nil {
		return 0
	}
	list, err := c.monitors()
	if err != nil {
		return 0
	}
	for _, m := range list {
		if gnomeConnector(m.Index) == name {
			return uint32(m.Index) + 1
		}
	}
	return 0
}
