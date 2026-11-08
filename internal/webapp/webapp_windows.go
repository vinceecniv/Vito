package webapp

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

// storeGlobs are the browser profiles under %LOCALAPPDATA%:
// Google\Chrome\User Data\Default, BraveSoftware\Brave-Browser\User Data\Default,
// Vivaldi\User Data\Default.
func storeGlobs() []string {
	local := os.Getenv("LOCALAPPDATA")
	if local == "" {
		return nil
	}
	return []string{
		filepath.Join(local, "*", "User Data", "*"),
		filepath.Join(local, "*", "*", "User Data", "*"),
	}
}

// launcher starts app id through the Start menu shortcut its browser made
// (Chrome puts them in "Chrome Apps", Edge and Brave straight in Programs).
// A .lnk keeps its arguments in UTF-16, so the shortcut is found by its
// "--app-id=<id>" in that form, and opened the way Explorer would.
func launcher(id string) func() error {
	want := utf16le("--app-id=" + id)
	var found string
	for _, root := range []string{
		filepath.Join(os.Getenv("APPDATA"), "Microsoft", "Windows", "Start Menu", "Programs"),
		filepath.Join(os.Getenv("USERPROFILE"), "Desktop"),
	} {
		filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || found != "" {
				return nil
			}
			if d.IsDir() || !strings.EqualFold(filepath.Ext(path), ".lnk") {
				return nil
			}
			if b, err := os.ReadFile(path); err == nil && bytes.Contains(b, want) {
				found = path
				return fs.SkipAll
			}
			return nil
		})
		if found != "" {
			return func() error {
				// The browser may take the foreground from Vito: with the
				// manifest's launch_handler it then brings the open window
				// forward instead of opening another, which Windows would
				// otherwise leave flashing in the taskbar.
				procAllowSetForegroundWindow.Call(asfwAny)
				return exec.Command("rundll32", "shell32.dll,ShellExec_RunDLL", found).Start()
			}
		}
	}
	return nil
}

func utf16le(s string) []byte {
	var b []byte
	for _, u := range utf16.Encode([]rune(s)) {
		b = append(b, byte(u), byte(u>>8))
	}
	return b
}

var procAllowSetForegroundWindow = windows.NewLazySystemDLL("user32.dll").NewProc("AllowSetForegroundWindow")

const asfwAny = ^uintptr(0) // ASFW_ANY

// focusApp: Windows has no app id on a window short of COM; the browser, let
// into the foreground and told by launch_handler, focuses its own window.
func focusApp(string) bool { return false }
