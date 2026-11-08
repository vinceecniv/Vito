package webapp

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var appID = regexp.MustCompile(`--app-id=([a-p]{32})`)

// storeGlobs are the browser profiles: ~/.config/google-chrome/Default,
// ~/.config/BraveSoftware/Brave-Browser/Default.
func storeGlobs() []string {
	cfg, err := os.UserConfigDir()
	if err != nil {
		return nil
	}
	return []string{filepath.Join(cfg, "*", "*"), filepath.Join(cfg, "*", "*", "*")}
}

// launcher starts app id through the desktop entry its browser wrote.
func launcher(id string) func() error {
	for _, entry := range desktopEntries() {
		exe, eid, ok := appEntry(entry)
		if !ok || eid != id {
			continue
		}
		return func() error { return exec.Command(exe[0], exe[1:]...).Start() }
	}
	return nil
}

// desktopEntries are the user's desktop entries, where browsers put the apps
// they install.
func desktopEntries() []string {
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		data = filepath.Join(home, ".local", "share")
	}
	files, _ := filepath.Glob(filepath.Join(data, "applications", "*.desktop"))
	return files
}

// appEntry reads a desktop entry that starts a browser's web app: its command
// and the app's id.
func appEntry(path string) (exe []string, id string, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, "", false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	inMain := false
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			inMain = line == "[Desktop Entry]"
			continue
		}
		if !inMain || !strings.HasPrefix(line, "Exec=") {
			continue
		}
		m := appID.FindStringSubmatch(line)
		if m == nil {
			return nil, "", false
		}
		for _, field := range strings.Fields(strings.TrimPrefix(line, "Exec=")) {
			if strings.HasPrefix(field, "%") { // %U, %F…: nothing to pass
				continue
			}
			exe = append(exe, strings.Trim(field, `"`))
		}
		return exe, m[1], len(exe) > 0
	}
	return nil, "", false
}

// focusApp brings an open window of web app id forward. Only niri is asked:
// its IPC can focus any window, and a Chromium app window's app id there is
// "<browser>-<id>-<profile>". Elsewhere it reports false and the browser is
// started, which with the manifest's launch_handler focuses the open window.
func focusApp(id string) bool {
	if os.Getenv("NIRI_SOCKET") == "" {
		return false
	}
	out, err := exec.Command("niri", "msg", "--json", "windows").Output()
	if err != nil {
		return false
	}
	var windows []struct {
		ID        uint64 `json:"id"`
		AppID     string `json:"app_id"`
		IsFocused bool   `json:"is_focused"`
	}
	if json.Unmarshal(out, &windows) != nil {
		return false
	}
	for _, w := range windows {
		if strings.Contains(w.AppID, "-"+id+"-") {
			return exec.Command("niri", "msg", "action", "focus-window", "--id", strconv.FormatUint(w.ID, 10)).Run() == nil
		}
	}
	return false
}
