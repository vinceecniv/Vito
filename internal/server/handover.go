package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"vito/internal/cloudsync"
	"vito/internal/config"
	"vito/internal/history"
	"vito/internal/uibundle"
)

// The browser version (vito.talk/app) and the app meet here. Two routes take
// requests from that website and nowhere else, and neither needs the token:
//
//   - GET /api/hello lets the page find out that the app runs on this
//     computer (CORS, plus the private-network preflight Chrome asks for).
//     It says nothing beyond "Vito, this version".
//   - POST /handover receives what the browser kept — dictations, day sums,
//     dictionary, a few settings — as a form post in a new tab, parks it for
//     a quarter of an hour and sends the tab on to the app's own page, where
//     the user sees what came in and decides. Nothing is imported until then,
//     and that step is an ordinary authenticated call from the app's page, so
//     a website can at most offer data, never write it.

// webOrigins are the sites allowed to talk to the app this way.
func webOrigins() []string {
	o := []string{"https://vito.talk"}
	for _, s := range strings.Split(os.Getenv("VITO_WEB_ORIGIN"), ",") {
		if s = strings.TrimSpace(s); s != "" {
			o = append(o, s)
		}
	}
	return o
}

func allowedWebOrigin(r *http.Request) (string, bool) {
	o := r.Header.Get("Origin")
	return o, o != "" && slices.Contains(webOrigins(), o)
}

func (s *Server) handleHello(w http.ResponseWriter, r *http.Request) {
	if o, ok := allowedWebOrigin(r); ok {
		w.Header().Set("Access-Control-Allow-Origin", o)
		w.Header().Set("Vary", "Origin")
		w.Header().Set("Access-Control-Allow-Private-Network", "true")
		w.Header().Set("Access-Control-Allow-Methods", "GET")
	}
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"app": "vito", "version": Version, "api": uibundle.API})
}

// handoverPayload is what standalone/backend.js exports.
type handoverPayload struct {
	Source       string                     `json:"source"`
	Config       *config.Config             `json:"config"`
	History      []history.Entry            `json:"history"`
	Days         map[string]history.DaySums `json:"days"`
	Achievements map[string]int64           `json:"achievements"`
}

type parked struct {
	p       handoverPayload
	expires time.Time
}

var (
	handoverMu sync.Mutex
	handovers  = map[string]parked{}
)

func (s *Server) handleHandover(w http.ResponseWriter, r *http.Request) {
	if _, ok := allowedWebOrigin(r); !ok {
		s.log.Info("hand-over refused", "origin", r.Header.Get("Origin"), "referer", r.Referer())
		http.Error(w, "not from the Vito website", http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<20)
	var p handoverPayload
	if err := json.Unmarshal([]byte(r.PostFormValue("data")), &p); err != nil {
		http.Error(w, "unreadable hand-over: "+err.Error(), http.StatusBadRequest)
		return
	}
	if p.Source == "" {
		p.Source = "browser"
	}
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	id := hex.EncodeToString(b)
	handoverMu.Lock()
	for k, v := range handovers {
		if time.Now().After(v.expires) {
			delete(handovers, k)
		}
	}
	handovers[id] = parked{p, time.Now().Add(15 * time.Minute)}
	handoverMu.Unlock()
	http.Redirect(w, r, "/?handover="+id, http.StatusSeeOther)
}

func takeHandover(id string, remove bool) (handoverPayload, bool) {
	handoverMu.Lock()
	defer handoverMu.Unlock()
	v, ok := handovers[id]
	if !ok || time.Now().After(v.expires) {
		return handoverPayload{}, false
	}
	if remove {
		delete(handovers, id)
	}
	return v.p, true
}

// handleHandoverSummary says what a parked hand-over holds, for the page to
// ask about it.
func (s *Server) handleHandoverSummary(w http.ResponseWriter, r *http.Request) {
	p, ok := takeHandover(r.PathValue("id"), false)
	if !ok {
		s.writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "this hand-over has expired — start it again from the browser"})
		return
	}
	var words int64
	days := 0
	for _, d := range p.Days {
		words += d.Words
		if d.Words > 0 {
			days++
		}
	}
	terms, fixes := 0, 0
	if p.Config != nil {
		terms, fixes = len(p.Config.Dictionary.Keyterms), len(p.Config.Dictionary.Corrections)
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "entries": len(p.History), "words": words,
		"days": days, "keyterms": terms, "corrections": fixes, "settings": p.Config != nil,
		"layout": p.Config != nil && len(p.Config.UI.Dashboard) > 0})
}

// handleHandoverApply imports a parked hand-over: the dictations and their
// day sums always, the dictionary and the settings when asked.
func (s *Server) handleHandoverApply(w http.ResponseWriter, r *http.Request) {
	var opt struct {
		Dictionary bool `json:"dictionary"`
		Settings   bool `json:"settings"`
		Layout     bool `json:"layout"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&opt)
	p, ok := takeHandover(r.PathValue("id"), true)
	if !ok {
		s.writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "this hand-over has expired — start it again from the browser"})
		return
	}
	added, err := s.hist.Import("web:"+p.Source, p.History, p.Days)
	if err != nil {
		s.writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	var ids []string
	for id := range p.Achievements {
		ids = append(ids, id)
	}
	_, _ = s.hist.RecordAchievements(ids)

	if p.Config != nil && (opt.Dictionary || opt.Settings || opt.Layout) {
		cfg := s.d.Config()
		if opt.Dictionary {
			cfg.Dictionary = cloudsync.MergeDictionary(cfg.Dictionary, p.Config.Dictionary)
		}
		if opt.Settings {
			// What someone sets while trying Vito: the language they speak and
			// see, how it looks, how fast they type. Nothing about providers.
			if l := p.Config.STT.Language; l != "" {
				cfg.STT.Language = l
			}
			cfg.UI.Lang, cfg.UI.Theme = p.Config.UI.Lang, p.Config.UI.Theme
			if p.Config.Stats.TypingSpeed != "" {
				cfg.Stats.TypingSpeed = p.Config.Stats.TypingSpeed
			}
		}
		// The dashboard as arranged in the browser: the app takes it whole,
		// rather than ending up with a different order on the same cards.
		if opt.Layout && len(p.Config.UI.Dashboard) > 0 {
			cfg.UI.Dashboard = p.Config.UI.Dashboard
		}
		if err := cfg.Validate(); err == nil {
			if err := cfg.Save(); err != nil {
				s.writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			s.d.UpdateConfig(&cfg)
		}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "added": added})
}
