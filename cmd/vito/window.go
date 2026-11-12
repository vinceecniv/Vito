package main

import (
	"fmt"
	"net/url"
)

// externalLinksJS sends every link to another site, and window.open of one,
// to vitoOpenExternal; it also drops the browser's context menu and tells the
// window when Vito's theme changes (vitoTheme, where the window binds it).
const externalLinksJS = `(function(){
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
  // The window's title bar follows Vito's theme, not only the system's.
  // This runs before the page exists (no <html> yet), so it watches the
  // document as a whole.
  let last = null;
  const theme = () => {
    const el = document.documentElement; if (!el || !window.vitoTheme) return;
    const dark = el.dataset.theme === "dark";
    if (dark !== last) { last = dark; window.vitoTheme(dark); }
  };
  new MutationObserver(theme).observe(document, { subtree: true, attributes: true, attributeFilter: ["data-theme"] });
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
