package tray

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"sync"

	"vito/web"
)

// The tray speaks the same language as the settings page: English source
// strings are the keys, looked up in the tables the web UI uses. Dutch is read
// from the inline TR.nl table in index.html (it is the contract every language
// is checked against, so there is no second copy to drift); the other
// languages come from the embedded i18n/<code>.json files.

var (
	trMu    sync.Mutex
	trCache = map[string]map[string]string{}
)

// translator returns the lookup for lang; unknown keys fall back to the key,
// which is already the English text — exactly like t() in the web UI.
func translator(lang string) func(string) string {
	tbl := table(lang)
	return func(s string) string {
		if v, ok := tbl[s]; ok && v != "" {
			return v
		}
		return s
	}
}

func table(lang string) map[string]string {
	if lang == "" || lang == "en" {
		return nil
	}
	trMu.Lock()
	defer trMu.Unlock()
	if tbl, ok := trCache[lang]; ok {
		return tbl
	}
	var tbl map[string]string
	if lang == "nl" {
		tbl = dutchTable()
	} else if raw, err := web.I18n.ReadFile("i18n/" + lang + ".json"); err == nil {
		var f struct {
			Strings map[string]string `json:"strings"`
		}
		if json.Unmarshal(raw, &f) == nil {
			tbl = f.Strings
		}
	}
	trCache[lang] = tbl
	return tbl
}

// dutchTable extracts TR.nl from index.html. Its body is JSON apart from
// `//` comment lines and a trailing comma.
func dutchTable() map[string]string {
	const start, end = "const TR = { nl: {", "}, en: {} };"
	src := web.Index
	i := bytes.Index(src, []byte(start))
	if i < 0 {
		return nil
	}
	src = src[i+len(start):]
	j := bytes.Index(src, []byte(end))
	if j < 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString("{")
	for _, line := range strings.Split(string(src[:j]), "\n") {
		if l := strings.TrimSpace(line); l != "" && !strings.HasPrefix(l, "//") {
			b.WriteString(l)
			b.WriteString("\n")
		}
	}
	body := strings.TrimRight(strings.TrimSpace(b.String()), ",") + "}"
	var tbl map[string]string
	if err := json.Unmarshal([]byte(body), &tbl); err != nil {
		return nil
	}
	return tbl
}

// resolveLang mirrors detectLang() in the web UI: the configured ui.lang when
// set, otherwise the OS language, and English when that isn't one we ship.
func resolveLang(configured string) string {
	if configured != "" && shipped(configured) {
		return configured
	}
	if l := osLang(); shipped(l) {
		return l
	}
	return "en"
}

func shipped(lang string) bool {
	if lang == "en" || lang == "nl" {
		return true
	}
	_, err := web.I18n.Open("i18n/" + lang + ".json")
	return err == nil
}

// envLang reads the POSIX locale variables in their order of precedence and
// returns the two-letter language ("pt_PT.UTF-8" → "pt"). "C" and "POSIX"
// carry no language.
func envLang() string {
	for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := os.Getenv(k); v != "" {
			return langCode(v)
		}
	}
	return ""
}

// langCode reduces a locale or BCP 47 tag to its lowercase two-letter language.
func langCode(v string) string {
	if v == "C" || v == "POSIX" || strings.HasPrefix(v, "C.") || len(v) < 2 {
		return ""
	}
	return strings.ToLower(v[:2])
}
