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
	text, _ = ApplyUsed(text, corrections)
	return text
}

// ApplyUsed is Apply, also returning the misheard word of every correction
// it made, once per occurrence (for the dictionary's usage counts).
func ApplyUsed(text string, corrections []config.Correction) (string, []string) {
	var used []string
	for _, c := range corrections {
		wrong := strings.TrimSpace(c.Wrong)
		if wrong == "" || c.Right == "" {
			continue
		}
		re, err := regexp.Compile(`(?i)\b` + regexp.QuoteMeta(wrong) + `\b`)
		if err != nil {
			continue
		}
		if n := len(re.FindAllStringIndex(text, -1)); n > 0 {
			for range n {
				used = append(used, wrong)
			}
			text = re.ReplaceAllString(text, c.Right)
		}
	}
	return text, used
}
