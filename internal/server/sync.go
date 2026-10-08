package server

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"time"

	"vito/internal/cloudsync"
	"vito/internal/config"
	"vito/internal/daemon"
)

// Cloud sync's routes (internal/cloudsync). Connecting runs in the browser:
// the page asks for the provider's sign-in URL and opens it, the provider
// sends the browser back to /oauth/callback, and that page closes the loop.

func (s *Server) startSync() {
	s.sync = cloudsync.New(s.log, s.hist, s.d.Config, s.updateConfig,
		func(st cloudsync.Status) { s.hub.broadcast(map[string]any{"type": "sync", "sync": st}) })
	// Every finished dictation is worth sharing; Kick waits for a quiet moment.
	s.d.AddEventListener(func(e daemon.Event) {
		if e.Type == "final" {
			s.sync.Kick()
		}
	})
	go s.sync.Run(context.Background())
}

// updateConfig changes the config the way a settings save does: validated,
// saved, applied.
func (s *Server) updateConfig(f func(*config.Config)) error {
	cfg := s.d.Config()
	cfg.Dictionary = config.Dictionary{
		Keyterms:    append([]string(nil), cfg.Dictionary.Keyterms...),
		Corrections: append([]config.Correction(nil), cfg.Dictionary.Corrections...),
	}
	f(&cfg)
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := cfg.Save(); err != nil {
		return err
	}
	s.d.UpdateConfig(&cfg)
	return nil
}

func (s *Server) handleSyncStatus(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, s.sync.Status())
}

func (s *Server) handleSyncConnect(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Provider string `json:"provider"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&body)
	p := cloudsync.ProviderByID(body.Provider)
	if p == nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "unknown provider"})
		return
	}
	u, err := cloudsync.BeginAuth(p)
	if err != nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "url": u})
}

// handleOAuthCallback is where the provider sends the browser back. It needs
// no token: the state it carries was handed out by BeginAuth and works once.
func (s *Server) handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page := func(title, msg string) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>Vito</title>
<body style="font-family:system-ui,sans-serif;max-width:32em;margin:15vh auto;padding:0 1em;color:#2B2440;background:#FAF6EF">
<h2>%s</h2><p>%s</p><p><a href="http://127.0.0.1:%d/">Vito</a></p></body>`, html.EscapeString(title), html.EscapeString(msg), s.port)
	}
	if e := q.Get("error"); e != "" {
		page("Not connected", q.Get("error_description")+" ("+e+")")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	p, refresh, err := cloudsync.FinishAuth(ctx, q.Get("state"), q.Get("code"))
	if err != nil {
		page("Not connected", err.Error())
		return
	}
	if err := s.sync.Connect(ctx, p, refresh); err != nil {
		page("Not connected", err.Error())
		return
	}
	// Back to the settings page, which picks the new state up from the event.
	http.Redirect(w, r, fmt.Sprintf("http://127.0.0.1:%d/?synced=%s", s.port, p.ID), http.StatusSeeOther)
}

func (s *Server) handleSyncNow(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	if err := s.sync.SyncNow(ctx); err != nil {
		s.writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error(), "sync": s.sync.Status()})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sync": s.sync.Status()})
}

func (s *Server) handleSyncDisconnect(w http.ResponseWriter, r *http.Request) {
	if err := s.sync.Disconnect(); err != nil {
		s.writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
