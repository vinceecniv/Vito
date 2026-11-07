//go:build linux

package pill

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"vito/internal/inject"
)

// A Wayland client written out by hand, for the few requests the pill needs: a
// surface on the overlay layer. Going through libwayland would be the first cgo
// dependency on a system library, and the protocol is small enough not to.
//
// wlr-layer-shell is what makes it a pill rather than a window. It floats above
// everything including fullscreen apps, is placed by the app, gets no title bar
// and, with keyboard interactivity off, can never take the focus. That last part
// is the one that matters: a pill holding focus would swallow the paste it is
// announcing. GNOME's Mutter does not offer the protocol, and neither does
// Flatpak's filtered socket unless the direct link is in place.

// Objects with an id fixed by this client. Ids are ours to hand out, but they
// must run without a gap in the order the objects are made.
const (
	idDisplay uint32 = iota + 1
	idRegistry
	idSync
	idCompositor
	idShm
	idLayerShell
	idOutput
	idSurface
	idRegion
	idLayerSurface
	idPool
	idBuffer0
	idBuffer1
)

// Requests and events, from wayland.xml and wlr-layer-shell-unstable-v1.xml.
const (
	displaySync, displayGetRegistry = 0, 1

	registryBind = 0

	compositorCreateSurface, compositorCreateRegion = 0, 1

	surfaceAttach, surfaceSetInputRegion = 1, 5
	surfaceCommit, surfaceSetBufferScale = 6, 8
	surfaceDamageBuffer                  = 9

	shmCreatePool, poolCreateBuffer = 0, 0

	layerShellGetLayerSurface = 0
	layerTop, layerOverlay    = 2, 3

	layerSetSize, layerSetAnchor = 0, 1
	layerSetMargin               = 3
	layerAckConfigure            = 6
	anchorBottom                 = 2

	formatARGB8888 = 0

	outputVersion, outputName = 4, 4 // the name event came with version 4

	// How far above the bottom edge of the screen, in logical pixels.
	marginBottom = 28

	handshakeTimeout = 2 * time.Second
)

type wlWindow struct {
	conn *net.UnixConn

	wmu sync.Mutex // one message at a time on the socket

	mu       sync.Mutex
	inUse    [2]bool // the compositor still reads this buffer
	next     int
	pending  uint32 // serial of a configure not yet acknowledged
	acked    bool
	shown    bool // a picture is up
	output   string
	started  bool // the registry's first listing is complete
	lost     error
	lostCh   chan struct{} // closed with the first error
	ready    chan struct{} // the registry's roundtrip is done
	mapped   chan struct{} // the first configure arrived
	mapOnce  sync.Once
	globals  map[string]global
	outputs  map[uint32]bool        // wl_output globals, by name
	onChange func(outputs []uint32) // set by watchOutputs
	shared   []byte
	stride   int
	height   int
	poolSize int
}

type global struct{ name, version uint32 }

// openWindow opens the pill of a dictation on the screen in use: with no output
// named, the compositor picks the focused one.
func openWindow() (window, error) {
	return openLayer(layerOverlay, 0, "vito:pill")
}

// openIdleWindow opens the resting pill on one screen. It sits one layer lower,
// so a fullscreen window covers it, while a dictation still shows on top of one.
func openIdleWindow(output uint32) (window, error) {
	return openLayer(layerTop, output, "vito:pill-idle")
}

func openLayer(layer, output uint32, namespace string) (window, error) {
	w, err := connect()
	if err != nil {
		return nil, err
	}
	go w.read()
	if err := w.setup(layer, output, namespace); err != nil {
		w.close()
		return nil, err
	}
	return w, nil
}

func connect() (*wlWindow, error) {
	path := socketPath()
	if path == "" {
		return nil, errUnsupported
	}
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	return &wlWindow{
		conn:    conn,
		ready:   make(chan struct{}),
		mapped:  make(chan struct{}),
		lostCh:  make(chan struct{}),
		globals: map[string]global{},
		outputs: map[uint32]bool{},
		stride:  surfaceW * scale * 4,
		height:  surfaceH * scale,
	}, nil
}

// watchOutputs reports the screens now and whenever they change, until stop is
// closed or the connection ends, which it returns with.
func watchOutputs(stop <-chan struct{}, changed func(outputs []uint32)) error {
	w, err := connect()
	if err != nil {
		return err
	}
	defer w.close()
	w.onChange = changed
	go w.read()
	w.send(idDisplay, displayGetRegistry, nil, idRegistry)
	w.send(idDisplay, displaySync, nil, idSync)
	if err := w.await(w.ready); err != nil {
		return err
	}
	w.mu.Lock()
	_, ok := w.globals["zwlr_layer_shell_v1"]
	w.mu.Unlock()
	if !ok {
		return errUnsupported
	}
	w.outputsChanged()
	select {
	case <-stop:
		return nil
	case <-w.lostCh:
		return errors.New("compositor connection lost")
	}
}

func (w *wlWindow) outputsChanged() {
	w.mu.Lock()
	list := make([]uint32, 0, len(w.outputs))
	for name := range w.outputs {
		list = append(list, name)
	}
	fn := w.onChange
	w.mu.Unlock()
	if fn != nil {
		fn(list)
	}
}

// socketPath finds the compositor. In a Flatpak the direct link comes first:
// the managed socket filters layer-shell out.
func socketPath() string {
	if p := inject.HostWayland(); p != "" {
		return p
	}
	name := os.Getenv("WAYLAND_DISPLAY")
	if name == "" {
		return ""
	}
	if filepath.IsAbs(name) {
		return name
	}
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, name)
}

func (w *wlWindow) setup(layer, output uint32, namespace string) error {
	w.send(idDisplay, displayGetRegistry, nil, idRegistry)
	w.send(idDisplay, displaySync, nil, idSync)
	if err := w.await(w.ready); err != nil {
		return err
	}
	w.mu.Lock()
	compositor, okC := w.globals["wl_compositor"]
	shm, okS := w.globals["wl_shm"]
	shell, okL := w.globals["zwlr_layer_shell_v1"]
	w.mu.Unlock()
	if !okC || !okS || !okL {
		return errUnsupported
	}
	if compositor.version < 4 {
		return errors.New("wl_compositor too old")
	}
	w.bind(compositor.name, "wl_compositor", 4, idCompositor)
	w.bind(shm.name, "wl_shm", 1, idShm)
	w.bind(shell.name, "zwlr_layer_shell_v1", 1, idLayerShell)
	var outputID uint32
	if output != 0 {
		w.bind(output, "wl_output", min(outputVersion, w.outputSupport()), idOutput)
		outputID = idOutput
	} else {
		w.send(idDisplay, displaySync, nil, idOutput) // takes the id; no effect
	}

	w.send(idCompositor, compositorCreateSurface, nil, idSurface)
	w.send(idSurface, surfaceSetBufferScale, nil, scale)
	// An empty input region: clicks go through to whatever is underneath.
	w.send(idCompositor, compositorCreateRegion, nil, idRegion)
	w.send(idSurface, surfaceSetInputRegion, nil, idRegion)

	msg := newMessage(idLayerShell, layerShellGetLayerSurface)
	msg.u32(idLayerSurface).u32(idSurface).u32(outputID).u32(layer).str(namespace)
	w.write(msg, nil)
	w.send(idLayerSurface, layerSetSize, nil, surfaceW, surfaceH)
	w.send(idLayerSurface, layerSetAnchor, nil, anchorBottom)
	w.send(idLayerSurface, layerSetMargin, nil, 0, 0, marginBottom, 0)
	w.send(idSurface, surfaceCommit, nil)
	if err := w.await(w.mapped); err != nil {
		return err
	}
	return w.createBuffers()
}

func (w *wlWindow) outputSupport() uint32 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.globals["wl_output"].version
}

// connector is the screen's name (DP-1), once told. It comes ahead of
// the first configure, so an open window knows it; before wl_output version 4
// there is none.
func (w *wlWindow) connector() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.output
}

func (w *wlWindow) bind(name uint32, iface string, version, id uint32) {
	w.write(newMessage(idRegistry, registryBind).u32(name).str(iface).u32(version).u32(id), nil)
}

// createBuffers shares memory for two pictures with the compositor: one it is
// showing, one being drawn.
func (w *wlWindow) createBuffers() error {
	size := w.stride * w.height
	w.poolSize = size * 2
	fd, err := unix.MemfdCreate("vito-pill", unix.MFD_CLOEXEC)
	if err != nil {
		return fmt.Errorf("memfd: %w", err)
	}
	defer unix.Close(fd)
	if err := unix.Ftruncate(fd, int64(w.poolSize)); err != nil {
		return fmt.Errorf("truncate: %w", err)
	}
	mem, err := unix.Mmap(fd, 0, w.poolSize, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		return fmt.Errorf("mmap: %w", err)
	}
	w.mu.Lock()
	w.shared = mem
	w.mu.Unlock()
	w.send(idShm, shmCreatePool, unix.UnixRights(fd), idPool, uint32(w.poolSize))
	for i, id := range []uint32{idBuffer0, idBuffer1} {
		w.send(idPool, poolCreateBuffer, nil, id, uint32(i*size), surfaceW*scale, uint32(w.height), uint32(w.stride), formatARGB8888)
	}
	return nil
}

func (w *wlWindow) present(pixels []byte) error {
	w.mu.Lock()
	if w.lost != nil {
		err := w.lost
		w.mu.Unlock()
		return err
	}
	i := w.next
	if w.inUse[i] { // the compositor is behind, or the output is off; skip a frame
		w.mu.Unlock()
		return nil
	}
	w.inUse[i], w.next, w.shown = true, 1-i, true
	serial, ack := w.pending, !w.acked
	w.acked = true
	size := w.stride * w.height
	copy(w.shared[i*size:(i+1)*size], pixels)
	w.mu.Unlock()

	if ack {
		w.send(idLayerSurface, layerAckConfigure, nil, serial)
	}
	w.send(idSurface, surfaceAttach, nil, idBuffer0+uint32(i), 0, 0)
	w.send(idSurface, surfaceDamageBuffer, nil, 0, 0, surfaceW*scale, uint32(w.height))
	w.send(idSurface, surfaceCommit, nil)
	return nil
}

// close drops the connection, which ends every object on it.
func (w *wlWindow) close() {
	w.conn.Close()
	w.fail(net.ErrClosed)
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.shared != nil {
		unix.Munmap(w.shared)
		w.shared = nil
	}
}

func (w *wlWindow) await(ch chan struct{}) error {
	select {
	case <-ch:
		return nil
	case <-time.After(handshakeTimeout):
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.lost != nil {
			return w.lost
		}
		return errors.New("compositor did not answer")
	}
}

func (w *wlWindow) send(object, opcode uint32, rights []byte, args ...uint32) {
	m := newMessage(object, opcode)
	for _, a := range args {
		m.u32(a)
	}
	w.write(m, rights)
}

func (w *wlWindow) write(m *message, rights []byte) {
	w.wmu.Lock()
	defer w.wmu.Unlock()
	if _, _, err := w.conn.WriteMsgUnix(m.finish(), rights, nil); err != nil {
		w.fail(err)
	}
}

func (w *wlWindow) fail(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.lost == nil {
		w.lost = err
		close(w.lostCh)
	}
}

// gone reports the window's end, for one nobody draws to.
func (w *wlWindow) gone() <-chan struct{} { return w.lostCh }

// read handles the events the pill cares about, for as long as the connection
// lasts. Everything else the compositor says is skipped by its length.
func (w *wlWindow) read() {
	var head [8]byte
	var body []byte
	for {
		if _, err := io.ReadFull(w.conn, head[:]); err != nil {
			w.fail(err)
			return
		}
		object := binary.LittleEndian.Uint32(head[:])
		word := binary.LittleEndian.Uint32(head[4:])
		opcode, size := word&0xffff, int(word>>16)
		if size < 8 {
			w.fail(errors.New("malformed message"))
			return
		}
		body = append(body[:0], make([]byte, size-8)...)
		if _, err := io.ReadFull(w.conn, body); err != nil {
			w.fail(err)
			return
		}
		w.event(object, opcode, body)
	}
}

func (w *wlWindow) event(object, opcode uint32, body []byte) {
	switch {
	case object == idDisplay && opcode == 0: // error
		w.fail(protocolError(body))
	case object == idRegistry && opcode == 0: // global
		if name, iface, version, ok := parseGlobal(body); ok {
			w.mu.Lock()
			w.globals[iface] = global{name, version}
			if iface == "wl_output" {
				w.outputs[name] = true
			}
			live := w.onChange != nil && w.started
			w.mu.Unlock()
			if live {
				w.outputsChanged()
			}
		}
	case object == idRegistry && opcode == 1 && len(body) >= 4: // global_remove
		w.mu.Lock()
		delete(w.outputs, binary.LittleEndian.Uint32(body))
		w.mu.Unlock()
		w.outputsChanged()
	case object == idSync && opcode == 0: // done
		w.mu.Lock()
		w.started = true
		w.mu.Unlock()
		close(w.ready)
	case object == idLayerSurface && opcode == 0 && len(body) >= 4: // configure
		serial := binary.LittleEndian.Uint32(body)
		w.mu.Lock()
		w.pending, w.acked = serial, false
		// A window that shows one still picture has no next frame to answer in.
		settled := w.shown
		if settled {
			w.acked = true
		}
		w.mu.Unlock()
		if settled {
			w.send(idLayerSurface, layerAckConfigure, nil, serial)
			w.send(idSurface, surfaceCommit, nil)
		}
		w.mapOnce.Do(func() { close(w.mapped) })
	case object == idLayerSurface && opcode == 1: // closed
		w.fail(errors.New("closed by the compositor"))
	case object == idOutput && opcode == outputName:
		if len(body) > 4 {
			n := int(binary.LittleEndian.Uint32(body))
			if n > 0 && len(body) >= 4+n {
				w.mu.Lock()
				w.output = string(body[4 : 4+n-1])
				w.mu.Unlock()
			}
		}
	case object == idBuffer0 && opcode == 0: // release
		w.release(0)
	case object == idBuffer1 && opcode == 0:
		w.release(1)
	}
}

func (w *wlWindow) release(i int) {
	w.mu.Lock()
	w.inUse[i] = false
	w.mu.Unlock()
}

func parseGlobal(b []byte) (name uint32, iface string, version uint32, ok bool) {
	if len(b) < 8 {
		return
	}
	name = binary.LittleEndian.Uint32(b)
	n := int(binary.LittleEndian.Uint32(b[4:])) // includes the terminating zero
	padded := (n + 3) &^ 3
	if n == 0 || len(b) < 8+padded+4 {
		return
	}
	return name, string(b[8 : 8+n-1]), binary.LittleEndian.Uint32(b[8+padded:]), true
}

func protocolError(b []byte) error {
	if len(b) < 12 {
		return errors.New("wayland protocol error")
	}
	object := binary.LittleEndian.Uint32(b)
	code := binary.LittleEndian.Uint32(b[4:])
	n := int(binary.LittleEndian.Uint32(b[8:]))
	text := ""
	if n > 0 && len(b) >= 12+n {
		text = string(b[12 : 12+n-1])
	}
	return fmt.Errorf("wayland error on object %d, code %d: %s", object, code, text)
}

type message struct{ b []byte }

func newMessage(object, opcode uint32) *message {
	m := &message{b: make([]byte, 8, 64)}
	binary.LittleEndian.PutUint32(m.b, object)
	binary.LittleEndian.PutUint32(m.b[4:], opcode)
	return m
}

func (m *message) u32(v uint32) *message {
	m.b = binary.LittleEndian.AppendUint32(m.b, v)
	return m
}

func (m *message) str(s string) *message {
	m.u32(uint32(len(s) + 1))
	m.b = append(m.b, s...)
	m.b = append(m.b, 0)
	for len(m.b)%4 != 0 {
		m.b = append(m.b, 0)
	}
	return m
}

func (m *message) finish() []byte {
	opcode := binary.LittleEndian.Uint32(m.b[4:])
	binary.LittleEndian.PutUint32(m.b[4:], uint32(len(m.b))<<16|opcode)
	return m.b
}
