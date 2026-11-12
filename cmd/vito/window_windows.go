//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	webview "github.com/webview/webview_go"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"vito/internal/config"
	"vito/internal/selfexe"
)

// vito window opens Vito's page in a window of its own: WebView2, the Edge
// engine that ships with Windows 11, so the page runs on the same Chromium as
// the PWA without a browser, an install step or the PWA's cache.
//
// It is its own process on purpose. The daemon keeps the tray and the pills on
// their own threads; the window gets the main thread WebView2 wants, closing it
// frees the engine's memory entirely, and a hung page never takes dictation
// down with it. One window at a time: a second `vito window` hands its address
// to the first (WM_COPYDATA) and brings it forward.

const (
	windowTitle = "Vito"
	windowClass = "webview" // the class webview registers its windows under

	wmSetIcon    = 0x0080
	wmCopyData   = 0x004A
	wmNCActivate = 0x0086
	gwlpWndProc  = ^uintptr(3) // -4
	swRestore    = 9
	copyDataNav  = 0x56495430 // "VIT0": marks our WM_COPYDATA
)

var (
	user32                    = windows.NewLazySystemDLL("user32.dll")
	procFindWindowW           = user32.NewProc("FindWindowW")
	procSetForegroundWindow   = user32.NewProc("SetForegroundWindow")
	procShowWindow            = user32.NewProc("ShowWindow")
	procIsIconic              = user32.NewProc("IsIconic")
	procSendMessageW          = user32.NewProc("SendMessageW")
	procSetWindowLongPtrW     = user32.NewProc("SetWindowLongPtrW")
	procCallWindowProcW       = user32.NewProc("CallWindowProcW")
	procSetWindowPos          = user32.NewProc("SetWindowPos")
	procGetForegroundWindow   = user32.NewProc("GetForegroundWindow")
	procAllowSetForegroundWnd = user32.NewProc("AllowSetForegroundWindow")
	shell32                   = windows.NewLazySystemDLL("shell32.dll")
	procExtractIconExW        = shell32.NewProc("ExtractIconExW")
	dwmapi                    = windows.NewLazySystemDLL("dwmapi.dll")
	procDwmSetWindowAttribute = dwmapi.NewProc("DwmSetWindowAttribute")
	windowOldProc             uintptr
	windowNavigate            func(string)
	windowProcCallback        = windows.NewCallback(windowProc)
)

type copyDataStruct struct {
	dwData uintptr
	cbData uint32
	lpData *byte
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
		u, err := ownPage(args[0], port)
		if err != nil {
			return err
		}
		target = u
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

	// WebView2 keeps its profile next to the exe by default, which is the
	// install folder (or dist/ for a dev build). It belongs with Vito's cache.
	if os.Getenv("WEBVIEW2_USER_DATA_FOLDER") == "" {
		if dir, err := os.UserCacheDir(); err == nil {
			os.Setenv("WEBVIEW2_USER_DATA_FOLDER", filepath.Join(dir, "vito", "webview2"))
		}
	}

	w := webview.New(os.Getenv("VITO_DEBUG") != "")
	if w == nil {
		return fmt.Errorf("could not start WebView2 — is the WebView2 runtime installed?")
	}
	defer w.Destroy()
	w.SetTitle(windowTitle)
	w.SetSize(1280, 860, webview.HintNone)
	w.SetSize(760, 560, webview.HintMin)

	hwnd := uintptr(w.Window())
	setWindowIcon(hwnd)
	setDarkTitleBar(hwnd, wantDarkTitle(cfg.UI.Theme))
	windowNavigate = func(u string) {
		w.Dispatch(func() {
			b, _ := json.Marshal(u)
			w.Eval("location.href=" + string(b))
		})
	}
	windowOldProc, _, _ = procSetWindowLongPtrW.Call(hwnd, gwlpWndProc, windowProcCallback)

	// Links that leave Vito open in the user's browser: a webview has no tabs,
	// and target=_blank would otherwise do nothing.
	_ = w.Bind("vitoOpenExternal", func(raw string) error {
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "mailto") {
			return fmt.Errorf("not an external link")
		}
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", u.String()).Start()
	})
	_ = w.Bind("vitoTheme", func(dark bool) {
		w.Dispatch(func() { setDarkTitleBar(hwnd, dark) })
	})
	w.Init(externalLinksJS)
	w.Navigate(target)
	w.Run()
	return nil
}

func findVitoWindow() uintptr {
	cls, _ := windows.UTF16PtrFromString(windowClass)
	title, _ := windows.UTF16PtrFromString(windowTitle)
	h, _, _ := procFindWindowW.Call(uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(title)))
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

func windowProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	if msg == wmCopyData && lparam != 0 {
		// lparam is the sender's COPYDATASTRUCT, valid for this call (read
		// through a pointer to it, the form vet accepts for a Windows lparam).
		cds := *(**copyDataStruct)(unsafe.Pointer(&lparam))
		if cds.dwData == copyDataNav && cds.cbData > 0 && cds.cbData < 4096 && windowNavigate != nil {
			b := unsafe.Slice(cds.lpData, cds.cbData)
			windowNavigate(strings.TrimRight(string(b), "\x00"))
			focusWindow(hwnd)
			return 1
		}
	}
	r, _, _ := procCallWindowProcW.Call(windowOldProc, hwnd, msg, wparam, lparam)
	return r
}

// setWindowIcon gives the window the exe's own icon (rsrc_windows.syso);
// webview leaves it at the generic application icon.
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
// DWMWA_USE_IMMERSIVE_DARK_MODE, Windows 10 20H1 and later).
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

// LaunchWindow starts `vito window <url>` — what the daemon does instead of
// opening the PWA when ui.window is on.
func launchWindow(u string) error {
	exe, err := selfexe.Path()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "window", u)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS}
	procAllowSetForegroundWnd.Call(^uintptr(0))
	return cmd.Start()
}
