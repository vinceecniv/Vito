//go:build windows

package frontapp

import (
	"fmt"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// On Windows the foreground window's process names the program, and its
// version information ("FileDescription") what people call it. A UWP app's
// window belongs to ApplicationFrameHost; the app's own process owns a child.
func current() App {
	fg := windows.GetForegroundWindow()
	if fg == 0 {
		return App{}
	}
	var pid uint32
	if _, err := windows.GetWindowThreadProcessId(fg, &pid); err != nil || pid == 0 {
		return App{}
	}
	path := imagePath(pid)
	if strings.EqualFold(filepath.Base(path), "ApplicationFrameHost.exe") {
		if child := uwpChild(fg, pid); child != 0 {
			path = imagePath(child)
		}
	}
	if path == "" {
		return App{}
	}
	id := strings.TrimSuffix(strings.ToLower(filepath.Base(path)), ".exe")
	keepIcon(id, path)
	return App{ID: id, Name: cachedName(strings.ToLower(path), func() string { return describe(path) })}
}

func imagePath(pid uint32) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
		return ""
	}
	return windows.UTF16ToString(buf[:n])
}

// uwpChild finds the process of the first child window that is not the
// frame host's own.
func uwpChild(parent windows.HWND, host uint32) uint32 {
	var found uint32
	cb := syscall.NewCallback(func(h windows.HWND, _ uintptr) uintptr {
		var pid uint32
		if _, err := windows.GetWindowThreadProcessId(h, &pid); err == nil && pid != 0 && pid != host {
			found = pid
			return 0
		}
		return 1
	})
	windows.EnumChildWindows(parent, cb, nil)
	return found
}

// describe reads the executable's FileDescription, else its ProductName,
// else gives its name without ".exe".
func describe(path string) string {
	fallback := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	size, err := windows.GetFileVersionInfoSize(path, nil)
	if err != nil || size == 0 {
		return fallback
	}
	block := make([]byte, size)
	if err := windows.GetFileVersionInfo(path, 0, size, unsafe.Pointer(&block[0])); err != nil {
		return fallback
	}
	var tr unsafe.Pointer
	var trLen uint32
	langs := []string{"040904b0", "040904e4", "000004b0"}
	if windows.VerQueryValue(unsafe.Pointer(&block[0]), `\VarFileInfo\Translation`, unsafe.Pointer(&tr), &trLen) == nil && trLen >= 4 {
		p := (*[2]uint16)(tr)
		langs = append([]string{fmt.Sprintf("%04x%04x", p[0], p[1])}, langs...)
	}
	for _, field := range []string{"FileDescription", "ProductName"} {
		for _, l := range langs {
			var v unsafe.Pointer
			var n uint32
			if windows.VerQueryValue(unsafe.Pointer(&block[0]), `\StringFileInfo\`+l+`\`+field, unsafe.Pointer(&v), &n) == nil && n > 1 {
				s := strings.TrimSpace(windows.UTF16PtrToString((*uint16)(v)))
				if s != "" {
					return s
				}
			}
		}
	}
	return fallback
}
