package webapp

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// A Chromium-family browser (Chrome, Chromium, Brave, Edge, Vivaldi) installs
// a web app as a launcher of the system's own kind — a desktop entry on Linux,
// a Start menu shortcut on Windows, a small .app on macOS — that starts the
// browser with the app's id. The id is derived from the app's manifest, so it
// does not tell which page it is; the browser's own record does. Its web-app
// sync store (<profile>/Sync Data/LevelDB, kept with sync off as well) holds
// each app under "web_apps-dt-<id>" followed by the app's start URL, and that
// is what ties a launcher to this daemon's address: a Vito on another port, or
// a web app merely named Vito, is never taken for it.

// installed returns how to start the web app installed from url, or nil.
// When the app is open already its window is brought forward instead, where
// the desktop lets Vito do that (focusApp): started again, the browser would
// open a second window (unless the manifest's launch_handler, which it reads
// only now and then, has reached it).
func installed(url string) func() error {
	for _, id := range appIDs(strings.TrimSuffix(url, "/") + "/") {
		if launch := launcher(id); launch != nil {
			return func() error {
				if focusApp(id) {
					return nil
				}
				return launch()
			}
		}
	}
	return nil
}

var storeKey = regexp.MustCompile(`web_apps-dt-([a-p]{32})`)

// appIDs are the ids of the web apps the browsers on this machine installed
// with start as their start URL.
func appIDs(start string) []string {
	want := []byte(start)
	seen := map[string]bool{}
	var ids []string
	for _, pattern := range storeGlobs() {
		files, _ := filepath.Glob(filepath.Join(pattern, "Sync Data", "LevelDB", "*"))
		for _, path := range files {
			if !strings.HasSuffix(path, ".log") && !strings.HasSuffix(path, ".ldb") {
				continue
			}
			b, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			for _, m := range storeKey.FindAllSubmatchIndex(b, -1) {
				rest := b[m[1]:]
				id := string(b[m[2]:m[3]])
				if bytes.Contains(rest[:min(len(rest), 64)], want) && !seen[id] {
					seen[id] = true
					ids = append(ids, id)
				}
			}
		}
	}
	return ids
}
