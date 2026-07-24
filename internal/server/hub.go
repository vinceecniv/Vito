package server

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// hub fans daemon events out to connected web-UI WebSockets. Slow clients
// get dropped rather than blocking the pipeline.
type hub struct {
	mu    sync.Mutex
	conns map[*websocket.Conn]chan []byte
}

func newHub() *hub {
	return &hub{conns: make(map[*websocket.Conn]chan []byte)}
}

func (h *hub) add(c *websocket.Conn) chan []byte {
	ch := make(chan []byte, 64)
	h.mu.Lock()
	h.conns[c] = ch
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
