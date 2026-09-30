package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// probeChat must tell "no chat route here" (LM Studio's native /api/v1, #50)
// apart from a server that has the route and merely rejects the empty body.
func TestProbeChat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/chat/completions":
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			w.WriteHeader(http.StatusBadRequest) // route exists, body is empty
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	if st := probeChat(ctx, srv.URL+"/v1/models", ""); st != http.StatusBadRequest {
		t.Errorf("OpenAI-compatible base: status %d, want 400", st)
	}
	if st := probeChat(ctx, srv.URL+"/api/v1/models", ""); st != http.StatusNotFound {
		t.Errorf("native API base: status %d, want 404", st)
	}
}
