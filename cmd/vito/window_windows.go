//go:build windows

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/jchv/go-webview2/pkg/edge"
	"github.com/jchv/go-webview2/webviewloader"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"vito/internal/config"
	"vito/internal/notify"
	"vito/internal/selfexe"
	"vito/internal/webapp"
)

// vito window opens Vito's page in a window of its own: WebView2, the Edge
// engine that ships with Windows 11, so the page runs on the same Chromium as
// the PWA without a browser, an install step or the PWA's cache.
//
// The window is Vito's own Win32 window with WebView2 embedded through
// go-webview2's edge package, which reaches the controller and its settings:
// no browser menu, accelerator keys or status bar, a dark caption that follows
// Vito's theme, and the window's place kept between runs.
//
// It is its own process on purpose. The daemon keeps the tray and the pills on
// their own threads; closing the window frees the engine's memory entirely, and
// a hung page never takes dictation down with it. One window at a time: a
// second `vito window` hands its address to the first (WM_COPYDATA). It closes
// itself when the daemon goes away, so an update can replace vito.exe.
//
// Without the WebView2 runtime (a bare Windows 10) everything falls back to the
// installed web app or a browser tab, as before.

const (
	windowClass = "VitoWindow"
	windowTitle = "Vito"

	wmDestroy       = 0x0002
	wmMove          = 0x0003
	wmSize          = 0x0005
	wmActivate      = 0x0006
	wmClose         = 0x0010
	wmGetMinMaxInfo = 0x0024
	wmSetIcon       = 0x0080
	wmNCActivate    = 0x0086
	wmCopyData      = 0x004A
	wmDpiChanged    = 0x02E0
	wmApp           = 0x8000
	swShow          = 5
	swShowMax       = 3
	swRestore       = 9
	copyDataNav     = 0x56495430 // "VIT0": marks our WM_COPYDATA

	vkR  = 0x52
	vkF5 = 0x74
)

var (
	user32                    = windows.NewLazySystemDLL("user32.dll")
	procRegisterClassExW      = user32.NewProc("RegisterClassExW")
	procCreateWindowExW       = user32.NewProc("CreateWindowExW")
	procDefWindowProcW        = user32.NewProc("DefWindowProcW")
	procDestroyWindow         = user32.NewProc("DestroyWindow")
	procShowWindow            = user32.NewProc("ShowWindow")
	procUpdateWindow          = user32.NewProc("UpdateWindow")
	procGetMessageW           = user32.NewProc("GetMessageW")
	procTranslateMessage      = user32.NewProc("TranslateMessage")
	procDispatchMessageW      = user32.NewProc("DispatchMessageW")
	procPostMessageW          = user32.NewProc("PostMessageW")
	procPostQuitMessage       = user32.NewProc("PostQuitMessage")
	procSendMessageW          = user32.NewProc("SendMessageW")
	procFindWindowW           = user32.NewProc("FindWindowW")
	procSetForegroundWindow   = user32.NewProc("SetForegroundWindow")
	procIsIconic              = user32.NewProc("IsIconic")
	procSetWindowPos          = user32.NewProc("SetWindowPos")
	procGetForegroundWindow   = user32.NewProc("GetForegroundWindow")
	procAllowSetForegroundWnd = user32.NewProc("AllowSetForegroundWindow")
	procGetWindowPlacement    = user32.NewProc("GetWindowPlacement")
	procSetWindowPlacement    = user32.NewProc("SetWindowPlacement")
	procMonitorFromRect       = user32.NewProc("MonitorFromRect")
	procGetDpiForWindow       = user32.NewProc("GetDpiForWindow")
	procGetDpiForSystem       = user32.NewProc("GetDpiForSystem")
	procGetKeyState           = user32.NewProc("GetKeyState")
	procSetProcessDpiAwareCtx = user32.NewProc("SetProcessDpiAwarenessContext")
	procSystemParametersInfo  = user32.NewProc("SystemParametersInfoW")
	shell32                   = windows.NewLazySystemDLL("shell32.dll")
	procExtractIconExW        = shell32.NewProc("ExtractIconExW")
	procSetAppIDForProcess    = shell32.NewProc("SetCurrentProcessExplicitAppUserModelID")
	procSHGetPropertyStoreWnd = shell32.NewProc("SHGetPropertyStoreForWindow")
	dwmapi                    = windows.NewLazySystemDLL("dwmapi.dll")
	procDwmSetWindowAttribute = dwmapi.NewProc("DwmSetWindowAttribute")
)

type copyDataStruct struct {
	dwData uintptr
	cbData uint32
	lpData *byte
}

type wndClassEx struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     windows.Handle
	hIcon         windows.Handle
	hCursor       windows.Handle
	hbrBackground windows.Handle
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       windows.Handle
}

type point struct{ x, y int32 }
type rect struct{ left, top, right, bottom int32 }

type minMaxInfo struct {
	reserved, maxSize, maxPosition, minTrackSize, maxTrackSize point
}

type windowPlacement struct {
	length        uint32
	flags         uint32
	showCmd       uint32
	ptMinPosition point
	ptMaxPosition point
	rcNormal      rect
}

type msg struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      point
	private uint32
}

// vwin is the one window this process shows.
type vwin struct {
	hwnd   uintptr
	web    *edge.Chromium
	ready  bool
	mu     sync.Mutex
	queue  []func()
	placeF string
}

var theWindow *vwin

// webView2Available says whether the WebView2 runtime is installed.
func webView2Available() bool {
	v, err := webviewloader.GetInstalledVersion()
	return err == nil && v != ""
}

func openWindow(args []string) error {
	hideOwnConsole()
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

	// One window: hand the address to the one already open.
	if h := findVitoWindow(); h != 0 {
		if len(args) > 0 {
			sendNavigate(h, target)
		}
		focusWindow(h)
		return nil
	}
	name, _ := windows.UTF16PtrFromString(`Local\VitoWindow`)
	mutex, err := windows.CreateMutex(nil, false, name)
	if err == windows.ERROR_ALREADY_EXISTS {
		// Another `vito window` is starting right now; let it come up.
		for i := 0; i < 50; i++ {
			if h := findVitoWindow(); h != 0 {
				focusWindow(h)
				return nil
			}
			time.Sleep(100 * time.Millisecond)
		}
		return nil
	}
	if mutex != 0 {
		defer windows.CloseHandle(mutex)
	}

	if !daemonRunning(port) {
		if err := startDaemon(); err != nil {
			return err
		}
		if !waitForDaemon(port, 15*time.Second) {
			return fmt.Errorf("vito daemon is not answering on port %d", port)
		}
	}

	if !webView2Available() {
		return webapp.Open(target) // the web app, or a browser tab
	}
	if err := runWindow(cfg, target); err != nil {
		if errors.Is(err, errNoEngine) {
			return webapp.Open(target)
		}
		return err
	}
	return nil
}

var errNoEngine = errors.New("WebView2 could not start")

func runWindow(cfg *config.Config, target string) error {
	// WebView2 wants one thread for the window, its COM objects and the loop.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	procSetProcessDpiAwareCtx.Call(^uintptr(3)) // PER_MONITOR_AWARE_V2 (-4)
	if id, err := windows.UTF16PtrFromString(notify.AppUserModelID); err == nil {
		procSetAppIDForProcess.Call(uintptr(unsafe.Pointer(id)))
	}

	w := &vwin{}
	theWindow = w
	if dir, err := os.UserConfigDir(); err == nil {
		w.placeF = filepath.Join(dir, "vito", "window.json")
	}

	var hinst windows.Handle
	_ = windows.GetModuleHandleEx(0, nil, &hinst)
	cls, _ := windows.UTF16PtrFromString(windowClass)
	wc := wndClassEx{
		cbSize:        uint32(unsafe.Sizeof(wndClassEx{})),
		lpfnWndProc:   windows.NewCallback(windowProc),
		hInstance:     hinst,
		lpszClassName: cls,
	}
	procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))

	// The default size, scaled to the screen; a kept placement replaces it.
	dpi, _, _ := procGetDpiForSystem.Call()
	if dpi == 0 {
		dpi = 96
	}
	width, height := 1280*int(dpi)/96, 880*int(dpi)/96
	const cwUseDefault = 0x80000000
	x, y := uintptr(cwUseDefault), uintptr(cwUseDefault)
	kept, haveKept := w.loadPlacement()
	if haveKept {
		// Made where it was, on that screen and at its scale: placed after
		// creation it would first be made on the main screen, and the change
		// of scale on the way over resized it.
		r := kept.rcNormal
		var wa rect                                                               // rcNormal is in workspace coordinates: the main screen's work area
		procSystemParametersInfo.Call(0x0030, 0, uintptr(unsafe.Pointer(&wa)), 0) // SPI_GETWORKAREA
		x, y = uintptr(int(r.left+wa.left)), uintptr(int(r.top+wa.top))
		width, height = int(r.right-r.left), int(r.bottom-r.top)
	}
	title, _ := windows.UTF16PtrFromString(windowTitle)
	w.hwnd, _, _ = procCreateWindowExW.Call(0,
		uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(title)),
		0x00CF0000, // WS_OVERLAPPEDWINDOW
		x, y, uintptr(width), uintptr(height),
		0, 0, uintptr(hinst), 0)
	if w.hwnd == 0 {
		return errNoEngine
	}
	setWindowIcon(w.hwnd)
	setRelaunch(w.hwnd)
	setDarkTitleBar(w.hwnd, wantDarkTitle(cfg.UI.Theme))
	show := uintptr(swShow)
	if p := kept; haveKept {
		if p.showCmd == swShowMax {
			show = swShowMax
		}
		p.showCmd = 0 // placed hidden; shown below
		procSetWindowPlacement.Call(w.hwnd, uintptr(unsafe.Pointer(&p)))
	}
	procShowWindow.Call(w.hwnd, show)
	procUpdateWindow.Call(w.hwnd)

	web := edge.NewChromium()
	web.DataPath = webView2DataPath()
	web.MessageCallback = w.onMessage
	web.AcceleratorKeyCallback = w.onKey
	debug := os.Getenv("VITO_DEBUG") != ""
	if !web.Embed(w.hwnd) {
		procDestroyWindow.Call(w.hwnd)
		return errNoEngine
	}
	w.web = web
	if s, err := web.GetSettings(); err == nil {
		// The browser's context menu stays on: the page script below keeps
		// it for fields and selections (cut, copy, paste) and drops it elsewhere.
		_ = s.PutAreDevToolsEnabled(debug)
		_ = s.PutIsStatusBarEnabled(false)
		_ = s.PutAreBrowserAcceleratorKeysEnabled(debug)
		_ = s.PutIsSwipeNavigationEnabled(false)
	}
	web.Resize()
	if debug {
		web.Init(windowDebugJS)
	}
	web.Init(windowBridgeJS)
	web.Init(externalLinksJS)
	web.Navigate(target)
	web.Focus()
	w.mu.Lock()
	w.ready = true
	w.mu.Unlock()

	go w.watchDaemon(cfg.Server.Port)

	var m msg
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		if m.message == wmApp && m.hwnd == w.hwnd {
			w.runQueue()
			continue
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
	return nil
}

// windowBridgeJS gives the shared page script its two calls, through
// WebView2's message channel.
const windowBridgeJS = `(function(){
  const post = (o) => { try { window.chrome.webview.postMessage(JSON.stringify(o)); } catch (e) {} };
  window.vitoOpenExternal = (u) => post({ t: "open", url: String(u) });
  window.vitoTheme = (dark) => post({ t: "theme", dark: !!dark });
})();`

// windowDebugJS (VITO_DEBUG) sends the page's errors to the window's log,
// %LOCALAPPDATA%\vito\window.log: WebView2 has no console to read otherwise.
const windowDebugJS = `(function(){
  const post = (m) => { try { window.chrome.webview.postMessage(JSON.stringify({ t: "log", msg: String(m) })); } catch (e) {} };
  window.addEventListener("error", (e) => post((e.message || "error") + " @ " + (e.filename || "") + ":" + (e.lineno || 0)));
  window.addEventListener("unhandledrejection", (e) => post("unhandled: " + (e.reason && (e.reason.stack || e.reason.message) || e.reason)));
  const ce = console.error; console.error = function(...a) { post("console.error: " + a.join(" ")); return ce.apply(console, a); };
})();`

func (w *vwin) onMessage(raw string) {
	var m struct {
		T    string `json:"t"`
		URL  string `json:"url"`
		Dark bool   `json:"dark"`
		Msg  string `json:"msg"`
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
		_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", u.String()).Start()
	case "theme":
		setDarkTitleBar(w.hwnd, m.Dark)
	case "log":
		if dir, err := os.UserCacheDir(); err == nil {
			if f, err := os.OpenFile(filepath.Join(dir, "vito", "window.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
				fmt.Fprintln(f, time.Now().Format("15:04:05"), m.Msg)
				f.Close()
			}
		}
	}
}

// onKey keeps reloading (F5, Ctrl+R) with the browser's keys switched off.
func (w *vwin) onKey(vk uint) bool {
	ctrl, _, _ := procGetKeyState.Call(0x11)
	if vk == vkF5 || (vk == vkR && int16(ctrl) < 0) {
		w.web.Eval("location.reload()")
		return true
	}
	return false
}

// dispatch runs f on the window's thread.
func (w *vwin) dispatch(f func()) {
	w.mu.Lock()
	w.queue = append(w.queue, f)
	w.mu.Unlock()
	procPostMessageW.Call(w.hwnd, wmApp, 0, 0)
}

func (w *vwin) runQueue() {
	w.mu.Lock()
	q := w.queue
	w.queue = nil
	w.mu.Unlock()
	for _, f := range q {
		f()
	}
}

// watchDaemon closes the window once the daemon is gone: after `vito quit`
// (the installer and the updater ask for that) the window must not hold
// vito.exe open, and a window on a stopped daemon only shows errors.
func (w *vwin) watchDaemon(port int) {
	misses := 0
	for {
		time.Sleep(500 * time.Millisecond)
		if daemonRunning(port) {
			misses = 0
			continue
		}
		if misses++; misses >= 2 {
			// Gone without closing the window first (a restart, an update):
			// the daemon that starts next opens it again.
			markReopen()
			procPostMessageW.Call(w.hwnd, wmClose, 0, 0)
			return
		}
	}
}

// setRelaunch gives the window Vito's AppUserModelID and says how to start it
// again, so a window pinned to the taskbar runs `vito open` (not the bare exe,
// which prints its usage) and groups with the Start menu's Vito shortcut.
func setRelaunch(hwnd uintptr) {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	iid := windows.GUID{Data1: 0x886D8EEB, Data2: 0x8CF2, Data3: 0x4446,
		Data4: [8]byte{0x8D, 0x02, 0xCD, 0xBA, 0x1D, 0xBD, 0xCF, 0x99}} // IID_IPropertyStore
	var ps *struct{ vtbl *[8]uintptr }
	if hr, _, _ := procSHGetPropertyStoreWnd.Call(hwnd, uintptr(unsafe.Pointer(&iid)),
		uintptr(unsafe.Pointer(&ps))); hr != 0 || ps == nil {
		return
	}
	defer syscall.SyscallN(ps.vtbl[2], uintptr(unsafe.Pointer(ps))) // Release
	type propKey struct {
		fmtid windows.GUID
		pid   uint32
	}
	type propVariant struct {
		vt  uint16
		_   [3]uint16
		val *uint16
		_   uintptr
	}
	appModel := windows.GUID{Data1: 0x9F4C2855, Data2: 0x9F79, Data3: 0x4B39,
		Data4: [8]byte{0xA8, 0xD0, 0xE1, 0xD4, 0x2D, 0xE1, 0xD5, 0xF3}} // PKEY_AppUserModel_*
	set := func(pid uint32, s string) {
		v, err := windows.UTF16PtrFromString(s)
		if err != nil {
			return
		}
		k := propKey{appModel, pid}
		pv := propVariant{vt: 31, val: v} // VT_LPWSTR
		syscall.SyscallN(ps.vtbl[6], uintptr(unsafe.Pointer(ps)),
			uintptr(unsafe.Pointer(&k)), uintptr(unsafe.Pointer(&pv))) // SetValue
		runtime.KeepAlive(v)
	}
	set(2, `"`+exe+`" open`)                                  // RelaunchCommand
	set(3, exe+",0")                                          // RelaunchIconResource
	set(4, "Vito")                                            // RelaunchDisplayNameResource
	set(5, notify.AppUserModelID)                             // ID, last
	syscall.SyscallN(ps.vtbl[7], uintptr(unsafe.Pointer(ps))) // Commit
}

// closeWindow closes Vito's window, if one is open, before the daemon quits on
// the user's word, so the next daemon does not open it again (markReopen).
func closeWindow() {
	cls, _ := windows.UTF16PtrFromString(windowClass)
	if h, _, _ := procFindWindowW.Call(uintptr(unsafe.Pointer(cls)), 0); h != 0 {
		procPostMessageW.Call(h, wmClose, 0, 0)
	}
}

func windowProc(hwnd, message, wparam, lparam uintptr) uintptr {
	w := theWindow
	if w == nil || hwnd != w.hwnd {
		r, _, _ := procDefWindowProcW.Call(hwnd, message, wparam, lparam)
		return r
	}
	w.mu.Lock()
	ready := w.ready
	w.mu.Unlock()
	switch message {
	case wmSize:
		if ready {
			w.web.Resize()
		}
	case wmMove:
		if ready {
			_ = w.web.NotifyParentWindowPositionChanged()
		}
	case wmActivate:
		if ready && wparam&0xFFFF != 0 {
			w.web.Focus()
		}
	case wmGetMinMaxInfo:
		mmi := *(**minMaxInfo)(unsafe.Pointer(&lparam))
		dpi, _, _ := procGetDpiForWindow.Call(hwnd)
		if dpi == 0 {
			dpi = 96
		}
		mmi.minTrackSize = point{int32(760 * dpi / 96), int32(560 * dpi / 96)}
		return 0
	case wmDpiChanged:
		// Windows suggests the rectangle for the new DPI; take it.
		r := *(**rect)(unsafe.Pointer(&lparam))
		procSetWindowPos.Call(hwnd, 0, uintptr(r.left), uintptr(r.top),
			uintptr(r.right-r.left), uintptr(r.bottom-r.top), 0x0004|0x0010) // NOZORDER NOACTIVATE
		return 0
	case wmCopyData:
		cds := *(**copyDataStruct)(unsafe.Pointer(&lparam))
		if cds != nil && cds.dwData == copyDataNav && cds.cbData > 0 && cds.cbData < 4096 && ready {
			b := unsafe.Slice(cds.lpData, cds.cbData)
			u, _ := json.Marshal(strings.TrimRight(string(b), "\x00"))
			w.web.Eval("location.href=" + string(u))
			focusWindow(hwnd)
			return 1
		}
	case wmClose:
		w.savePlacement()
		procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, message, wparam, lparam)
	return r
}

// keptPlacement is where the window stood, and whether it was maximised.
type keptPlacement struct {
	Left, Top, Right, Bottom int32
	Maximized                bool
}

func (w *vwin) loadPlacement() (windowPlacement, bool) {
	var p windowPlacement
	if w.placeF == "" {
		return p, false
	}
	b, err := os.ReadFile(w.placeF)
	if err != nil {
		return p, false
	}
	var k keptPlacement
	if json.Unmarshal(b, &k) != nil || k.Right-k.Left < 200 || k.Bottom-k.Top < 200 {
		return p, false
	}
	r := rect{k.Left, k.Top, k.Right, k.Bottom}
	// A screen that was unplugged since: start in the default place instead.
	if mon, _, _ := procMonitorFromRect.Call(uintptr(unsafe.Pointer(&r)), 0); mon == 0 {
		return p, false
	}
	p.length = uint32(unsafe.Sizeof(p))
	p.rcNormal = r
	p.showCmd = 1
	if k.Maximized {
		p.showCmd = swShowMax
	}
	return p, true
}

func (w *vwin) savePlacement() {
	if w.placeF == "" {
		return
	}
	var p windowPlacement
	p.length = uint32(unsafe.Sizeof(p))
	if r, _, _ := procGetWindowPlacement.Call(w.hwnd, uintptr(unsafe.Pointer(&p))); r == 0 {
		return
	}
	k := keptPlacement{p.rcNormal.left, p.rcNormal.top, p.rcNormal.right, p.rcNormal.bottom, p.showCmd == swShowMax}
	b, _ := json.Marshal(k)
	_ = os.MkdirAll(filepath.Dir(w.placeF), 0o755)
	_ = os.WriteFile(w.placeF, b, 0o644)
}

// webView2DataPath keeps WebView2's profile with Vito's cache, not next to
// the exe (the install folder, or dist/ for a development build).
func webView2DataPath() string {
	if dir, err := os.UserCacheDir(); err == nil {
		return filepath.Join(dir, "vito", "webview2")
	}
	return ""
}

func findVitoWindow() uintptr {
	cls, _ := windows.UTF16PtrFromString(windowClass)
	h, _, _ := procFindWindowW.Call(uintptr(unsafe.Pointer(cls)), 0)
	return h
}

func focusWindow(h uintptr) {
	if r, _, _ := procIsIconic.Call(h); r != 0 {
		procShowWindow.Call(h, swRestore)
	}
	procSetForegroundWindow.Call(h)
}

func sendNavigate(h uintptr, u string) {
	b := append([]byte(u), 0)
	cds := copyDataStruct{dwData: copyDataNav, cbData: uint32(len(b)), lpData: &b[0]}
	procAllowSetForegroundWnd.Call(^uintptr(0)) // ASFW_ANY: the window may come forward itself
	procSendMessageW.Call(h, wmCopyData, 0, uintptr(unsafe.Pointer(&cds)))
}

// setWindowIcon gives the window the exe's own icon (rsrc_windows.syso).
func setWindowIcon(hwnd uintptr) {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	p, _ := windows.UTF16PtrFromString(exe)
	var large, small uintptr
	if n, _, _ := procExtractIconExW.Call(uintptr(unsafe.Pointer(p)), 0,
		uintptr(unsafe.Pointer(&large)), uintptr(unsafe.Pointer(&small)), 1); n == 0 {
		return
	}
	if large != 0 {
		procSendMessageW.Call(hwnd, wmSetIcon, 1, large)
	}
	if small != 0 {
		procSendMessageW.Call(hwnd, wmSetIcon, 0, small)
	}
}

// setDarkTitleBar asks DWM for the dark caption (attribute 20,
// DWMWA_USE_IMMERSIVE_DARK_MODE) and repaints it at once.
func setDarkTitleBar(hwnd uintptr, dark bool) {
	v := int32(0)
	if dark {
		v = 1
	}
	procDwmSetWindowAttribute.Call(hwnd, 20, uintptr(unsafe.Pointer(&v)), 4)
	// DWM repaints the caption only when the frame changes or the window is
	// (de)activated; without this a theme switch shows at the next focus change.
	const swpFlags = 0x0001 | 0x0002 | 0x0004 | 0x0010 | 0x0020 // NOSIZE NOMOVE NOZORDER NOACTIVATE FRAMECHANGED
	procSetWindowPos.Call(hwnd, 0, 0, 0, 0, 0, swpFlags)
	if fg, _, _ := procGetForegroundWindow.Call(); fg == hwnd {
		procSendMessageW.Call(hwnd, wmNCActivate, 0, 0)
		procSendMessageW.Call(hwnd, wmNCActivate, 1, 0)
	}
}

func wantDarkTitle(theme string) bool {
	switch theme {
	case "dark":
		return true
	case "light":
		return false
	}
	k, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	light, _, err := k.GetIntegerValue("AppsUseLightTheme")
	return err == nil && light == 0
}

// startDaemon starts `vito serve` from this same binary, detached, so closing
// the window leaves it running.
func startDaemon() error {
	exe, err := selfexe.Path()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "serve")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP,
		HideWindow:    true,
	}
	return cmd.Start()
}

// launchWindow opens `vito window <url>`, or says there is no window to be
// had (no WebView2 runtime), so the caller opens the web app or a tab.
func launchWindow(u string) error {
	if !webView2Available() {
		return webapp.ErrNoNative
	}
	if h := findVitoWindow(); h != 0 { // already open: no new process needed
		sendNavigate(h, u)
		focusWindow(h)
		return nil
	}
	exe, err := selfexe.Path()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "window", u)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS}
	procAllowSetForegroundWnd.Call(^uintptr(0))
	return cmd.Start()
}

// windowSupported: this system can show Vito's own window.
func windowSupported() bool { return webView2Available() }
