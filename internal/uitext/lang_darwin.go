package tray

import (
	"os/exec"
	"strings"
	"sync"
)

// osLang prefers the locale variables (set when started from a terminal) and
// otherwise asks for the user's first preferred language: an app launched from
// Finder or at login gets no LANG at all.
var osLang = sync.OnceValue(func() string {
	if l := envLang(); l != "" {
		return l
	}
	out, err := exec.Command("defaults", "read", "-g", "AppleLanguages").Output()
	if err != nil {
		return ""
	}
	// Output is a plist array: ( "pt-PT", "en-GB" ) — take the first entry.
	if f := strings.FieldsFunc(string(out), func(r rune) bool {
		return strings.ContainsRune("(),\" \n\t", r)
	}); len(f) > 0 {
		return langCode(f[0])
	}
	return ""
})
