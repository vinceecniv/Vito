package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"vito/internal/cloudsync"
	"vito/internal/config"
	"vito/internal/daemon"
)

// Sync's routes (internal/cloudsync): a folder that the user's own sync app
// keeps the same on every computer.

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
		Folder string `json:"folder"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&body)
	if err := s.sync.Connect(body.Folder); err != nil {
		s.writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sync": s.sync.Status()})
}

// handleSyncPick opens the system's own folder picker, on this computer —
// the page can't: a browser never hands a website a real path.
func (s *Server) handleSyncPick(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title string `json:"title"`
		Start string `json:"start"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&body)
	if body.Title == "" {
		body.Title = "Choose the folder your sync app keeps in sync"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	p, err := cloudsync.PickFolder(ctx, body.Title, body.Start)
	if errors.Is(err, cloudsync.ErrNoPicker) {
		s.writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "no_picker"})
		return
	}
	if err != nil {
		s.writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": p})
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
