package pill

import "sync"

// embedWindow is the pill's window when the island is pinned: instead of a
// surface of its own, its pictures go to the island, which lays them where its
// microphone button is, and the island passes on the pointer over them.
type embedWindow struct {
	mu     sync.Mutex
	frame  []byte // the latest picture, surfaceW×surfaceH at scale
	fresh  bool   // a picture came since the island last took one
	ptr    chan pointerEvent
	ended  chan struct{}
	closed bool
}

func newEmbedWindow() *embedWindow {
	return &embedWindow{
		frame: make([]byte, surfaceW*scale*surfaceH*scale*4),
		ptr:   make(chan pointerEvent, 32),
		ended: make(chan struct{}),
	}
}

func (e *embedWindow) present(pixels []byte) error {
	e.mu.Lock()
	copy(e.frame, pixels)
	e.fresh = true
	e.mu.Unlock()
	return nil
}

// take hands the island the latest picture, and whether it is new.
func (e *embedWindow) take(dst []byte) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	copy(dst, e.frame)
	fresh := e.fresh
	e.fresh = false
	return fresh
}

func (e *embedWindow) connector() string            { return "" }
func (e *embedWindow) gone() <-chan struct{}        { return e.ended }
func (e *embedWindow) pointer() <-chan pointerEvent { return e.ptr }
func (e *embedWindow) setInput(...[4]int)           {}

// end tells the pill its window is gone: the island closed or was unpinned.
func (e *embedWindow) end() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.closed {
		e.closed = true
		close(e.ended)
	}
}

func (e *embedWindow) close() { e.end() }

// forward passes a pointer event on to the pill, never blocking the island.
func (e *embedWindow) forward(ev pointerEvent) {
	select {
	case e.ptr <- ev:
	default:
	}
}
