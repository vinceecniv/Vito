package dictionary

import (
	"strings"

	"vito/internal/config"
)

// Relevant keeps the corrections a transcript could need, for the cleanup's
// prompt: those whose misheard or intended side occurs in it, or a stretch of
// it that looks or sounds like one ("Pricelerper" for "Priceslurper"). Apply
// has already swapped the exact misheard words, so the intended side is the
// usual reason; the rest of the dictionary is only tokens and distraction.
func Relevant(text string, corrections []config.Correction) []config.Correction {
	if len(corrections) == 0 {
		return nil
	}
	ws := words(text)
	whole := flat(text)
	type gram struct {
		f, sound string
		r        []rune // folded, for ratio
	}
	grams := map[int][]gram{} // by word count
	seen := map[string]bool{}
	maxN := 1
	for _, c := range corrections {
		maxN = max(maxN, len(strings.Fields(c.Wrong))+1, len(strings.Fields(c.Right))+1)
	}
	maxN = min(maxN, 5)
	for n := 1; n <= maxN; n++ {
		for i := 0; i+n <= len(ws); i++ {
			f := flat(strings.Join(ws[i:i+n], " "))
			if k := string(rune('0'+n)) + f; !seen[k] {
				seen[k] = true
				grams[n] = append(grams[n], gram{f, phonetic(f), []rune(fold(f))})
			}
		}
	}
	near := func(side string) bool {
		f := flat(strings.TrimSpace(side))
		lf := len([]rune(f))
		if lf == 0 {
			return false
		}
		if lf >= 5 && strings.Contains(whole, f) {
			return true
		}
		n := len(strings.Fields(side))
		sound, fr := phonetic(f), []rune(fold(f))
		for m := max(1, n-1); m <= min(maxN, n+1); m++ {
			for _, g := range grams[m] {
				if g.f == f {
					return true
				}
				if lf < 4 {
					continue
				}
				if d := len(g.r) - len(fr); d > max(2, lf/4) || -d > max(2, lf/4) {
					continue
				}
				if r := 2 * float64(lcs(g.r, fr)) / float64(len(g.r)+len(fr)); r >= 0.75 || (r >= 0.6 && g.sound == sound) {
					return true
				}
			}
		}
		return false
	}
	var out []config.Correction
	for _, c := range corrections {
		if strings.TrimSpace(c.Wrong) == "" || strings.TrimSpace(c.Right) == "" {
			continue
		}
		if near(c.Right) || near(c.Wrong) {
			out = append(out, c)
		}
	}
	return out
}
