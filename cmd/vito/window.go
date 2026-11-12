package main

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"vito/internal/config"
	"vito/internal/webapp"
)

// openNative decides how the daemon opens its page: Vito's own window unless
// ui.window is switched off; ErrNoNative then (and where there is no window to
// be had), so webapp.Open carries on with the web app or a browser tab. The
// web app still works for whoever opens it, but is no longer the way in.
func openNative(pref *bool, u string) error {
	if !windowSupported() || (pref != nil && !*pref) {
		return webapp.ErrNoNative
	}
	return launchWindow(u)
}

// openPage is `vito open [url]`: what the Start menu shortcut and the
// installer use. It starts the daemon when needed and opens the page the way
// the daemon would.
func openPage(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	port := cfg.Server.Port
	target := fmt.Sprintf("http://127.0.0.1:%d/", port)
	if len(args) > 0 {
		if target, err = ownPage(args[0], port); err != nil {
			return err
		}
	}
	// Right after an install the daemon is still starting: give it a moment
	// before starting one ourselves.
	for i := 0; i < 30 && !daemonRunning(port); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	if !daemonRunning(port) {
		if err := startDaemon(); err != nil {
			return err
		}
		for i := 0; i < 150 && !daemonRunning(port); i++ {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if err := openNative(cfg.UI.Window, target); err == nil {
		return nil
	}
	return webapp.Open(target)
}

// externalLinksJS sends every link to another site, and window.open of one,
// to vitoOpenExternal; it also drops the browser's context menu and tells the
// window when Vito's theme changes (vitoTheme, where the window binds it).
const externalLinksJS = `(function(){
  // The page asks this to tell its own window from a browser tab (no "install
  // as app" there).
  window.__VITO_WINDOW__ = true;
  const ext = (u) => { try { const x = new URL(u, location.href);
    return (x.protocol === "http:" || x.protocol === "https:" || x.protocol === "mailto:") && x.origin !== location.origin; } catch (e) { return false; } };
  document.addEventListener("click", (e) => {
    const a = e.target && e.target.closest && e.target.closest("a[href]");
    if (a && ext(a.href)) { e.preventDefault(); e.stopPropagation(); window.vitoOpenExternal(a.href); }
  }, true);
  // The browser's own menu (Back, Refresh, Save as, Print) has no place in an
  // app; in a field or over a selection it stays, for cut, copy and paste.
  document.addEventListener("contextmenu", (e) => {
    const t = e.target, sel = String(window.getSelection && window.getSelection() || "");
    if (t && t.closest && t.closest("input, textarea, [contenteditable=''], [contenteditable='true']")) return;
    if (sel.trim()) return;
    e.preventDefault();
  });
  // The window's title bar follows Vito's theme, not only the system's, and
  // its icon Vito's colour theme.
  // This runs before the page exists (no <html> yet), so it watches the
  // document as a whole.
  let last = null;
  const theme = () => {
    const el = document.documentElement; if (!el || !window.vitoTheme) return;
    const dark = el.dataset.theme === "dark", accent = el.dataset.accent || "";
    const now = dark + "/" + accent;
    if (now !== last) { last = now; window.vitoTheme(dark, accent); }
  };
  new MutationObserver(theme).observe(document, { subtree: true, attributes: true, attributeFilter: ["data-theme", "data-accent"] });
  document.addEventListener("DOMContentLoaded", theme);
  const open = window.open;
  window.open = function(u, ...rest) {
    if (u && ext(u)) { window.vitoOpenExternal(new URL(u, location.href).href); return null; }
    return open.call(window, u, ...rest);
  };
})();`

// ownPage accepts only Vito's own page on this port, so a window can never be
// pointed elsewhere.
func ownPage(raw string, port int) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.Host != fmt.Sprintf("127.0.0.1:%d", port) {
		return "", fmt.Errorf("vito window opens only http://127.0.0.1:%d/", port)
	}
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String(), nil
}

// reopenFile marks that Vito's window was open when its daemon went away
// without closing it (a restart, an update). The daemon that starts next
// opens the window again if the mark is fresh.
func reopenFile() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "vito", "window-reopen")
}

func markReopen() {
	if f := reopenFile(); f != "" {
		_ = os.WriteFile(f, []byte(time.Now().Format(time.RFC3339)), 0o600)
	}
}

// reopenWindow opens the window again after a restart or update that closed
// it, once the server answers. A mark older than a few minutes is ignored:
// the user may have started Vito again much later, on purpose without it.
func reopenWindow(port int, on func() bool) {
	f := reopenFile()
	if f == "" {
		return
	}
	st, err := os.Stat(f)
	if err != nil {
		return
	}
	_ = os.Remove(f)
	if time.Since(st.ModTime()) > 5*time.Minute || !on() {
		return
	}
	for i := 0; i < 100 && !daemonRunning(port); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	_ = launchWindow(fmt.Sprintf("http://127.0.0.1:%d/", port))
}
