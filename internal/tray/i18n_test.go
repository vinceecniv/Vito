package tray

import (
	"io/fs"
	"strings"
	"testing"

	"vito/web"
)

// trayKeys are the English source strings relabel and statusText look up.
var trayKeys = []string{
	"Version", "Status", "Idle", "Recording…", "Processing…", "idle", "recording", "processing",
	"Open Vito", "Open the web UI in the browser",
	"Start / stop dictation", "Start or stop a recording",
	"Cancel", "Cancel the current recording",
	"Media while dictating", "What to do with playing media", "Duck volume", "Pause", "Off",
	"Cleanup on by default", "Run every dictation through the AI cleanup",
	"Enter after text", "Automatically adds an Enter (sends chat or terminal input, for example)",
	"Start with the system", "Start Vito when you log in",
	"Quit Vito", "Stop the daemon",
}

// Every tray string must be in TR.nl — the contract the language files are
// checked against — and translated in every shipped language.
func TestTrayStringsTranslated(t *testing.T) {
	nl := dutchTable()
	if len(nl) < 100 {
		t.Fatalf("TR.nl not parsed from index.html (%d entries)", len(nl))
	}
	langs := []string{"nl"}
	files, _ := fs.Glob(web.I18n, "i18n/*.json")
	for _, f := range files {
		langs = append(langs, strings.TrimSuffix(strings.TrimPrefix(f, "i18n/"), ".json"))
	}
	for _, l := range langs {
		tbl := table(l)
		for _, k := range trayKeys {
			if tbl[k] == "" {
				t.Errorf("%s: tray string %q not translated", l, k)
			}
		}
	}
}

func TestResolveLang(t *testing.T) {
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_MESSAGES", "")
	t.Setenv("LANG", "pt_PT.UTF-8")
	if runtimeUsesEnv() {
		if got := resolveLang(""); got != "pt" {
			t.Errorf("auto with LANG=pt_PT: got %q, want pt", got)
		}
	}
	if got := resolveLang("de"); got != "de" {
		t.Errorf("configured de: got %q", got)
	}
	if got := resolveLang("xx"); got != "pt" && got != "en" && runtimeUsesEnv() {
		t.Errorf("unshipped configured language should fall back, got %q", got)
	}
	if got := translator("pt")("Quit Vito"); got != "Sair do Vito" {
		t.Errorf("pt Quit Vito = %q", got)
	}
	if got := translator("en")("Quit Vito"); got != "Quit Vito" {
		t.Errorf("en Quit Vito = %q", got)
	}
	if got := translator("nl")("Quit Vito"); got != "Vito afsluiten" {
		t.Errorf("nl Quit Vito = %q", got)
	}
}

func TestLangCode(t *testing.T) {
	for in, want := range map[string]string{"pt_PT.UTF-8": "pt", "en-GB": "en", "C": "", "C.UTF-8": "", "POSIX": "", "": "", "NL": "nl"} {
		if got := langCode(in); got != want {
			t.Errorf("langCode(%q) = %q, want %q", in, got, want)
		}
	}
}
