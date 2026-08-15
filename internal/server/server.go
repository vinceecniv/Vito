// Package server exposes the local control API and the web UI on 127.0.0.1.
//
// Security posture (see design §2): loopback bind only, bearer-token auth on
// every endpoint, no CORS headers ever, and any browser request from a
// foreign origin is rejected. The web UI gets the token injected into the
// served index.html — never via CORS.
package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/coder/websocket"

	"vito/internal/achievements"
	"vito/internal/audio"
	"vito/internal/autostart"
	"vito/internal/backup"
	"vito/internal/config"
	"vito/internal/daemon"
	"vito/internal/demo"
	"vito/internal/history"
	"vito/internal/hotkey"
	"vito/internal/inject"
	"vito/internal/selfexe"
	"vito/internal/stt"
	"vito/internal/update"
	"vito/web"
)

type Server struct {
	d        *daemon.Daemon
	log      *slog.Logger
	audioCtx *audio.Context
	hist     *history.Store
	hk       *hotkey.Manager
	hub      *hub
	token    string
	port     int
	updates  *update.Checker

	fxMu   sync.Mutex
	fxRate float64   // cached USD→EUR
	fxAt   time.Time // when fxRate was fetched
}

func New(d *daemon.Daemon, log *slog.Logger, audioCtx *audio.Context, hist *history.Store, hk *hotkey.Manager, token string, port int) *Server {
	s := &Server{d: d, log: log, audioCtx: audioCtx, hist: hist, hk: hk, hub: newHub(), token: token, port: port,
		updates: update.NewChecker(Version)}
	d.OnEvent = func(e daemon.Event) { s.hub.broadcast(e) }
	return s
}

// ListenAndServe blocks. A port conflict returns a clear single-instance
// message instead of a raw bind error.
func (s *Server) ListenAndServe() error {
	addr := fmt.Sprintf("127.0.0.1:%d", s.port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		if errors.Is(err, syscall.EADDRINUSE) {
			return fmt.Errorf("port %d is already in use — is another 'vito serve' running?", s.port)
		}
		return err
	}
	srv := &http.Server{
		Handler:           s.routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	s.log.Info("listening", "addr", "http://"+addr)
	go s.autoBackupLoop()
	return srv.Serve(ln)
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("GET /ws", s.handleWS)

	// PWA assets: public (no token) so the browser can install and cache them.
	mux.HandleFunc("GET /manifest.webmanifest", s.static("application/manifest+json", web.Manifest, false))
	mux.HandleFunc("GET /sw.js", s.static("text/javascript", web.ServiceWorker, false))
	mux.HandleFunc("GET /favicon.svg", s.static("image/svg+xml", web.Favicon, true))
	mux.HandleFunc("GET /icon-192.png", s.static("image/png", web.Icon192, true))
	mux.HandleFunc("GET /icon-512.png", s.static("image/png", web.Icon512, true))
	mux.HandleFunc("GET /fonts-baloo2.woff2", s.static("font/woff2", web.FontBaloo2, true))
	mux.HandleFunc("GET /fonts-sora.woff2", s.static("font/woff2", web.FontSora, true))
	mux.HandleFunc("GET /flags/{name}", s.handleFlag)
	mux.HandleFunc("GET /i18n/{name}", s.handleI18n)
	// Public (no token): achievement medal art + animations, embedded in the binary.
	mux.HandleFunc("GET /achievements/{name}", s.handleAchievementAsset)
	mux.HandleFunc("GET /achievements/lottie/{name}", s.handleAchievementLottie)

	mux.HandleFunc("POST /api/toggle", s.auth(s.handleToggle))
	mux.HandleFunc("POST /api/start", s.auth(s.action(s.d.Start)))
	mux.HandleFunc("POST /api/stop", s.auth(s.action(s.d.Stop)))
	mux.HandleFunc("POST /api/cancel", s.auth(s.action(s.d.Cancel)))
	mux.HandleFunc("GET /api/status", s.auth(s.handleStatus))
	mux.HandleFunc("GET /api/devices", s.auth(s.handleDevices))
	mux.HandleFunc("GET /api/config", s.auth(s.handleGetConfig))
	mux.HandleFunc("PUT /api/config", s.auth(s.handlePutConfig))
	mux.HandleFunc("GET /api/hotkey", s.auth(s.handleGetHotkey))
	mux.HandleFunc("POST /api/accessibility", s.auth(s.handleRequestAccessibility))
	mux.HandleFunc("POST /api/hotkey/configure", s.auth(s.handleConfigureHotkey))
	mux.HandleFunc("POST /api/test-key", s.auth(s.handleTestKey))
	mux.HandleFunc("GET /api/costs", s.auth(s.handleCosts))
	mux.HandleFunc("GET /api/achievements", s.auth(s.handleAchievements))
	mux.HandleFunc("POST /api/achievements/{id}", s.auth(s.handleAchievementSet))
	mux.HandleFunc("POST /api/welcome-done", s.auth(s.handleWelcomeDone))
	mux.HandleFunc("GET /api/about", s.auth(s.handleAbout))
	mux.HandleFunc("GET /api/linux-tools", s.auth(s.handleLinuxTools))
	mux.HandleFunc("GET /api/autostart", s.auth(s.handleGetAutostart))
	mux.HandleFunc("PUT /api/autostart", s.auth(s.handlePutAutostart))
	mux.HandleFunc("GET /api/privacy", s.auth(s.handleGetPrivacy))
	mux.HandleFunc("PUT /api/privacy", s.auth(s.handlePutPrivacy))
	mux.HandleFunc("POST /api/mic-test", s.auth(s.handleMicTest))
	mux.HandleFunc("POST /api/play-sound", s.auth(s.handlePlaySound))
	mux.HandleFunc("GET /api/input-level", s.auth(s.handleGetInputLevel))
	mux.HandleFunc("PUT /api/input-level", s.auth(s.handlePutInputLevel))
	mux.HandleFunc("GET /api/stats", s.auth(s.handleStats))
	mux.HandleFunc("GET /api/history", s.auth(s.handleHistory))
	mux.HandleFunc("DELETE /api/history", s.auth(s.handleClearHistory))
	mux.HandleFunc("DELETE /api/history/{id}", s.auth(s.handleDeleteEntry))
	mux.HandleFunc("PUT /api/history/{id}/favorite", s.auth(s.handleFavorite))
	mux.HandleFunc("POST /api/history/{id}/inject", s.auth(s.handleReinject))
	mux.HandleFunc("GET /api/history/{id}/audio", s.auth(s.handleEntryAudio))
	mux.HandleFunc("POST /api/playback", s.auth(s.handlePlayback))
	mux.HandleFunc("POST /api/transcribe-file", s.auth(s.handleTranscribeFile))
	mux.HandleFunc("POST /api/quit", s.auth(s.handleQuit))
	mux.HandleFunc("GET /api/update", s.auth(s.handleUpdate))
	mux.HandleFunc("POST /api/update/apply", s.auth(s.handleUpdateApply))
	mux.HandleFunc("GET /api/backup", s.auth(s.handleBackupExport))
	mux.HandleFunc("POST /api/restore", s.auth(s.handleRestore))
	mux.HandleFunc("GET /api/backups", s.auth(s.handleBackupList))
	mux.HandleFunc("POST /api/backups", s.auth(s.handleBackupNow))
	mux.HandleFunc("GET /api/backups/{name}", s.auth(s.handleBackupDownload))
	mux.HandleFunc("POST /api/backups/{name}/restore", s.auth(s.handleBackupRestore))
	return s.rejectCrossOrigin(mux)
}

// ownOrigin is the only origin browsers may send requests from.
func (s *Server) ownOrigin() string {
	return fmt.Sprintf("http://127.0.0.1:%d", s.port)
}

// rejectCrossOrigin blocks CSRF-style requests from web pages: only our own
// UI's origin is accepted; requests without an Origin header (CLI, curl)
// pass through to token auth.
func (s *Server) rejectCrossOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" && origin != s.ownOrigin() {
			http.Error(w, "cross-origin requests are not allowed", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+s.token {
			s.writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "unauthorized"})
			return
		}
		next(w, r)
	}
}

// handleIndex serves the web UI with the auth token injected (design §2:
// the UI receives the token in the page, never via CORS).
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	page := bytes.ReplaceAll(web.Index, []byte("__VITO_TOKEN__"), []byte(s.token))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(page)
}

// static serves an embedded asset with the given content type. cacheable assets
// get a long cache lifetime; the service worker must not be cached so updates
// propagate.
func (s *Server) static(contentType string, body []byte, cacheable bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		if cacheable {
			w.Header().Set("Cache-Control", "public, max-age=86400")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		_, _ = w.Write(body)
	}
}

var flagName = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)?\.svg$`)
var i18nName = regexp.MustCompile(`^[a-z]{2}\.json$`)

// handleI18n serves an embedded UI translation file. Public (no token): it's
// static UI text with no secrets, needed before the app is authenticated.
func (s *Server) handleI18n(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !i18nName.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	data, err := web.I18n.ReadFile("i18n/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	// Must revalidate: these files are baked into the binary and change with it,
	// and a day-old copy of a language file leaves the interface half-translated
	// after an update — with no way for the user to tell why.
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(data)
}

var achAssetName = regexp.MustCompile(`^[a-z0-9._-]+$`)

// handleAchievementAsset serves an embedded medal asset — a static PNG or the
// Lottie player script. Public (no token): static art/code with no secrets, and
// the unlock toast can surface it before the page is otherwise in focus.
func (s *Server) handleAchievementAsset(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !achAssetName.MatchString(name) || strings.Contains(name, "..") {
		http.NotFound(w, r)
		return
	}
	var ct string
	switch {
	case strings.HasSuffix(name, ".png"):
		ct = "image/png"
	case strings.HasSuffix(name, ".js"):
		ct = "text/javascript"
	default:
		http.NotFound(w, r)
		return
	}
	data, err := web.Achievements.ReadFile("achievements/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(data)
}

// handleAchievementLottie serves one embedded medal animation (Lottie JSON).
func (s *Server) handleAchievementLottie(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !achAssetName.MatchString(name) || strings.Contains(name, "..") || !strings.HasSuffix(name, ".json") {
		http.NotFound(w, r)
		return
	}
	data, err := web.Achievements.ReadFile("achievements/lottie/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(data)
}

// achievementImages lists the ids that have a medal PNG shipped; achievementLotties
// lists those that also have an unlock animation. The UI uses the PNG for every
// medal and plays the animation, when present, on unlock and hover.
func achievementImages() []string  { return achAssetIDs("achievements", ".png") }
func achievementLotties() []string { return achAssetIDs("achievements/lottie", ".json") }

func achAssetIDs(dir, ext string) []string {
	entries, err := web.Achievements.ReadDir(dir)
	if err != nil {
		return nil
	}
	var ids []string
	for _, e := range entries {
		if n := e.Name(); strings.HasSuffix(n, ext) {
			ids = append(ids, strings.TrimSuffix(n, ext))
		}
	}
	return ids
}

// Version is the app version string, set from main at startup.
var Version = "dev"

// handleAbout returns app identity, license and build/version info. The commit
// comes from the VCS stamp Go embeds automatically when building from a repo.
func (s *Server) handleAbout(w http.ResponseWriter, r *http.Request) {
	var commit, ctime string
	var modified bool
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, kv := range bi.Settings {
			switch kv.Key {
			case "vcs.revision":
				commit = kv.Value
			case "vcs.time":
				ctime = kv.Value
			case "vcs.modified":
				modified = kv.Value == "true"
			}
		}
	}
	short := commit
	if len(short) > 12 {
		short = short[:12]
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"name":        "Vito",
		"tagline":     "Voice In, Text Out",
		"version":     Version,
		"commit":      short,
		"commit_time": ctime,
		"modified":    modified,
		"license":     "AGPL-3.0-or-later",
	})
}

// handleFlag serves an embedded country-flag SVG. The name is restricted to a
// bare "<code>.svg" so it can't escape the embedded flags directory.
func (s *Server) handleFlag(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !flagName.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	data, err := web.Flags.ReadFile("flags/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(data)
}

// handleWS upgrades the web UI's event stream. Token arrives as a query
// parameter (set by the injected page); Origin is verified.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("token") != s.token {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{fmt.Sprintf("127.0.0.1:%d", s.port)},
	})
	if err != nil {
		s.log.Debug("ws accept failed", "err", err)
		return
	}
	ch := s.hub.add(c)
	defer s.hub.remove(c)
	defer c.Close(websocket.StatusNormalClosure, "")

	// Reader detects disconnect; the UI never sends messages.
	go func() {
		for {
			if _, _, err := c.Read(r.Context()); err != nil {
				s.hub.remove(c)
				return
			}
		}
	}()
	writeLoop(r.Context(), c, ch)
}

func (s *Server) handleToggle(w http.ResponseWriter, r *http.Request) {
	state, err := s.d.Toggle()
	if err != nil {
		s.writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": err.Error(), "state": state})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "state": state})
}

func (s *Server) action(fn func() error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := fn(); err != nil {
			s.writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		s.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "state": s.d.Status().State})
	}
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	st := s.d.Status()
	// The last recording may since have been pruned, deleted with its entry or
	// wiped by switching the setting off — don't advertise a dead download.
	if st.LastRecordingID != "" {
		if _, ok := audio.RecordingPath(st.LastRecordingID); !ok || s.demo() {
			st.LastRecordingID = ""
		}
	}
	st.Credit = s.d.CreditOut() // providers currently out of credit, for the UI card
	s.writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleDevices(w http.ResponseWriter, r *http.Request) {
	in, err := s.audioCtx.CaptureDevices()
	if err != nil {
		s.writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	out, err := s.audioCtx.PlaybackDevices()
	if err != nil {
		s.writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"input": in, "output": out})
}

// demo reports whether the UI should be served fabricated sample data instead
// of the user's own. Set with "demo": true in the config file.
func (s *Server) demo() bool { return s.d.Config().Demo }

func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	cfg := s.d.Config()
	if s.demo() {
		// Swapped in for display only — the stored dictionary is never touched,
		// and handlePutConfig keeps it that way.
		cfg.Dictionary = demo.Dictionary()
	}
	s.writeJSON(w, http.StatusOK, cfg)
}

// handlePutConfig validates, saves and applies a new configuration without
// restart. Port and token changes take effect on the next restart.
func (s *Server) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	current := s.d.Config()
	// Decoding into cfg writes through the slice headers it shares with current,
	// so anything we mean to restore afterwards has to be copied out first —
	// a plain struct copy would be aliased and get overwritten in place.
	savedDict := config.Dictionary{
		Keyterms:    append([]string(nil), current.Dictionary.Keyterms...),
		Corrections: append([]config.Correction(nil), current.Dictionary.Corrections...),
	}
	cfg := current // start from current so omitted fields keep their value
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&cfg); err != nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid JSON: " + err.Error()})
		return
	}
	// The local API is not allowed to move or re-key the server itself.
	cfg.Server = current.Server
	// In demo mode the UI is showing (and would send back) the sample
	// dictionary, so keep the real one — saving any settings change from a demo
	// must not overwrite the user's keyterms and corrections.
	if s.demo() {
		cfg.Dictionary = savedDict
		// Demo mode bows out the moment a speech-recognition key is entered:
		// that's the end of onboarding, and from then on there is real data to
		// look at. Deliberately on the transition from empty, not on "a key
		// exists" — otherwise someone with a key configured could never switch
		// demo mode on to show the app to somebody. The banner's off-switch
		// works regardless: that PUT simply carries demo=false.
		if strings.TrimSpace(current.STT.APIKey) == "" && strings.TrimSpace(cfg.STT.APIKey) != "" {
			cfg.Demo = false
		}
	}
	if err := cfg.Validate(); err != nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if err := cfg.Save(); err != nil {
		s.writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.d.UpdateConfig(&cfg)
	// Switching the recordings off has to actually remove the audio that is
	// already on disk — otherwise "off" would only mean "no new ones".
	if current.History.StoreAudio && !cfg.History.StoreAudio {
		_ = audio.RemoveAllRecordings()
	}
	// A changed global hotkey is re-registered live, so the UI's conflict badge
	// reflects the new combination immediately.
	if cfg.HotkeyWindows != current.HotkeyWindows || cfg.HotkeyCancelWindows != current.HotkeyCancelWindows {
		s.hk.Rebind(cfg.HotkeyWindows, cfg.HotkeyCancelWindows)
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleGetHotkey reports the active global hotkeys, whether they registered,
// and this executable's path (so the Linux instructions can show a real command).
func (s *Server) handleGetHotkey(w http.ResponseWriter, r *http.Request) {
	toggle, cancel, supported := s.hk.Status()
	cfg := s.d.Config()
	exe, _ := selfexe.Path()
	bind := func(b hotkey.BindInfo, configured string) map[string]any {
		return map[string]any{
			"spec":       b.Spec,
			"configured": configured,
			"registered": b.Registered,
			"error":      b.ErrCode,
		}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"os":        runtime.GOOS,
		"supported": supported,
		"exe":       exe,
		"toggle":    bind(toggle, cfg.HotkeyWindows),
		"cancel":    bind(cancel, cfg.HotkeyCancelWindows),
		// Whether the desktop can actually open its own shortcut editor for Vito.
		// Only GlobalShortcuts v2 has ConfigureShortcuts, and the portal frontend
		// lists the method whatever the backend supports — so ask the manager,
		// which knows the version, rather than assuming.
		"configurable": s.hk.CanConfigure(),
		// macOS gates both the hotkey and pasting behind one permission; the
		// settings page offers to ask for it when this is false. Always true
		// elsewhere, so the UI can read it without checking the OS first.
		"accessibility": inject.Accessible(),
	})
}

// handleRequestAccessibility triggers the OS permission prompt for synthesising
// keystrokes and reports whether the right is held afterwards.
//
// It is deliberately a user action rather than a startup check: macOS shows its
// prompt only once per app per login, so asking in the background would burn it
// at a moment nobody is looking at the screen. "false" is the normal answer even
// on success — the user still has to tick the box in System Settings, and macOS
// only reports the new state to a fresh process.
func (s *Server) handleRequestAccessibility(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"granted": inject.RequestAccessibility(),
	})
}

// handleConfigureHotkey asks the desktop to open its shortcut editor for Vito.
// On Wayland the binding belongs to the user, not the app, so this is how the
// settings page offers to change it without inventing a key-capture UI.
func (s *Server) handleConfigureHotkey(w http.ResponseWriter, r *http.Request) {
	if err := s.hk.Configure(); err != nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleLinuxTools reports which optional external helpers Vito relies on are
// present on this Linux host, so the web UI can show a live "what's installed"
// report. Non-Linux hosts get os != "linux" and an empty list; the UI hides the
// card there. Detection is cheap (PATH lookups + a socket stat), safe to re-run.
func (s *Server) handleLinuxTools(w http.ResponseWriter, r *http.Request) {
	if runtime.GOOS != "linux" {
		s.writeJSON(w, http.StatusOK, map[string]any{"os": runtime.GOOS, "present": map[string]bool{}})
		return
	}
	names := []string{"wl-copy", "wl-paste", "wtype", "ydotool", "notify-send", "pactl"}
	present := make(map[string]bool, len(names))
	for _, n := range names {
		_, err := exec.LookPath(n)
		present[n] = err == nil
	}
	// ydotoold has no binary on PATH worth reporting; what matters is whether its
	// socket exists (client default location or Fedora's system-service path).
	socket := ""
	for _, p := range []string{
		filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), ".ydotool_socket"),
		"/tmp/.ydotool_socket",
	} {
		if p == ".ydotool_socket" {
			continue // XDG_RUNTIME_DIR unset
		}
		if _, err := os.Stat(p); err == nil {
			socket = p
			break
		}
	}
	present["ydotoold"] = socket != ""
	// Which of the three delivery routes is actually in play. With a fallback
	// chain, "it works" and "it works the way you think" are different claims —
	// this is the first thing worth knowing when delivery misbehaves.
	s.writeJSON(w, http.StatusOK, map[string]any{
		"os":             runtime.GOOS,
		"present":        present,
		"ydotool_socket": socket,
		"backend":        inject.ActiveBackend(s.d.Config().Injection),
		// Inside a Flatpak most of this card is beside the point: the helpers are
		// bundled or forbidden, not something the user installs. The UI needs to
		// know which story to tell, and only the daemon can see the sandbox.
		"flatpak": inject.Sandboxed(),
	})
}

// handleCosts estimates spend from the retained usage aggregates and the
// configured provider rates: the running month-to-date total (broken down into
// speech-recognition and AI-cleanup), plus a projected monthly cost from the
// rolling 30-day usage. Amounts are converted to the configured currency.
// costRates returns the configured provider rates, the USD→display-currency
// factor and that currency. Shared by the cost card and the per-day amounts in
// the weekly chart's tooltip.
// sttRateUSD is the published streaming price per hour for each speech model,
// plus the keyterms add-on where it isn't included. Source: AssemblyAI's
// realtime pricing page. The rate follows the selected model, so switching to
// Pro immediately shows what it actually costs.
// An empty model means Vito sends no speech_model at all and AssemblyAI picks
// one from the language code. That is not the cheap English-only tier: the
// billing dashboard shows those sessions charged as "Realtime Universal-3.5
// Pro", which is also why a Dutch language code works on it.
func sttRateUSD(provider, model string, keyterms bool) float64 {
	if provider == "soniox" {
		return 0.12 // one realtime model, one price, all languages
	}
	switch model {
	case "universal-3-5-pro", "":
		return 0.45 // keyterm prompting is included in this model
	case "universal-streaming-multilingual", "universal-streaming-english":
		if keyterms {
			return 0.15 + 0.04 // keyterms prompting is an add-on here
		}
		return 0.15
	}
	return 0 // unknown model: fall back to the configured rate
}

func (s *Server) costRates() (sttHr, inRate, outRate, fx float64, currency string) {
	cfg := s.d.Config()
	c := cfg.Costs
	sttHr = nonZero(sttRateUSD(cfg.STT.Provider, cfg.STT.Model, cfg.STT.KeytermsEnabled), nonZero(c.SttPerHourUSD, 0.15))
	inRate = nonZero(c.CleanupInPerMTokUSD, 1.0)
	outRate = nonZero(c.CleanupOutPerMTokUSD, 5.0)
	currency = c.Currency
	if currency == "" {
		currency = "eur"
	}
	fx = 1.0
	if currency == "eur" {
		fx = s.usdToEur()
	}
	return
}

// assistTokenRates returns the per-Mtok input/output USD rates for Vito Assist
// command tokens: the cleanup rates when Assist borrows the cleanup model, or
// its own rates (from the settings page, 0 = free) when it runs a heavier model.
func (s *Server) assistTokenRates(cleanupIn, cleanupOut float64) (float64, float64) {
	cfg := s.d.Config()
	if cfg.Assist.UsesCleanupModel() {
		return cleanupIn, cleanupOut
	}
	return cfg.Costs.AssistInPerMTokUSD, cfg.Costs.AssistOutPerMTokUSD
}

func (s *Server) handleCosts(w http.ResponseWriter, r *http.Request) {
	sttHr, inRate, outRate, rate, currency := s.costRates()
	aInRate, aOutRate := s.assistTokenRates(inRate, outRate)

	sttUSD := func(durMS int64) float64 { return float64(durMS) / 3_600_000.0 * sttHr }
	// Transcribed uploads are billed at the provider's pre-recorded rate, which
	// is lower than what a streaming session costs.
	uploadHr := stt.UploadRateUSD(s.d.Config().STT.Provider)
	uploadUSD := func(durMS int64) float64 { return float64(durMS) / 3_600_000.0 * uploadHr }
	cleanUSD := func(in, out int64) float64 { return float64(in)/1e6*inRate + float64(out)/1e6*outRate }
	// Assist commands may run on their own model, so they price at their own rate.
	assistUSD := func(in, out int64) float64 { return float64(in)/1e6*aInRate + float64(out)/1e6*aOutRate }

	now := time.Now()
	loc := now.Location()
	today := now.Format("2006-01-02")
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc).Format("2006-01-02")
	day30Start := now.AddDate(0, 0, -29).Format("2006-01-02")

	costTotals := s.hist.CostTotals
	if s.demo() {
		costTotals = func(from, to string) (int64, int64, int64, int64, int64, int64, string, error) {
			d, i, o, f := demo.CostTotals(now, from, to)
			return d, 0, i, o, 0, 0, f, nil
		}
	}

	mDur, mUp, mIn, mOut, mCmdIn, mCmdOut, _, err := costTotals(monthStart, today)
	if err != nil {
		s.writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	mStt, mClean, mCmd := sttUSD(mDur)+uploadUSD(mUp), cleanUSD(mIn, mOut), assistUSD(mCmdIn, mCmdOut)

	// Cost over the window the status page is showing, so the card can name a
	// second figure next to the month-to-date one. Same day arithmetic as the
	// statistics: -1 is yesterday, 0 is all time.
	periodTotal := 0.0
	if v := r.URL.Query().Get("days"); v != "" {
		if days, e := strconv.Atoi(v); e == nil && days >= -1 {
			anchor := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
			if days == -1 {
				anchor, days = anchor.AddDate(0, 0, -1), 1
			}
			from := ""
			if days > 0 {
				from = anchor.AddDate(0, 0, -(days - 1)).Format("2006-01-02")
			}
			if pDur, pUp, pIn, pOut, pCmdIn, pCmdOut, _, e := costTotals(from, anchor.Format("2006-01-02")); e == nil {
				periodTotal = sttUSD(pDur) + uploadUSD(pUp) + cleanUSD(pIn, pOut) + assistUSD(pCmdIn, pCmdOut)
			}
		}
	}

	rDur, rUp, rIn, rOut, rCmdIn, rCmdOut, rFirst, err := costTotals(day30Start, today)
	if err != nil {
		s.writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	rTotalUSD := sttUSD(rDur) + uploadUSD(rUp) + cleanUSD(rIn, rOut) + assistUSD(rCmdIn, rCmdOut)
	// Project to a full month using the average per active day (days with data
	// in the window), so a fresh install isn't understated.
	activeDays := 30
	if rFirst != "" {
		if fd, e := time.ParseInLocation("2006-01-02", rFirst, loc); e == nil {
			d := int(time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).Sub(fd).Hours()/24) + 1
			if d < 1 {
				d = 1
			}
			if d > 30 {
				d = 30
			}
			activeDays = d
		}
	}
	projMonthlyUSD := rTotalUSD / float64(activeDays) * 30.0

	s.writeJSON(w, http.StatusOK, map[string]any{
		"currency":          currency,
		"fx_rate":           rate,
		"month_stt":         mStt * rate,
		"month_cleanup":     mClean * rate,
		"month_command":     mCmd * rate,
		"month_total":       (mStt + mClean + mCmd) * rate,
		"period_total":      periodTotal * rate,
		"projected_monthly": projMonthlyUSD * rate,
		// The rates actually used, so the explanation popup can show them
		// instead of guessing at the defaults.
		"stt_per_hour_usd":         sttHr,
		"upload_per_hour_usd":      uploadHr,
		"cleanup_in_per_mtok_usd":  inRate,
		"cleanup_out_per_mtok_usd": outRate,
		"assist_in_per_mtok_usd":   aInRate,
		"assist_out_per_mtok_usd":  aOutRate,
	})
}

// subMonthly is the going rate for a typical paid dictation app, in whole units
// of the display currency — what the "money saved" achievements measure against.
const subMonthly = 15.0

// handleAchievements evaluates every milestone against the usage figures,
// records any newly earned (so their unlock date sticks), and returns the whole
// set for the UI to render — earned and locked, with progress toward the next.
func (s *Server) handleAchievements(w http.ResponseWriter, r *http.Request) {
	wpm := s.d.Config().TypingWPM()
	st, err := s.hist.AchievementInputs(wpm)
	if err != nil {
		s.writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	// What a subscription would have cost over the months you've had Vito, minus
	// what you actually spent in API fees. Both in the display currency.
	_, _, _, fx, currency := s.costRates()
	now := time.Now()
	sttHr, inRate, outRate, _, _ := s.costRates()
	uploadHr := stt.UploadRateUSD(s.d.Config().STT.Provider)
	aInRate, aOutRate := s.assistTokenRates(inRate, outRate)
	dur, up, in, out, cmdIn, cmdOut, first, _ := s.hist.CostTotals("", now.Format("2006-01-02"))
	spent := (float64(dur)/3_600_000.0*sttHr + float64(up)/3_600_000.0*uploadHr +
		float64(in)/1e6*inRate + float64(out)/1e6*outRate +
		float64(cmdIn)/1e6*aInRate + float64(cmdOut)/1e6*aOutRate) * fx
	months := 0.0
	if first != "" {
		if fd, e := time.ParseInLocation("2006-01-02", first, now.Location()); e == nil {
			months = now.Sub(fd).Hours() / 24 / 30.44
		}
	}
	if savings := months*subMonthly - spent; savings > 0 {
		st.SubscriptionSavings = int64(savings)
	}

	earned := achievements.Evaluate(st)

	// Persist everything currently earned. On a first run this records the lot in
	// one pass; the client's own "already seen" set keeps it from celebrating a
	// long history all at once.
	ids := make([]string, 0, len(earned))
	for _, d := range achievements.List {
		if earned[d.ID] {
			ids = append(ids, d.ID)
		}
	}
	if _, err := s.hist.RecordAchievements(ids); err != nil {
		s.log.Warn("recording achievements failed", "err", err)
	}
	unlocked, _ := s.hist.UnlockedAchievements()

	items := make([]map[string]any, 0, len(achievements.List))
	for _, d := range achievements.List {
		unlockedAt, isUnlocked := unlocked[d.ID]
		// Manual badges are earned purely by being in the stored set; the rest by
		// meeting their metric this run.
		gotIt := earned[d.ID]
		if d.Manual {
			gotIt = isUnlocked
		}
		item := map[string]any{
			"id": d.ID, "group": d.Group, "icon": d.Icon, "name": d.Name,
			"desc": d.Desc, "threshold": d.Threshold, "value": st.Value(d.Group),
			"earned": gotIt, "secret": d.Secret, "manual": d.Manual,
		}
		if isUnlocked {
			item["unlocked_at"] = unlockedAt.UnixMilli()
		}
		items = append(items, item)
	}

	s.writeJSON(w, http.StatusOK, map[string]any{
		"currency":     currency,
		"savings":      st.SubscriptionSavings,
		"months":       months,
		"sub_monthly":  subMonthly,
		"images":       achievementImages(),
		"animated":     achievementLotties(),
		"achievements": items,
	})
}

// handleAchievementSet ticks or un-ticks an honour-system badge (the donation
// ones). Only ids marked Manual can be set this way — usage-derived milestones
// are off-limits.
func (s *Server) handleAchievementSet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	manual := false
	for _, d := range achievements.List {
		if d.ID == id && d.Manual {
			manual = true
			break
		}
	}
	if !manual {
		s.writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "not a self-checkable achievement"})
		return
	}
	var body struct {
		Earned bool `json:"earned"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid JSON"})
		return
	}
	if err := s.hist.SetManualAchievement(id, body.Earned); err != nil {
		s.writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "earned": body.Earned})
}

// handleWelcomeDone records that the onboarding welcome card was dismissed. It
// persists to Vito's own config (config.UI.WelcomeDone), not browser
// localStorage, so an uninstall+reinstall that deletes all settings brings the
// welcome card back instead of the browser silently remembering "seen".
func (s *Server) handleWelcomeDone(w http.ResponseWriter, r *http.Request) {
	cfg := s.d.Config()
	if !cfg.UI.WelcomeDone {
		cfg.UI.WelcomeDone = true
		if err := cfg.Save(); err != nil {
			s.writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		s.d.UpdateConfig(&cfg)
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func nonZero(v, dflt float64) float64 {
	if v > 0 {
		return v
	}
	return dflt
}

// usdToEur returns a cached USD→EUR rate, refreshed at most twice a day from a
// free FX API (frankfurter). Falls back to a static rate if the fetch fails.
func (s *Server) usdToEur() float64 {
	s.fxMu.Lock()
	defer s.fxMu.Unlock()
	if s.fxRate > 0 && time.Since(s.fxAt) < 12*time.Hour {
		return s.fxRate
	}
	rate := 0.92 // fallback
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.frankfurter.app/latest?from=USD&to=EUR", nil)
	if resp, err := (&http.Client{Timeout: 4 * time.Second}).Do(req); err == nil {
		defer resp.Body.Close()
		var body struct {
			Rates map[string]float64 `json:"rates"`
		}
		if json.NewDecoder(io.LimitReader(resp.Body, 1<<14)).Decode(&body) == nil {
			if v, ok := body.Rates["EUR"]; ok && v > 0 {
				rate = v
			}
		}
	}
	s.fxRate, s.fxAt = rate, time.Now()
	return rate
}

// handleTestKey validates an API key by making one cheap authenticated request
// to the provider and reporting the outcome. The key is checked as given (before
// it is necessarily saved), so the UI can auto-test on paste.
func (s *Server) handleTestKey(w http.ResponseWriter, r *http.Request) {
	var body struct{ Provider, Key, BaseURL string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid JSON"})
		return
	}
	key := strings.TrimSpace(body.Key)
	// A local OpenAI-compatible endpoint may legitimately need no key, so for that
	// provider an empty key is fine as long as an endpoint is given.
	if key == "" && body.Provider != "openai" {
		s.writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "empty"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 9*time.Second)
	defer cancel()

	var req *http.Request
	switch body.Provider {
	case "stt", "assemblyai":
		base := "https://api.assemblyai.com"
		if s.d.Config().STT.EUEndpoint {
			base = "https://api.eu.assemblyai.com"
		}
		req, _ = http.NewRequestWithContext(ctx, http.MethodGet, base+"/v2/transcript?limit=1", nil)
		req.Header.Set("Authorization", key)
	case "cleanup", "anthropic":
		req, _ = http.NewRequestWithContext(ctx, http.MethodGet, "https://api.anthropic.com/v1/models?limit=1", nil)
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01")
	case "soniox":
		req, _ = http.NewRequestWithContext(ctx, http.MethodGet, "https://api.soniox.com/v1/models", nil)
		req.Header.Set("Authorization", "Bearer "+key)
	case "openai":
		base := strings.TrimRight(strings.TrimSpace(body.BaseURL), "/")
		if base == "" {
			s.writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "empty"})
			return
		}
		// Most OpenAI-compatible servers (incl. local ones) expose GET /models.
		req, _ = http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
	default:
		s.writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "unknown provider"})
		return
	}

	resp, err := (&http.Client{Timeout: 9 * time.Second}).Do(req)
	if err != nil {
		s.writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "network"})
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<14))
	switch {
	case resp.StatusCode == http.StatusOK:
		s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		s.writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "unauthorized"})
	default:
		s.writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "status", "status": resp.StatusCode})
	}
}

// handleGetAutostart reports the real OS autostart state (source of truth), not
// a config value, so the UI reflects what is actually configured.
func (s *Server) handleGetAutostart(w http.ResponseWriter, r *http.Request) {
	enabled, err := autostart.Enabled()
	resp := map[string]any{"supported": autostart.Supported(), "enabled": enabled}
	if err != nil {
		resp["error"] = err.Error()
	}
	s.writeJSON(w, http.StatusOK, resp)
}

// handlePutAutostart enables or disables launching Vito at login.
func (s *Server) handlePutAutostart(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid JSON: " + err.Error()})
		return
	}
	if err := autostart.Set(body.Enabled); err != nil {
		s.writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	// Autostart lives in the OS, not the config file, so nothing else would tell
	// the tray its checkbox is now stale.
	s.d.NotifySettingsChanged()
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": body.Enabled})
}

func privacyJSON(on bool, until time.Time) map[string]any {
	m := map[string]any{"enabled": on, "until_ms": int64(0)}
	if on && !until.IsZero() {
		m["until_ms"] = until.UnixMilli()
	}
	return m
}

func (s *Server) handleGetPrivacy(w http.ResponseWriter, r *http.Request) {
	on, until := s.d.PrivacyStatus()
	s.writeJSON(w, http.StatusOK, privacyJSON(on, until))
}

func (s *Server) handlePutPrivacy(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled bool `json:"enabled"`
		Minutes int  `json:"minutes"` // 0 = until off
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid JSON: " + err.Error()})
		return
	}
	s.d.SetPrivacy(body.Enabled, time.Duration(body.Minutes)*time.Minute)
	on, until := s.d.PrivacyStatus()
	s.writeJSON(w, http.StatusOK, privacyJSON(on, until))
}

func (s *Server) handleMicTest(w http.ResponseWriter, r *http.Request) {
	seconds := 4
	if v := r.URL.Query().Get("seconds"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 15 {
			seconds = n
		}
	}
	go func() {
		if err := s.d.TestMic(seconds); err != nil {
			s.log.Debug("mic test", "err", err)
		}
	}()
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleGetInputLevel(w http.ResponseWriter, r *http.Request) {
	resp := map[string]any{"supported": audio.LevelSupported()}
	if lvl, err := s.d.InputLevel(); err == nil {
		resp["level"] = lvl
	} else {
		resp["error"] = err.Error()
	}
	s.writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handlePutInputLevel(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Level float64 `json:"level"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid JSON: " + err.Error()})
		return
	}
	if err := s.d.SetInputLevel(body.Level); err != nil {
		s.writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "level": body.Level})
}

func (s *Server) handlePlaySound(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	volume := -1.0 // -1 = use configured volume
	if v := r.URL.Query().Get("volume"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			volume = f
		}
	}
	if err := s.d.PlaySound(name, volume); err != nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	days := 28 // default look-back; 0 = all time, -1 = yesterday
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= -1 {
			days = n
		}
	}
	wpm := s.d.Config().TypingWPM()
	var st history.Stats
	if s.demo() {
		st = demo.Stats(time.Now(), wpm, days)
	} else {
		var err error
		if st, err = s.hist.Stats(time.Now(), wpm, days); err != nil {
			s.writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
	}
	// Price each chart day here: the history layer tracks the billable
	// quantities, the rates and currency live in config.
	sttHr, inRate, outRate, fx, currency := s.costRates()
	aInRate, aOutRate := s.assistTokenRates(inRate, outRate)
	st.Currency = currency
	for i := range st.Week {
		d := &st.Week[i]
		d.Cost = (float64(d.DurationMS)/3_600_000.0*sttHr +
			float64(d.CleanupInTokens)/1e6*inRate +
			float64(d.CleanupOutTokens)/1e6*outRate +
			float64(d.CommandInTokens)/1e6*aInRate +
			float64(d.CommandOutTokens)/1e6*aOutRate) * fx
	}
	s.writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	offset := 0
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			offset = n
		}
	}
	q := r.URL.Query().Get("q")
	favOnly := r.URL.Query().Get("fav") == "1"
	if s.demo() {
		items := demo.History(time.Now(), q, limit+offset)
		total := len(items)
		if offset < len(items) {
			items = items[offset:]
		} else {
			items = items[:0]
		}
		s.writeJSON(w, http.StatusOK, map[string]any{"total": total, "items": items})
		return
	}
	total, err := s.hist.Count(q, favOnly)
	if err != nil {
		s.writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	entries, err := s.hist.List(q, favOnly, limit, offset)
	if err != nil {
		s.writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		ids = append(ids, e.ID)
	}
	kept := audio.HaveRecordings(ids)
	items := make([]historyItem, 0, len(entries))
	for _, e := range entries {
		items = append(items, historyItem{Entry: e, HasAudio: kept[e.ID]})
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"total": total, "items": items})
}

// historyItem is a history entry plus whatever the server knows about it that
// the store doesn't — currently only whether its audio was kept on disk.
type historyItem struct {
	history.Entry
	HasAudio bool `json:"has_audio,omitempty"`
}

// handlePlayback drives the daemon's player: toggling a recording, scrubbing
// through it, or stopping it. Progress comes back over the WebSocket.
func (s *Server) handlePlayback(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID       string  `json:"id"`
		Action   string  `json:"action"` // toggle | seek | stop
		Position float64 `json:"position"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid JSON"})
		return
	}
	switch req.Action {
	case "seek":
		s.d.SeekPlayback(req.Position)
	case "stop":
		s.d.StopPlayback()
	default:
		if err := s.d.PlayRecording(req.ID); err != nil {
			s.writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": err.Error()})
			return
		}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "playback": s.d.PlaybackState()})
}

// maxUploadBytes caps a transcription upload. Five hours of MP3 is well under
// this; it is here to stop a mis-picked file from filling the disk.
const maxUploadBytes = 512 << 20

// handleTranscribeFile takes an audio file, transcribes it with the configured
// provider and returns the text. Progress goes out over the WebSocket, so the
// request simply stays open until the job is done — which for a long recording
// is minutes.
func (s *Server) handleTranscribeFile(w http.ResponseWriter, r *http.Request) {
	if s.demo() {
		s.writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "demo mode: transcription is off"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	file, hdr, err := r.FormFile("file")
	if err != nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "no file: " + err.Error()})
		return
	}
	defer file.Close()

	// Spooled to disk rather than held in memory: the providers want a file, and
	// an hour of audio has no business sitting in RAM.
	tmp, err := os.CreateTemp("", "vito-upload-*"+filepath.Ext(hdr.Filename))
	if err != nil {
		s.writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, file); err != nil {
		tmp.Close()
		s.writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	tmp.Close()

	durationMS, _ := strconv.ParseInt(r.FormValue("duration_ms"), 10, 64)
	res, err := s.d.TranscribeUpload(r.Context(), tmp.Name(), hdr.Filename, durationMS)
	if err != nil {
		s.writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.writeJSON(w, http.StatusOK, res)
}

// handleUpdate reports the running version and, when the check is on, what the
// newest release is. ?force=1 skips the once-a-day cache.
func (s *Server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{
		"current":   Version,
		"can_apply": update.CanApply(),
		"checking":  s.d.Config().Update.CheckEnabled(),
		"repo":      "https://github.com/" + update.Repo,
		"repo_url":  "https://github.com/" + update.Repo + "/releases",
	}
	if !s.d.Config().Update.CheckEnabled() {
		s.writeJSON(w, http.StatusOK, out)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	rel, err := s.updates.Check(ctx, r.URL.Query().Get("force") == "1")
	if err != nil {
		out["error"] = err.Error()
		s.writeJSON(w, http.StatusOK, out) // not fatal: the card just says so
		return
	}
	out["release"] = rel
	s.writeJSON(w, http.StatusOK, out)
}

// handleUpdateApply downloads the release installer, verifies it against the
// published checksum and hands it to Windows. The reply goes out before the
// daemon steps aside, because the installer's first move is to stop us.
func (s *Server) handleUpdateApply(w http.ResponseWriter, r *http.Request) {
	if !update.CanApply() {
		s.writeJSON(w, http.StatusNotImplemented, map[string]any{"ok": false,
			"error": "installing an update from inside Vito is only supported on Windows"})
		return
	}
	rel, _ := s.updates.Cached()
	if rel == nil || !rel.Available {
		s.writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "no update available"})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	path, err := s.updates.Download(ctx, rel, func(frac float64) {
		s.hub.broadcast(daemon.Event{Type: "update", Upload: &daemon.UploadStatus{
			Phase: "upload", Frac: frac, Name: rel.Installer}})
	})
	if err != nil {
		s.writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if err := update.Apply(path); err != nil {
		s.writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.log.Info("update installer started; shutting down", "version", rel.Version)
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": rel.Version})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	go func() {
		// Give the reply time to reach the browser, then get out of the way: the
		// installer cannot replace an executable that is still running.
		time.Sleep(500 * time.Millisecond)
		s.d.Shutdown()
		os.Exit(0)
	}()
}

// --- backup & restore ---

const (
	backupKeep     = 4 // automatic backups to keep
	backupInterval = 7 * 24 * time.Hour
)

// handleBackupExport streams a complete backup for the user to save wherever
// they like. It carries settings (including API keys) and all stored history —
// the UI warns that it is sensitive.
func (s *Server) handleBackupExport(w http.ResponseWriter, r *http.Request) {
	data, err := s.hist.Snapshot()
	if err != nil {
		s.writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	f := backup.Make(s.d.Config(), Version, data, time.Now())
	raw, err := backup.Encrypt(f)
	if err != nil {
		s.writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="`+backup.DownloadName(time.Now())+`"`)
	_, _ = w.Write(raw)
}

// handleRestore applies a backup uploaded in the request body.
func (s *Server) handleRestore(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<20))
	if err != nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "could not read upload"})
		return
	}
	f, err := backup.Parse(raw)
	if err != nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.applyBackup(w, f)
}

// handleBackupList reports the automatic backups on disk plus whether the
// rolling backup is on and how old the newest one is.
func (s *Server) handleBackupList(w http.ResponseWriter, r *http.Request) {
	list, err := backup.List()
	if err != nil {
		s.writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if list == nil {
		list = []backup.Info{}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"auto":    s.d.Config().Backup.AutoEnabled(),
		"keep":    backupKeep,
		"backups": list,
	})
}

// handleBackupNow writes one automatic backup immediately (the manual "back up
// now" button) and returns the refreshed list.
func (s *Server) handleBackupNow(w http.ResponseWriter, r *http.Request) {
	if err := s.writeAutoBackup(); err != nil {
		s.writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.handleBackupList(w, r)
}

// handleBackupDownload streams one stored automatic backup for saving elsewhere.
func (s *Server) handleBackupDownload(w http.ResponseWriter, r *http.Request) {
	_, raw, err := backup.Read(r.PathValue("name"))
	if err != nil {
		s.writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="`+backup.DownloadName(time.Now())+`"`)
	_, _ = w.Write(raw)
}

// handleBackupRestore applies one of the stored automatic backups.
func (s *Server) handleBackupRestore(w http.ResponseWriter, r *http.Request) {
	f, _, err := backup.Read(r.PathValue("name"))
	if err != nil {
		s.writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.applyBackup(w, f)
}

// applyBackup swaps in a backup's data and settings. The server's own port and
// token are never moved (a backup taken elsewhere would otherwise lock this
// machine out); everything else — settings, history, stats, achievements — is
// replaced. Settings are validated before any data is touched.
func (s *Server) applyBackup(w http.ResponseWriter, f backup.File) {
	cur := s.d.Config()
	nc := *f.Config
	nc.Server = cur.Server // never move or re-key the running server
	if err := nc.Validate(); err != nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "backup settings are invalid: " + err.Error()})
		return
	}
	if err := s.hist.Restore(f.Data); err != nil {
		s.writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "restoring history failed: " + err.Error()})
		return
	}
	if err := s.d.SetConfig(nc); err != nil {
		s.writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "applying settings failed: " + err.Error()})
		return
	}
	// Same live side-effects as a normal settings change.
	if nc.HotkeyWindows != cur.HotkeyWindows || nc.HotkeyCancelWindows != cur.HotkeyCancelWindows {
		s.hk.Rebind(nc.HotkeyWindows, nc.HotkeyCancelWindows)
	}
	if cur.History.StoreAudio && !nc.History.StoreAudio {
		_ = audio.RemoveAllRecordings()
	}
	s.log.Info("backup restored", "history", len(f.Data.History), "achievements", len(f.Data.Achievements))
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// writeAutoBackup snapshots the current state into the rolling backup pool.
func (s *Server) writeAutoBackup() error {
	data, err := s.hist.Snapshot()
	if err != nil {
		return err
	}
	now := time.Now()
	f := backup.Make(s.d.Config(), Version, data, now)
	name, err := backup.Write(f, backupKeep, now)
	if err != nil {
		return err
	}
	s.log.Info("automatic backup written", "name", name)
	return nil
}

// autoBackupLoop keeps a small pool of recent backups without being asked: it
// writes one whenever the newest is older than backupInterval (or there is
// none). A short initial delay keeps it clear of startup.
func (s *Server) autoBackupLoop() {
	time.Sleep(20 * time.Second)
	check := func() {
		if !s.d.Config().Backup.AutoEnabled() {
			return
		}
		if age, ok := backup.NewestAge(time.Now()); ok && age < backupInterval {
			return
		}
		if err := s.writeAutoBackup(); err != nil {
			s.log.Warn("automatic backup failed", "err", err)
		}
	}
	check()
	tk := time.NewTicker(12 * time.Hour)
	defer tk.Stop()
	for range tk.C {
		check()
	}
}

// handleQuit shuts the daemon down. The installer calls this before replacing
// the binary — a running Vito holds its own .exe open and the port with it.
// The reply goes out first, then the process exits from a goroutine, so the
// caller gets an answer instead of a dropped connection.
func (s *Server) handleQuit(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	s.log.Info("shutting down on request")
	go func() {
		s.d.Shutdown() // restore ducked media, cancel any recording in flight
		time.Sleep(150 * time.Millisecond)
		os.Exit(0)
	}()
}

// handleEntryAudio serves the kept WAV of one dictation as a download.
func (s *Server) handleEntryAudio(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	path, ok := audio.RecordingPath(id)
	if s.demo() || !ok {
		s.writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "recording not found"})
		return
	}
	name := "vito-" + id + ".wav"
	if e, found, err := s.hist.Get(id); err == nil && found {
		name = "vito-" + e.Timestamp.Format("20060102-150405") + ".wav"
	}
	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	http.ServeFile(w, r, path)
}

func (s *Server) handleClearHistory(w http.ResponseWriter, r *http.Request) {
	if s.demo() {
		// The UI is showing fabricated entries; wiping the real database from a
		// demo would be destroying data the user can't even see right now.
		s.writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "demo mode: history is read-only"})
		return
	}
	deleted, err := s.hist.Clear()
	if err != nil {
		s.writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	// Clear keeps starred favorites, so remove only the recordings of the entries
	// that were actually deleted — the favorites' audio stays.
	for _, id := range deleted {
		audio.RemoveRecording(id)
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleFavorite stars or unstars a history entry. Starred entries survive the
// automatic pruning (row cap and age-based auto-delete) and their recording is
// kept regardless of the last-N cap.
func (s *Server) handleFavorite(w http.ResponseWriter, r *http.Request) {
	if s.demo() {
		s.writeJSON(w, http.StatusOK, map[string]any{"ok": true}) // nothing real to star
		return
	}
	var req struct {
		Favorite bool `json:"favorite"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		s.writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid JSON"})
		return
	}
	if err := s.hist.SetFavorite(r.PathValue("id"), req.Favorite); err != nil {
		s.writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleDeleteEntry(w http.ResponseWriter, r *http.Request) {
	if s.demo() {
		s.writeJSON(w, http.StatusOK, map[string]any{"ok": true}) // pretend; nothing to delete
		return
	}
	audio.RemoveRecording(r.PathValue("id"))
	if err := s.hist.Delete(r.PathValue("id")); err != nil {
		s.writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleReinject(w http.ResponseWriter, r *http.Request) {
	var (
		entry history.Entry
		found bool
		err   error
	)
	if s.demo() {
		entry, found = demo.Entry(time.Now(), r.PathValue("id"))
	} else {
		entry, found, err = s.hist.Get(r.PathValue("id"))
	}
	if err != nil || !found {
		s.writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "entry not found"})
		return
	}
	if err := s.d.InjectText(entry.Text()); err != nil {
		s.writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	if err := enc.Encode(v); err != nil && !strings.Contains(err.Error(), "broken pipe") {
		s.log.Debug("write response", "err", err)
	}
}
