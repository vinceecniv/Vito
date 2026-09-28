package tray

import "golang.org/x/sys/windows"

// osLang returns the user's first preferred display language ("pt-PT" → "pt").
func osLang() string {
	langs, err := windows.GetUserPreferredUILanguages(windows.MUI_LANGUAGE_NAME)
	if err != nil || len(langs) == 0 {
		return ""
	}
	return langCode(langs[0])
}
