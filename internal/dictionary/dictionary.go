// Package dictionary applies the user's correction list to transcripts.
package dictionary

import (
	"regexp"
	"strings"

	"vito/internal/config"
)

// Apply replaces misheard terms with their intended form, case-insensitively
// and on word boundaries, preserving surrounding text. It runs regardless of
// whether the cleanup pass is on, so corrections also work in raw mode.
func Apply(text string, corrections []config.Correction) string {
	for _, c := range corrections {
		wrong := strings.TrimSpace(c.Wrong)
		if wrong == "" || c.Right == "" {
			continue
		}
		re, err := regexp.Compile(`(?i)\b` + regexp.QuoteMeta(wrong) + `\b`)
		if err != nil {
			continue
		}
		text = re.ReplaceAllString(text, c.Right)
	}
	return text
}
