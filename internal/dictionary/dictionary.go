// Package dictionary applies the user's correction list to transcripts and
// prepares keyterms for the STT session.
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

// Keyterms returns the deduplicated term list for the STT keyterms prompt,
// capped at the API maximum of 100 terms.
func Keyterms(d config.Dictionary) []string {
	const maxTerms = 100
	seen := make(map[string]bool)
	out := make([]string, 0, len(d.Keyterms))
	for _, t := range d.Keyterms {
		t = strings.TrimSpace(t)
		if t == "" || seen[strings.ToLower(t)] {
			continue
		}
		seen[strings.ToLower(t)] = true
		out = append(out, t)
		if len(out) == maxTerms {
			break
		}
	}
	return out
}
