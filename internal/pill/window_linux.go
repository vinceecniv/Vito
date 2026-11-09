//go:build linux

package pill

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"vito/internal/inject"
	"vito/internal/x11"
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
	// The pointer. Each id is taken whether the object exists or not (an
	// unused one by a sync), so the ids after them run on without a gap.
	idSeat
	idCursorShape
	idSeatSync // the roundtrip that tells whether the seat has a pointer
	idPointer
	idCursorDevice
	idFirstFree // and on: made while the window lives (newID)
)

// Requests and events, from wayland.xml and wlr-layer-shell-unstable-v1.xml.
const (
	displaySync, displayGetRegistry = 0, 1

	registryBind = 0

	compositorCreateSurface, compositorCreateRegion = 0, 1

	surfaceAttach, surfaceSetInputRegion = 1, 5
	regionAdd                            = 1
	surfaceCommit, surfaceSetBufferScale = 6, 8
	surfaceDamageBuffer                  = 9

	shmCreatePool, poolCreateBuffer = 0, 0

	layerShellGetLayerSurface = 0
	layerTop, layerOverlay    = 2, 3

	layerSetSize, layerSetAnchor = 0, 1
	layerSetMargin               = 3
	layerAckConfigure            = 6
	anchorBottom                 = 2
	anchorTop                    = 1
	anchorLeft, anchorRight      = 4, 8

	formatARGB8888 = 0

	regionDestroy        = 0
	seatGetPointer       = 0
	seatCapPointer       = 1
	cursorShapeGetDevice = 1
	cursorDeviceSetShape = 1

	pointerEnter, pointerLeave, pointerMotion, pointerButton = 0, 1, 2, 3

	relativeGetPointer = 1 // zwp_relative_pointer_manager_v1
	relativeMotion     = 0 // zwp_relative_pointer_v1

	outputVersion, outputName = 4, 4 // the name event came with version 4

	handshakeTimeout = 2 * time.Second
)

type wlWindow struct {
	conn *net.UnixConn

	wmu sync.Mutex // one message at a time on the socket

	mu        sync.Mutex
	inUse     [2]bool // the compositor still reads this buffer
	next      int
	pending   uint32 // serial of a configure not yet acknowledged
	acked     bool
	shown     bool // a picture is up
	output    string
	started   bool // the registry's first listing is complete
	lost      error
	lostCh    chan struct{} // closed with the first error
	ready     chan struct{} // the registry's roundtrip is done
	mapped    chan struct{} // the first configure arrived
	mapOnce   sync.Once
	globals   map[string]global
	outputs   map[uint32]bool        // wl_output globals, by name
	onChange  func(outputs []uint32) // set by watchOutputs
	probe     map[uint32]uint32      // outputNamed: object id -> wl_output global
	probed    map[uint32]string      // and that global's connector name
	probeSync uint32                 // the roundtrip after the binds
	probeDone chan struct{}
	caps      uint32            // the seat's capabilities
	seatDone  chan struct{}     // the seat roundtrip is done
	shaped    bool              // the cursor-shape manager is bound
	nextID    uint32            // see newID
	pointerCh chan pointerEvent // nil: no pointer wanted
	entered   uint32            // the serial of the pointer's enter, for its cursor
	relPtr    uint32            // the relative pointer, once asked for (relative)
	cursor    uint32            // the shape shown
	lw, lh    int               // the size in logical pixels
	margin    int               // above the bottom edge of the screen
	shared    []byte
	stride    int
	height    int
	poolSize  int
}

type global struct{ name, version uint32 }

// openWindow opens the pill of a dictation: on output when given (the screen
// whose resting pill was clicked), else on the screen in use, which the
// compositor picks. Only the box in input takes the pointer.
func openWindow(output uint32, input [4]int) (window, error) {
	if x11.Session() {
		return openX11(output, surfaceW, surfaceH, marginBottom, input, true)
	}
	w, err := openLayer(layerOverlay, output, "vito:pill", surfaceW, surfaceH, marginBottom, input)
	if errors.Is(err, errUnsupported) {
		if c := gnomePill(); c != nil {
			return c.open("pill", output, surfaceW, surfaceH, marginBottom, input)
		}
	}
	return w, err
}

// openIslandWindow opens the island (island.go) on one screen, lw × lh
// logical pixels, above the resting pill it grows out of.
func openIslandWindow(output uint32, lw, lh int, input [4]int) (window, error) {
	if x11.Session() {
		return openX11(output, lw, lh, islandMargin, input, true)
	}
	w, err := openLayer(layerOverlay, output, "vito:island", lw, lh, islandMargin, input)
	if errors.Is(err, errUnsupported) {
		if c := gnomePill(); c != nil {
			return c.open("island", output, lw, lh, islandMargin, input)
		}
	}
	return w, err
}

// openIdleWindow opens the resting pill on one screen. It sits one layer lower,
// so a fullscreen window covers it, while a dictation still shows on top of one.
func openIdleWindow(output uint32, input [4]int) (window, error) {
	if x11.Session() {
		return openX11(output, surfaceW, surfaceH, marginBottom, input, false)
	}
	w, err := openLayer(layerTop, output, "vito:pill-idle", surfaceW, surfaceH, marginBottom, input)
	if errors.Is(err, errUnsupported) {
		if c := gnomePill(); c != nil {
			return c.open("idle", output, surfaceW, surfaceH, marginBottom, input)
		}
	}
	return w, err
}

func openLayer(layer, output uint32, namespace string, lw, lh, margin int, input [4]int) (window, error) {
	w, err := connect()
	if err != nil {
		return nil, err
	}
	w.lw, w.lh, w.margin = lw, lh, margin
	w.stride, w.height = lw*scale*4, lh*scale
	w.pointerCh = make(chan pointerEvent, 32)
	w.seatDone = make(chan struct{})
	w.nextID = idFirstFree
	go w.read()
	if err := w.setup(layer, output, namespace, input); err != nil {
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
	if x11.Session() {
		return watchX11Outputs(stop, changed)
	}
	if !available() {
		if c := gnomePill(); c != nil {
			return watchGnomeOutputs(c, stop, changed)
		}
	}
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

// outputNamed finds the screen called name (DP-1), 0 when there is none or
// the desktop does not say: the pinned island opens there again.
func outputNamed(name string) uint32 {
	if name == "" {
		return 0
	}
	if x11.Session() {
		return x11OutputNamed(name)
	}
	if !available() {
		return gnomeOutputNamed(name)
	}
	w, err := connect()
	if err != nil {
		return 0
	}
	defer w.close()
	go w.read()
	w.send(idDisplay, displayGetRegistry, nil, idRegistry)
	w.send(idDisplay, displaySync, nil, idSync)
	if w.await(w.ready) != nil {
		return 0
	}
	if w.outputSupport() < outputName {
		return 0
	}
	// Every id and the roundtrip's are set before the first bind goes out, so
	// the reader never sees them change. Ids run on from the registry's
	// roundtrip without a gap, in order.
	w.mu.Lock()
	globals := slices.Sorted(maps.Keys(w.outputs))
	w.probe, w.probed, w.probeDone = map[uint32]uint32{}, map[uint32]string{}, make(chan struct{})
	first := uint32(idSync + 1)
	for i, g := range globals {
		w.probe[first+uint32(i)] = g
	}
	w.probeSync = first + uint32(len(globals))
	w.mu.Unlock()
	for i, g := range globals {
		w.bind(g, "wl_output", outputVersion, first+uint32(i))
	}
	w.send(idDisplay, displaySync, nil, w.probeSync)
	if w.await(w.probeDone) != nil {
		return 0
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for g, n := range w.probed {
		if n == name {
			return g
		}
	}
	return 0
}

// probing takes outputNamed's events: the roundtrip's end and each name.
func (w *wlWindow) probing(object, opcode uint32, body []byte) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.probe == nil {
		return false
	}
	if object == w.probeSync && opcode == 0 {
		close(w.probeDone)
		return true
	}
	g, ok := w.probe[object]
	if !ok {
		return false
	}
	if opcode == outputName && len(body) > 4 {
		n := int(binary.LittleEndian.Uint32(body))
		if n > 0 && len(body) >= 4+n {
			w.probed[g] = string(body[4 : 4+n-1])
		}
	}
	return true
}

// available asks the compositor whether it offers layer-shell; an X11
// session has its own window (window_x11.go).
func available() bool {
	if x11.Session() {
		return true
	}
	w, err := connect()
	if err != nil {
		return false
	}
	defer w.close()
	go w.read()
	w.send(idDisplay, displayGetRegistry, nil, idRegistry)
	w.send(idDisplay, displaySync, nil, idSync)
	if w.await(w.ready) != nil {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	_, ok := w.globals["zwlr_layer_shell_v1"]
	return ok
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

func (w *wlWindow) setup(layer, output uint32, namespace string, input [4]int) error {
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
	// Only the pill takes the pointer; elsewhere clicks go through to whatever
	// is underneath.
	w.send(idCompositor, compositorCreateRegion, nil, idRegion)
	if input[2] > 0 && input[3] > 0 {
		w.send(idRegion, regionAdd, nil, uint32(input[0]), uint32(input[1]), uint32(input[2]), uint32(input[3]))
	}
	w.send(idSurface, surfaceSetInputRegion, nil, idRegion)

	msg := newMessage(idLayerShell, layerShellGetLayerSurface)
	msg.u32(idLayerSurface).u32(idSurface).u32(outputID).u32(layer).str(namespace)
	w.write(msg, nil)
	w.send(idLayerSurface, layerSetSize, nil, uint32(w.lw), uint32(w.lh))
	w.send(idLayerSurface, layerSetAnchor, nil, anchorBottom)
	w.send(idLayerSurface, layerSetMargin, nil, 0, 0, uint32(w.margin), 0)
	w.send(idSurface, surfaceCommit, nil)
	if err := w.await(w.mapped); err != nil {
		return err
	}
	if err := w.createBuffers(); err != nil {
		return err
	}
	return w.bindPointer()
}

// bindPointer binds the seat and the cursor-shape manager that shows a hand
// over the pill, and asks for the pointer once the seat said it has one.
func (w *wlWindow) bindPointer() error {
	w.mu.Lock()
	seat, okSeat := w.globals["wl_seat"]
	shape, okShape := w.globals["wp_cursor_shape_manager_v1"]
	w.mu.Unlock()
	if okSeat {
		w.bind(seat.name, "wl_seat", 1, idSeat)
	} else {
		w.send(idDisplay, displaySync, nil, idSeat) // takes the id
	}
	if okShape {
		w.bind(shape.name, "wp_cursor_shape_manager_v1", 1, idCursorShape)
	} else {
		w.send(idDisplay, displaySync, nil, idCursorShape)
	}
	// The capabilities come right after the bind, so before this is done.
	w.send(idDisplay, displaySync, nil, idSeatSync)
	if err := w.await(w.seatDone); err != nil {
		return err
	}
	w.mu.Lock()
	pointer := okSeat && w.caps&seatCapPointer != 0
	w.shaped = pointer && okShape
	w.mu.Unlock()
	if pointer {
		w.send(idSeat, seatGetPointer, nil, idPointer)
	} else {
		w.send(idDisplay, displaySync, nil, idPointer)
	}
	if w.shaped {
		w.send(idCursorShape, cursorShapeGetDevice, nil, idCursorDevice, idPointer)
	} else {
		w.send(idDisplay, displaySync, nil, idCursorDevice)
	}
	return nil
}

// newID hands out the next object id.
func (w *wlWindow) newID() uint32 {
	w.mu.Lock()
	defer w.mu.Unlock()
	id := w.nextID
	w.nextID++
	return id
}

// setInput makes box (x, y, w, h in logical pixels) the part of the window that
// takes the pointer, from the next picture on.
func (w *wlWindow) setInput(boxes ...[4]int) {
	id := w.newID()
	w.send(idCompositor, compositorCreateRegion, nil, id)
	for _, box := range boxes {
		w.send(id, regionAdd, nil, uint32(box[0]), uint32(box[1]), uint32(box[2]), uint32(box[3]))
	}
	w.send(idSurface, surfaceSetInputRegion, nil, id)
	w.send(id, regionDestroy, nil)
}

// setOffset moves the window from where it opened, the middle of the bottom
// of a screen screenW logical pixels wide. Layer-shell places a surface by
// anchors and margins: knowing the width, the left margin says it. Without it,
// the protocol's rule that a surface anchored to both sides with a fixed width
// is centred between the margins does, where the compositor keeps it (niri
// puts it against the left edge instead, which is why the width is asked for).
func (w *wlWindow) setOffset(x, up, screenW int) {
	switch {
	case x == 0 && up == 0:
		w.send(idLayerSurface, layerSetAnchor, nil, anchorBottom)
		w.send(idLayerSurface, layerSetMargin, nil, 0, 0, uint32(w.margin), 0)
	case screenW > 0:
		w.send(idLayerSurface, layerSetAnchor, nil, anchorBottom|anchorLeft)
		w.send(idLayerSurface, layerSetMargin, nil, 0, 0, uint32(int32(w.margin+up)), uint32(int32(screenW/2-w.lw/2+x)))
	default:
		w.send(idLayerSurface, layerSetAnchor, nil, anchorBottom|anchorLeft|anchorRight)
		w.send(idLayerSurface, layerSetMargin, nil, 0, uint32(int32(max(0, -2*x))), uint32(int32(w.margin+up)), uint32(int32(max(0, 2*x))))
	}
}

// setPlace puts the window's top left corner at left, top on its screen. It
// is anchored at the bottom all the same: a desktop places a layer surface in
// what other surfaces leave free, and measured from the bottom the window stays
// clear of a taskbar there (KDE's) just as the pill does.
func (w *wlWindow) setPlace(left, top, screenH int) {
	w.send(idLayerSurface, layerSetAnchor, nil, anchorBottom|anchorLeft)
	w.send(idLayerSurface, layerSetMargin, nil, 0, 0, uint32(int32(max(0, screenH-top-w.lh))), uint32(int32(left)))
}

// relative asks for the pointer's own motion, apart from where the window
// is: what a drag needs, since the window moving under the pointer changes the
// position it reports, a moment after the move was asked for.
func (w *wlWindow) relative() bool {
	w.mu.Lock()
	mgr, ok := w.globals["zwp_relative_pointer_manager_v1"]
	have := w.relPtr != 0
	pointer := w.caps&seatCapPointer != 0
	w.mu.Unlock()
	if have {
		return true
	}
	if !ok || !pointer {
		return false
	}
	m := w.newID()
	w.bind(mgr.name, "zwp_relative_pointer_manager_v1", 1, m)
	rp := w.newID()
	w.send(m, relativeGetPointer, nil, rp, idPointer)
	w.mu.Lock()
	w.relPtr = rp
	w.mu.Unlock()
	return true
}

func (w *wlWindow) relativeID() uint32 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.relPtr
}

// setCursor shows shape over the window, when the desktop does cursor shapes.
func (w *wlWindow) setCursor(shape uint32) {
	w.mu.Lock()
	ok := w.shaped && w.entered != 0 && w.cursor != shape
	if ok {
		w.cursor = shape
	}
	serial := w.entered
	w.mu.Unlock()
	if ok {
		w.send(idCursorDevice, cursorDeviceSetShape, nil, serial, shape)
	}
}

// pointer reports what the pointer does over the window.
func (w *wlWindow) pointer() <-chan pointerEvent { return w.pointerCh }

func (w *wlWindow) pointerEvent(e pointerEvent) {
	select {
	case w.pointerCh <- e:
		return
	default:
	}
	// Full: drop the oldest, so the newest position (or a click) is never
	// the one lost.
	select {
	case <-w.pointerCh:
	default:
	}
	select {
	case w.pointerCh <- e:
	default:
	}
}

func wlFixed(b []byte) float64 { return float64(int32(binary.LittleEndian.Uint32(b))) / 256 }

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
		w.send(idPool, poolCreateBuffer, nil, id, uint32(i*size), uint32(w.lw*scale), uint32(w.height), uint32(w.stride), formatARGB8888)
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
	w.send(idSurface, surfaceDamageBuffer, nil, 0, 0, uint32(w.lw*scale), uint32(w.height))
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
	// Before the cases below: outputNamed's outputs take ids they use too.
	case object > idSync && w.probing(object, opcode, body):
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
	case object == idSeat && opcode == 0 && len(body) >= 4: // capabilities
		w.mu.Lock()
		w.caps = binary.LittleEndian.Uint32(body)
		w.mu.Unlock()
	case object == idSeatSync && opcode == 0: // done
		close(w.seatDone)
	case object == idPointer && opcode == pointerEnter && len(body) >= 16:
		serial := binary.LittleEndian.Uint32(body)
		w.mu.Lock()
		shaped := w.shaped
		w.entered, w.cursor = serial, cursorShapePointer
		w.mu.Unlock()
		if shaped {
			w.send(idCursorDevice, cursorDeviceSetShape, nil, serial, cursorShapePointer)
		}
		w.pointerEvent(pointerEvent{kind: pointerIn, x: wlFixed(body[8:]), y: wlFixed(body[12:])})
	case object == idPointer && opcode == pointerLeave:
		w.pointerEvent(pointerEvent{kind: pointerOut})
	case object == idPointer && opcode == pointerMotion && len(body) >= 12:
		w.pointerEvent(pointerEvent{kind: pointerMove, x: wlFixed(body[4:]), y: wlFixed(body[8:])})
	case object == idPointer && opcode == pointerButton && len(body) >= 16:
		button := binary.LittleEndian.Uint32(body[8:])
		if binary.LittleEndian.Uint32(body[12:]) == 1 { // pressed
			w.pointerEvent(pointerEvent{kind: pointerClick, button: button})
		} else {
			w.pointerEvent(pointerEvent{kind: pointerRelease, button: button})
		}
	case opcode == relativeMotion && len(body) >= 16 && object == w.relativeID():
		w.pointerEvent(pointerEvent{kind: pointerDelta, x: wlFixed(body[8:]), y: wlFixed(body[12:])})
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
