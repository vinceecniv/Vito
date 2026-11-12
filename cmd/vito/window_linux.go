package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"

	"vito/internal/config"
	"vito/internal/gtkwin"
	"vito/internal/notify"
	"vito/internal/selfexe"
	"vito/internal/webapp"
	"vito/internal/xdgportal"
)

// vito window on Linux: Vito's page in a window of its own, GTK 4 with
// WebKitGTK 6.0 (internal/gtkwin), as WebView2 does it on Windows. The
// libraries are opened when the window starts, so the binary needs none of
// them: where they are missing (a bare system, the Flatpak's runtime) Vito
// opens the installed web app or a browser tab, as before.
//
// As on Windows it is a process of its own, one window at a time: a second
// `vito window` hands its address to the first over a Unix socket. It closes
// when the daemon goes away and is opened again by the next one (markReopen).
//
// The window has no title bar of GTK's. On a desktop that shows title bars
// (GNOME, KDE, Cinnamon, …) the page draws its own, with the buttons the
// desktop's layout names; on a tiling one (niri, Hyprland, sway, …), where
// windows go without, it has none at all.

func openWindow(args []string) error {
	if len(args) > 0 && args[0] == "--probe" {
		// windowSupported asks this in a child: the daemon itself never loads GTK.
		if err := gtkwin.Load(); err != nil {
			return err
		}
		return gtkwin.Init(windowAppID())
	}
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
	token := takeActivationToken()

	// One window: hand the address to the one already open.
	if windowRequest(port, winReq{T: "nav", URL: target, Token: token}) == nil {
		return nil
	}
	lock, err := lockWindow(port)
	if err != nil {
		// Another `vito window` is starting right now; let it come up.
		for i := 0; i < 50; i++ {
			time.Sleep(100 * time.Millisecond)
			if windowRequest(port, winReq{T: "nav", URL: target, Token: token}) == nil {
				return nil
			}
		}
		return nil
	}
	defer lock.Close()

	if !daemonRunning(port) {
		if err := startDaemon(); err != nil {
			return err
		}
		for i := 0; i < 150 && !daemonRunning(port); i++ {
			time.Sleep(100 * time.Millisecond)
		}
		if !daemonRunning(port) {
			return fmt.Errorf("vito daemon is not answering on port %d", port)
		}
	}
	if err := gtkwin.Load(); err != nil {
		fmt.Fprintln(os.Stderr, "vito window:", err)
		return webapp.Open(target)
	}
	if err := gtkwin.Init(windowAppID()); err != nil {
		return err
	}
	runGTKWindow(cfg, target, token)
	return nil
}

// windowAppID names the window to the desktop: the desktop entry Vito writes
// (vito.desktop), or the Flatpak's.
func windowAppID() string {
	if id := os.Getenv("FLATPAK_ID"); id != "" {
		return id
	}
	return "vito"
}

// takeActivationToken is the XDG activation token this launch came with (a
// click in a launcher or dock), which lets the window take the focus. It
// belongs to this launch, so children must not see it.
func takeActivationToken() string {
	t := os.Getenv("XDG_ACTIVATION_TOKEN")
	if t == "" {
		t = os.Getenv("DESKTOP_STARTUP_ID")
	}
	os.Unsetenv("XDG_ACTIVATION_TOKEN")
	os.Unsetenv("DESKTOP_STARTUP_ID")
	return t
}

// tilingDesktop: windows go without title bars there, so Vito's has none
// either. VITO_WINDOW_FRAME=own or bare overrides it (for trying).
func tilingDesktop() bool {
	switch os.Getenv("VITO_WINDOW_FRAME") {
	case "own":
		return false
	case "bare":
		return true
	}
	for _, v := range []string{"NIRI_SOCKET", "HYPRLAND_INSTANCE_SIGNATURE", "SWAYSOCK", "I3SOCK"} {
		if os.Getenv(v) != "" {
			return true
		}
	}
	desk := strings.ToLower(os.Getenv("XDG_CURRENT_DESKTOP"))
	for _, d := range []string{"niri", "hyprland", "sway", "river", "i3", "qtile", "dwl", "bspwm", "awesome", "xmonad"} {
		if strings.Contains(desk, d) {
			return true
		}
	}
	return false
}

// gtkPlace is the window's size between runs. Wayland gives a window no
// say in where it goes, so only the size and whether it was maximised.
type gtkPlace struct {
	Width, Height int
	Maximized     bool
}

func placeFile() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "vito", "window-gtk.json")
}

func runGTKWindow(cfg *config.Config, target, token string) {
	port := cfg.Server.Port
	place := gtkPlace{Width: 1280, Height: 880}
	if b, err := os.ReadFile(placeFile()); err == nil {
		var p gtkPlace
		if json.Unmarshal(b, &p) == nil && p.Width >= 420 && p.Height >= 480 {
			place = p
		}
	}
	own := !tilingDesktop()
	debug := os.Getenv("VITO_DEBUG") != ""
	dark := wantDarkWindow(cfg.UI.Theme)
	bg := "#FAF6EF"
	if dark {
		bg = "#17141F"
	}

	scripts := []string{gtkBridgeJS}
	if !gtkwin.SharpCursors() {
		// GTK before 4.16 shows WebKit's pointer image at its drawn size:
		// the page makes it smaller (cursorCSS), a little soft but the right size.
		scripts = append(scripts, "window.__VITO_SHRINK_CURSORS__ = true;")
	}
	if own {
		scripts = append(scripts, gtkFrameJS(gtkwin.DecorationLayout()))
	}
	scripts = append(scripts, externalLinksJS)
	if debug {
		scripts = append(scripts, gtkDebugJS)
	}

	cache, _ := os.UserCacheDir()
	dataDir := filepath.Join(cache, "vito", "webkit")

	ln := listenWindow(port)
	if ln != nil {
		defer ln.Close()
	}
	var once sync.Once
	go watchGTKDaemon(port)

	gtkwin.Run(gtkwin.Options{
		URL:        target,
		DataDir:    filepath.Join(dataDir, "data"),
		CacheDir:   filepath.Join(dataDir, "cache"),
		Scripts:    scripts,
		Width:      place.Width,
		Height:     place.Height,
		Maximized:  place.Maximized,
		OwnFrame:   own,
		Debug:      debug,
		Background: bg,
	}, gtkwin.Handlers{
		Message: func(raw string) {
			once.Do(func() {
				gtkwin.SetDark(dark)
				if ln != nil {
					go serveWindow(ln)
				}
				if token != "" {
					gtkwin.Present(token)
				}
			})
			onGTKMessage(raw)
		},
		State: func(max, active bool) {
			gtkwin.Eval(fmt.Sprintf("window.vitoFrameState&&vitoFrameState({max:%t,active:%t})", max, active))
		},
		Closing: func(w, h int, max bool) {
			if w < 420 || h < 480 {
				return
			}
			b, _ := json.Marshal(gtkPlace{w, h, max})
			if f := placeFile(); f != "" {
				_ = os.MkdirAll(filepath.Dir(f), 0o755)
				_ = os.WriteFile(f, b, 0o644)
			}
		},
		Navigate: func(raw string, link bool) bool {
			u, err := url.Parse(raw)
			if err != nil {
				return true
			}
			switch u.Scheme {
			case "about", "blob", "data":
				return false
			case "vito": // the page's "start Vito" while the daemon is down
				_ = startDaemon()
				return true
			case "http":
				if u.Host == fmt.Sprintf("127.0.0.1:%d", port) {
					return false
				}
			}
			if !link {
				return false
			}
			if u.Scheme == "http" || u.Scheme == "https" || u.Scheme == "mailto" {
				_ = exec.Command("xdg-open", u.String()).Start()
			}
			return true
		},
		Downloaded: func(path string) {
			if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(path, home+"/") {
				path = "~" + path[len(home):]
			}
			notify.Send("Vito", path)
		},
	})
}

// onGTKMessage takes what the page posts (gtkBridgeJS, gtkFrameJS).
func onGTKMessage(raw string) {
	var m struct {
		T    string `json:"t"`
		URL  string `json:"url"`
		Dark bool   `json:"dark"`
		Acc  string `json:"accent"`
		Rad  string `json:"radius"`
		Msg  string `json:"msg"`
		A    string `json:"a"`
		Edge string `json:"edge"`
	}
	if json.Unmarshal([]byte(raw), &m) != nil {
		return
	}
	switch m.T {
	case "open":
		u, err := url.Parse(m.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "mailto") {
			return
		}
		_ = exec.Command("xdg-open", u.String()).Start()
	case "theme":
		gtkwin.SetDark(m.Dark)
		gtkwin.SetRadius(map[string]int{"minimal": 3, "none": 0}[m.Rad] + map[bool]int{true: 12}[m.Rad == ""])
		gtkwin.SetIconName(launcherIcon(m.Acc, m.Rad, false)) // X11 and KDE show the window's own
	case "win":
		if m.A == "hello" {
			max, active := gtkwin.State()
			gtkwin.Eval(fmt.Sprintf("window.vitoFrameState&&vitoFrameState({max:%t,active:%t})", max, active))
			return
		}
		gtkwin.Frame(m.A, m.Edge)
	case "reload":
		gtkwin.Eval("location.reload()")
	case "log":
		fmt.Fprintln(os.Stderr, time.Now().Format("15:04:05"), m.Msg)
	}
}

// gtkBridgeJS gives the shared page script its calls, through WebKit's
// message handler; F5 and Ctrl+R reload, as in a browser.
const gtkBridgeJS = `(function(){
  const post = (o) => { try { window.webkit.messageHandlers.vito.postMessage(JSON.stringify(o)); } catch (e) {} };
  window.vitoOpenExternal = (u) => post({ t: "open", url: String(u) });
  window.vitoTheme = (dark, accent, radius) => post({ t: "theme", dark: !!dark, accent: accent || "", radius: radius || "" });
  document.addEventListener("keydown", (e) => {
    if (e.key === "F5" || ((e.ctrlKey || e.metaKey) && !e.shiftKey && !e.altKey && (e.key === "r" || e.key === "R"))) { e.preventDefault(); post({ t: "reload" }); }
  }, true);
  post({ t: "ready" });
})();`

// gtkFrameJS tells the page it draws the title bar, with the buttons GTK's
// decoration layout names ("appmenu:close": only close, on the right).
func gtkFrameJS(layout string) string {
	l, _ := json.Marshal(layout)
	return `(function(){
  window.__VITO_FRAME__ = true;
  window.__VITO_FRAME_GTK__ = ` + string(l) + `;
  window.vitoWin = (a, edge) => { try { window.webkit.messageHandlers.vito.postMessage(JSON.stringify({ t: "win", a: String(a), edge: edge || "" })); } catch (e) {} };
})();`
}

// gtkDebugJS (VITO_DEBUG) sends the page's errors to the window's stderr.
const gtkDebugJS = `(function(){
  const post = (m) => { try { window.webkit.messageHandlers.vito.postMessage(JSON.stringify({ t: "log", msg: String(m) })); } catch (e) {} };
  window.addEventListener("error", (e) => post((e.message || "error") + " @ " + (e.filename || "") + ":" + (e.lineno || 0)));
  window.addEventListener("unhandledrejection", (e) => post("unhandled: " + (e.reason && (e.reason.stack || e.reason.message) || e.reason)));
})();`

// wantDarkWindow: Vito's theme, else the desktop's colour scheme (the
// Settings portal, as the pill reads it).
func wantDarkWindow(theme string) bool {
	switch theme {
	case "dark":
		return true
	case "light":
		return false
	}
	conn, err := xdgportal.Bus()
	if err != nil {
		return false
	}
	var v dbus.Variant
	err = conn.Object(xdgportal.Dest, dbus.ObjectPath(xdgportal.Path)).
		Call("org.freedesktop.portal.Settings.ReadOne", 0, "org.freedesktop.appearance", "color-scheme").Store(&v)
	if err != nil {
		return false
	}
	if inner, ok := v.Value().(dbus.Variant); ok {
		v = inner
	}
	n, ok := v.Value().(uint32)
	return ok && n == 1
}

// watchGTKDaemon closes the window once the daemon is gone (a quit, an
// update, a restart): a window on a stopped daemon only shows errors. The
// daemon that starts next opens it again.
func watchGTKDaemon(port int) {
	misses := 0
	for {
		time.Sleep(500 * time.Millisecond)
		if daemonRunning(port) {
			misses = 0
			continue
		}
		if misses++; misses >= 2 {
			markReopen()
			gtkwin.Do(gtkwin.Close)
			return
		}
	}
}

// ---- one window: the socket a second `vito window` talks to ----

type winReq struct {
	T     string `json:"t"` // nav, close
	URL   string `json:"url,omitempty"`
	Token string `json:"token,omitempty"`
}

func windowSocket(port int) string {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = os.TempDir()
	}
	return filepath.Join(dir, fmt.Sprintf("vito-window-%d.sock", port))
}

// windowRequest sends r to the open window; an error means there is none.
func windowRequest(port int, r winReq) error {
	c, err := net.DialTimeout("unix", windowSocket(port), time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	b, _ := json.Marshal(r)
	if _, err := c.Write(append(b, '\n')); err != nil {
		return err
	}
	ok, err := bufio.NewReader(c).ReadString('\n')
	if err != nil || strings.TrimSpace(ok) != "ok" {
		return errors.New("the window did not answer")
	}
	return nil
}

// lockWindow takes the lock one starting window holds, so two started at
// once make one window.
func lockWindow(port int) (*os.File, error) {
	f, err := os.OpenFile(windowSocket(port)+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func listenWindow(port int) net.Listener {
	p := windowSocket(port)
	_ = os.Remove(p) // a socket left by a window that died; the lock is ours
	ln, err := net.Listen("unix", p)
	if err != nil {
		return nil
	}
	_ = os.Chmod(p, 0o600)
	return ln
}

func serveWindow(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			_ = c.SetDeadline(time.Now().Add(2 * time.Second))
			line, err := bufio.NewReader(c).ReadString('\n')
			if err != nil {
				return
			}
			var r winReq
			if json.Unmarshal([]byte(line), &r) != nil {
				return
			}
			switch r.T {
			case "nav":
				gtkwin.Do(func() {
					if r.URL != "" {
						u, _ := json.Marshal(r.URL)
						gtkwin.Eval("location.href=" + string(u))
					}
					gtkwin.Present(r.Token)
				})
				go focusOwnWindow()
			case "close":
				gtkwin.Do(gtkwin.Close)
			}
			_, _ = c.Write([]byte("ok\n"))
		}(c)
	}
}

// focusOwnWindow brings the window forward where the compositor lets a
// process ask for it by itself. Without an activation token Wayland leaves
// the focus where it is; niri and Hyprland take it over their own IPC.
func focusOwnWindow() {
	time.Sleep(50 * time.Millisecond)
	pid := os.Getpid()
	switch {
	case os.Getenv("NIRI_SOCKET") != "":
		out, err := exec.Command("niri", "msg", "--json", "windows").Output()
		if err != nil {
			return
		}
		var ws []struct {
			ID  uint64 `json:"id"`
			PID int    `json:"pid"`
		}
		if json.Unmarshal(out, &ws) != nil {
			return
		}
		for _, w := range ws {
			if w.PID == pid {
				_ = exec.Command("niri", "msg", "action", "focus-window", "--id", strconv.FormatUint(w.ID, 10)).Run()
				return
			}
		}
	case os.Getenv("HYPRLAND_INSTANCE_SIGNATURE") != "":
		_ = exec.Command("hyprctl", "dispatch", "focuswindow", "pid:"+strconv.Itoa(pid)).Run()
	}
}

// ---- the daemon's side ----

// launchWindow opens `vito window <url>`, or hands the address to the window
// already open; ErrNoNative where this system has no window to give.
func launchWindow(u string) error {
	port := portOf(u)
	if port != 0 && windowRequest(port, winReq{T: "nav", URL: u}) == nil {
		return nil
	}
	if !windowSupported() {
		return webapp.ErrNoNative
	}
	exe, err := selfexe.Path()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "window", u)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd.Start()
}

func portOf(u string) int {
	p, err := url.Parse(u)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(p.Port())
	return n
}

var probe struct {
	once sync.Once
	ok   bool
}

// windowSupported: GTK 4 and WebKitGTK 6.0 are installed and start here.
// Asked once, of a child (`vito window --probe`), so the daemon never loads
// them itself.
func windowSupported() bool {
	probe.once.Do(func() {
		exe, err := selfexe.Path()
		if err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		probe.ok = exec.CommandContext(ctx, exe, "window", "--probe").Run() == nil
	})
	return probe.ok
}

// startDaemon starts `vito serve` from this same binary, in a session of its
// own so it outlives the window.
func startDaemon() error {
	exe, err := selfexe.Path()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "serve")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd.Start()
}

// closeWindow closes Vito's window, if one is open, before the daemon quits on
// the user's word, so the next daemon does not open it again (markReopen).
func closeWindow() {
	if cfg, err := config.Load(); err == nil {
		_ = windowRequest(cfg.Server.Port, winReq{T: "close"})
	}
}
