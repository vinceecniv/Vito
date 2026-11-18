// Package frontapp names the program the user is dictating into: the one with
// the keyboard when a dictation starts. The cleanup is told it, so it can fit
// the text's form to the place it goes, and a rule set can be chosen per
// program. Only the program, never a window's title: a title carries an
// e-mail's subject or a document's name, which have no business going to a
// cleanup provider.
//
// Each system has its own way to ask, and some none: Windows and macOS always
// answer, on Linux niri, Hyprland and X11 sessions do; GNOME and KDE on Wayland
// tell an ordinary client nothing.
package frontapp

import (
	"strings"
	"sync"
)

// App is a program: ID is a stable key in lower case ("outlook",
// "org.mozilla.firefox"), Name what people call it ("Microsoft Outlook").
type App struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Known reports whether the desktop said anything.
func (a App) Known() bool { return a.ID != "" }

// Current is the program with the keyboard now; a zero App when the system
// cannot say.
func Current() App {
	a := current()
	a.ID = strings.ToLower(strings.TrimSpace(a.ID))
	a.Name = strings.TrimSpace(a.Name)
	if a.ID != "" && a.Name == "" {
		a.Name = a.ID
	}
	return a
}

// shellNames are Windows' own parts, whose files describe themselves badly
// or not at all ("Microsoft® Windows® Operating System" is every one's
// product name).
var shellNames = map[string]string{
	"searchhost":              "Windows Search",
	"searchapp":               "Windows Search",
	"startmenuexperiencehost": "Start menu",
	"shellexperiencehost":     "Windows Shell",
	"textinputhost":           "Windows input",
	"lockapp":                 "Lock screen",
	"explorer":                "File Explorer",
	"applicationframehost":    "Windows app",
}

// ShellName is the name of one of Windows' own parts by its id, or "".
func ShellName(id string) string { return shellNames[strings.ToLower(id)] }

// osProduct reports a product name that only says "Windows".
func osProduct(s string) bool {
	return strings.Contains(strings.ToLower(s), "operating system")
}

// names caches what a program is called: looking it up reads a file.
var (
	namesMu sync.Mutex
	names   = map[string]string{}
)

func cachedName(key string, look func() string) string {
	namesMu.Lock()
	n, ok := names[key]
	namesMu.Unlock()
	if ok {
		return n
	}
	n = look()
	namesMu.Lock()
	names[key] = n
	namesMu.Unlock()
	return n
}
