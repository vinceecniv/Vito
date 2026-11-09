package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"vito/internal/daemon"
	"vito/internal/longrec"
)

// Long recordings (internal/longrec, daemon/long.go).

func (s *Server) longRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/long", s.auth(s.handleLongList))
	mux.HandleFunc("POST /api/long/start", s.auth(s.handleLongStart))
	mux.HandleFunc("POST /api/long/pause", s.auth(s.longAction(s.d.PauseLong)))
	mux.HandleFunc("POST /api/long/resume", s.auth(s.longAction(s.d.ResumeLong)))
	mux.HandleFunc("POST /api/long/stop", s.auth(s.handleLongStop))
	mux.HandleFunc("POST /api/long/discard", s.auth(s.longAction(s.d.DiscardLong)))
	mux.HandleFunc("POST /api/long/mark", s.auth(s.handleLongMark))
	mux.HandleFunc("POST /api/long/header", s.auth(s.handleLongHeader))
	mux.HandleFunc("GET /api/long/{id}", s.auth(s.handleLongDoc))
	mux.HandleFunc("DELETE /api/long/{id}", s.auth(s.handleLongDelete))
	mux.HandleFunc("PUT /api/long/{id}/title", s.auth(s.handleLongTitle))
	mux.HandleFunc("POST /api/long/{id}/event", s.auth(s.handleLongEvent))
	mux.HandleFunc("POST /api/long/{id}/continue", s.auth(s.handleLongContinue))
	mux.HandleFunc("POST /api/long/{id}/restore", s.auth(s.longIDAction(s.d.RestoreLong)))
	mux.HandleFunc("POST /api/long/{id}/reclean", s.auth(s.longIDAction(s.d.RecleanLong)))
	mux.HandleFunc("GET /api/long/{id}/export", s.auth(s.handleLongExport))
	mux.HandleFunc("GET /api/long/{id}/audio", s.auth(s.handleLongAudio))
}

func (s *Server) longErr(w http.ResponseWriter, code int, err error) {
	s.writeJSON(w, code, map[string]any{"ok": false, "error": err.Error()})
}

func (s *Server) handleLongList(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"ok": true, "sessions": []longrec.Meta{}}
	if st, ok := s.d.LongStatus(); ok {
		out["running"] = st
	}
	store, err := s.d.LongStore()
	if err != nil {
		out["error"] = err.Error()
		s.writeJSON(w, http.StatusOK, out)
		return
	}
	if list, err := store.List(); err == nil && list != nil && !s.demo() {
		out["sessions"] = list
	}
	s.writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleLongStart(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title string `json:"title"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	st, err := s.d.StartLong(body.Title)
	if err != nil {
		s.longErr(w, http.StatusConflict, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "running": st})
}

func (s *Server) handleLongContinue(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !longrec.ValidID(id) {
		s.longErr(w, http.StatusBadRequest, errBadID)
		return
	}
	st, err := s.d.ContinueLong(id)
	if err != nil {
		s.longErr(w, http.StatusConflict, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "running": st})
}

func (s *Server) longAction(fn func() error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := fn(); err != nil {
			s.longErr(w, http.StatusConflict, err)
			return
		}
		s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

func (s *Server) longIDAction(fn func(id string) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !longrec.ValidID(id) {
			s.longErr(w, http.StatusBadRequest, errBadID)
			return
		}
		if err := fn(id); err != nil {
			s.longErr(w, http.StatusConflict, err)
			return
		}
		s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

type badID struct{}

func (badID) Error() string { return "invalid session id" }

var errBadID = badID{}

func (s *Server) handleLongStop(w http.ResponseWriter, r *http.Request) {
	id, err := s.d.StopLong()
	if err != nil {
		s.longErr(w, http.StatusConflict, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id})
}

func (s *Server) handleLongMark(w http.ResponseWriter, r *http.Request) {
	e, err := s.d.MarkLong()
	if err != nil {
		s.longErr(w, http.StatusConflict, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "event": e})
}

func (s *Server) handleLongHeader(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title string `json:"title"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	e, err := s.d.HeaderLong(body.Title)
	if err != nil {
		s.longErr(w, http.StatusConflict, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "event": e})
}

// longStore resolves the store and the {id} of a request, answering the
// error itself.
func (s *Server) longStore(w http.ResponseWriter, r *http.Request) (*longrec.Store, string, bool) {
	id := r.PathValue("id")
	if !longrec.ValidID(id) {
		s.longErr(w, http.StatusBadRequest, errBadID)
		return nil, "", false
	}
	store, err := s.d.LongStore()
	if err != nil {
		s.longErr(w, http.StatusServiceUnavailable, err)
		return nil, "", false
	}
	if _, err := store.Meta(id); err != nil {
		s.longErr(w, http.StatusNotFound, err)
		return nil, "", false
	}
	return store, id, true
}

func (s *Server) handleLongDoc(w http.ResponseWriter, r *http.Request) {
	store, id, ok := s.longStore(w, r)
	if !ok {
		return
	}
	d, err := store.Doc(id, r.URL.Query().Get("raw") == "1")
	if err != nil {
		s.longErr(w, http.StatusInternalServerError, err)
		return
	}
	s.writeJSON(w, http.StatusOK, d)
}

func (s *Server) handleLongDelete(w http.ResponseWriter, r *http.Request) {
	if _, id, ok := s.longStore(w, r); ok {
		if err := s.d.DeleteLong(id); err != nil {
			s.longErr(w, http.StatusConflict, err)
			return
		}
		s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

func (s *Server) handleLongTitle(w http.ResponseWriter, r *http.Request) {
	_, id, ok := s.longStore(w, r)
	if !ok {
		return
	}
	var body struct {
		Title string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.longErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.d.TitleLong(id, clipText(body.Title, 200)); err != nil {
		s.longErr(w, http.StatusInternalServerError, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleLongEvent adds a typed header title, a note on a mark, or deletes a
// header or mark (type title, note, delete). Headers and marks themselves are
// set while recording.
func (s *Server) handleLongEvent(w http.ResponseWriter, r *http.Request) {
	store, id, ok := s.longStore(w, r)
	if !ok {
		return
	}
	var e longrec.Event
	if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
		s.longErr(w, http.StatusBadRequest, err)
		return
	}
	switch e.Type {
	case "title", "note", "delete":
	default:
		s.longErr(w, http.StatusBadRequest, errBadEvent)
		return
	}
	if e.Of == "" {
		s.longErr(w, http.StatusBadRequest, errBadEvent)
		return
	}
	e = longrec.Event{Type: e.Type, Of: e.Of, Title: clipText(e.Title, 200), Text: clipText(e.Text, 1000)}
	e, err := store.AddEvent(id, e)
	if err != nil {
		s.longErr(w, http.StatusInternalServerError, err)
		return
	}
	s.d.Announce(daemon.Event{Type: "long_doc", Session: id})
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "event": e})
}

type badEvent struct{}

func (badEvent) Error() string { return "event must be title, note or delete, with of" }

var errBadEvent = badEvent{}

func clipText(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

func (s *Server) handleLongExport(w http.ResponseWriter, r *http.Request) {
	store, id, ok := s.longStore(w, r)
	if !ok {
		return
	}
	d, err := store.Doc(id, r.URL.Query().Get("raw") == "1")
	if err != nil {
		s.longErr(w, http.StatusInternalServerError, err)
		return
	}
	title := strings.TrimSpace(d.Meta.Title)
	when := d.Meta.Started.Format("2006-01-02 15:04")
	if title == "" {
		title = "Vito " + when
	}
	name := fileName(title)
	var body, ctype, ext string
	switch r.URL.Query().Get("format") {
	case "txt":
		body, ctype, ext = title+"\n\n"+d.Text()+"\n", "text/plain; charset=utf-8", ".txt"
	case "srt":
		body, ctype, ext = d.Subtitles(false), "application/x-subrip; charset=utf-8", ".srt"
	case "vtt":
		body, ctype, ext = d.Subtitles(true), "text/vtt; charset=utf-8", ".vtt"
	default:
		body, ctype, ext = d.Markdown(title, when), "text/markdown; charset=utf-8", ".md"
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+ext+`"`)
	_, _ = w.Write([]byte(body))
}

// fileName keeps a title safe as a download's name.
func fileName(title string) string {
	var b strings.Builder
	for _, r := range title {
		switch {
		case strings.ContainsRune(`<>:"/\|?*`, r) || r < 32:
			b.WriteRune('-')
		default:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(clipText(b.String(), 80))
}

func (s *Server) handleLongAudio(w http.ResponseWriter, r *http.Request) {
	store, id, ok := s.longStore(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", `attachment; filename="vito-`+id+`.wav"`)
	}
	http.ServeFile(w, r, store.AudioPath(id))
}
