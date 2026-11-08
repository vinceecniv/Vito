package dictionary

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"vito/internal/config"
)

// A snippet's trigger is found in the transcript and swapped for a placeholder
// (⟦1⟧, ⟦2⟧, …) before the cleanup pass, so the model never sees — and so can
// never rewrite — the fixed text; the text goes in after. Without a cleanup pass
// Expand puts it in straight away.

var placeholderRe = regexp.MustCompile(`⟦(\d+)⟧`)

// Mark replaces every spoken trigger in text with a placeholder and returns the
// snippet texts in placeholder order. Matching ignores case, punctuation and
// spacing between the trigger's words ("Mijn linked in." finds "mijn
// LinkedIn"), and "m'n"/"z'n" count as "mijn"/"zijn". A transcript that is
// nothing but one trigger becomes the bare placeholder: the speaker asked for
// that text, not a sentence ending in a full stop.
func Mark(text string, snippets []config.Snippet) (string, []string) {
	type pat struct {
		re   *regexp.Regexp
		text string
		n    int // trigger length: longer ones first, so one never eats another
	}
	var pats []pat
	for _, s := range snippets {
		if s.Text == "" {
			continue
		}
		if re := triggerRe(s.Trigger); re != nil {
			pats = append(pats, pat{re, s.Text, len(s.Trigger)})
		}
	}
	if len(pats) == 0 {
		return text, nil
	}
	sort.SliceStable(pats, func(i, j int) bool { return pats[i].n > pats[j].n })
	var texts []string
	for _, p := range pats {
		var b strings.Builder
		last := 0
		for _, m := range p.re.FindAllStringIndex(text, -1) {
			if !boundary(text, m[0], m[1]) {
				continue
			}
			texts = append(texts, p.text)
			b.WriteString(text[last:m[0]])
			b.WriteString("⟦" + strconv.Itoa(len(texts)) + "⟧")
			last = m[1]
		}
		if last > 0 {
			b.WriteString(text[last:])
			text = b.String()
		}
	}
	if len(texts) == 1 && placeholderRe.MatchString(text) {
		if bare := strings.TrimFunc(text, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsPunct(r) && r != '⟦' && r != '⟧' }); bare == "⟦1⟧" {
			text = bare
		}
	}
	return text, texts
}

// Expand puts the snippet texts in place of their placeholders.
func Expand(marked string, texts []string) string {
	if len(texts) == 0 {
		return marked
	}
	return placeholderRe.ReplaceAllStringFunc(marked, func(ph string) string {
		i, _ := strconv.Atoi(placeholderRe.FindStringSubmatch(ph)[1])
		if i < 1 || i > len(texts) {
			return ph
		}
		return texts[i-1]
	})
}

// Restore expands the cleanup pass's output. ok is false when the model lost,
// doubled or invented a placeholder: then its text cannot be trusted to carry
// the snippets, and the caller keeps the transcript instead.
func Restore(cleaned string, texts []string) (string, bool) {
	if len(texts) == 0 {
		return cleaned, !placeholderRe.MatchString(cleaned)
	}
	seen := make([]int, len(texts))
	for _, m := range placeholderRe.FindAllStringSubmatch(cleaned, -1) {
		i, _ := strconv.Atoi(m[1])
		if i < 1 || i > len(texts) {
			return cleaned, false
		}
		seen[i-1]++
	}
	for _, n := range seen {
		if n != 1 {
			return cleaned, false
		}
	}
	return Expand(cleaned, texts), true
}

// OnlySnippets reports whether marked holds nothing but placeholders: there is
// then nothing for a cleanup pass to do.
func OnlySnippets(marked string) bool {
	if !placeholderRe.MatchString(marked) {
		return false
	}
	rest := placeholderRe.ReplaceAllString(marked, "")
	return strings.TrimFunc(rest, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsPunct(r) }) == ""
}

// triggerRe builds the pattern for a spoken trigger: its words in order,
// anything but letters and digits allowed between them, a camel-cased word
// ("LinkedIn") also matching when said or written apart.
func triggerRe(trigger string) *regexp.Regexp {
	var words []string
	for _, w := range strings.FieldsFunc(normalizeTrigger(trigger), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		words = append(words, splitCamel(w)...)
	}
	if len(words) == 0 {
		return nil
	}
	parts := make([]string, len(words))
	for i, w := range words {
		switch strings.ToLower(w) {
		case "mijn":
			parts[i] = `(?:mijn|m['’]n)`
		case "zijn":
			parts[i] = `(?:zijn|z['’]n)`
		default:
			parts[i] = regexp.QuoteMeta(w)
		}
	}
	re, err := regexp.Compile(`(?i)` + strings.Join(parts, `[^\p{L}\p{N}⟦⟧]*`))
	if err != nil {
		return nil
	}
	return re
}

var shortForms = strings.NewReplacer("m'n", "mijn", "m’n", "mijn", "M'n", "mijn", "M’n", "mijn",
	"z'n", "zijn", "z’n", "zijn", "Z'n", "zijn", "Z’n", "zijn")

func normalizeTrigger(s string) string { return shortForms.Replace(strings.TrimSpace(s)) }

// splitCamel cuts a word where a lower-case letter meets a capital.
func splitCamel(w string) []string {
	var out []string
	start := 0
	prev := rune(0)
	for i, r := range w {
		if i > 0 && unicode.IsLower(prev) && unicode.IsUpper(r) {
			out = append(out, w[start:i])
			start = i
		}
		prev = r
	}
	return append(out, w[start:])
}

// boundary reports whether text[i:j] stands as whole words.
func boundary(text string, i, j int) bool {
	if i > 0 {
		if r, _ := utf8.DecodeLastRuneInString(text[:i]); unicode.IsLetter(r) || unicode.IsDigit(r) {
			return false
		}
	}
	if j < len(text) {
		if r, _ := utf8.DecodeRuneInString(text[j:]); unicode.IsLetter(r) || unicode.IsDigit(r) {
			return false
		}
	}
	return true
}
