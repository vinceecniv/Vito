package server

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/coder/websocket"

	"vito/internal/daemon"
)

// hub fans daemon events out to connected web-UI WebSockets. Slow clients
// get dropped rather than blocking the pipeline.
type hub struct {
	mu    sync.Mutex
	conns map[*websocket.Conn]chan []byte
	// kept is a "show" event no page was open to receive: the page that the
	// click opening Vito starts gets it as its first event (keptFor).
	kept   []byte
	keptAt time.Time
}

// keptFor is how long a kept "show" waits for the page to open: a web app
// can take a few seconds to start.
const keptFor = 30 * time.Second

func newHub() *hub {
	return &hub{conns: make(map[*websocket.Conn]chan []byte)}
}

func (h *hub) add(c *websocket.Conn) chan []byte {
	ch := make(chan []byte, 64)
	h.mu.Lock()
	h.conns[c] = ch
	if h.kept != nil && time.Since(h.keptAt) < keptFor {
		ch <- h.kept
	}
	h.kept = nil
	h.mu.Unlock()
	return ch
}

func (h *hub) remove(c *websocket.Conn) {
	h.mu.Lock()
	if ch, ok := h.conns[c]; ok {
		delete(h.conns, c)
		close(ch)
	}
	h.mu.Unlock()
}

func (h *hub) broadcast(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	h.mu.Lock()
	if e, ok := v.(daemon.Event); ok && e.Type == "show" && len(h.conns) == 0 {
		h.kept, h.keptAt = data, time.Now()
	}
	for _, ch := range h.conns {
		select {
		case ch <- data:
		default: // slow client; skip this event
		}
	}
	h.mu.Unlock()
}

// writeLoop pumps queued events to one connection until it dies.
func writeLoop(ctx context.Context, c *websocket.Conn, ch chan []byte) {
	for data := range ch {
		wctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := c.Write(wctx, websocket.MessageText, data)
		cancel()
		if err != nil {
			return
		}
	}
}
