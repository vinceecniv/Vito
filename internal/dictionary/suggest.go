package dictionary

import (
	"regexp"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Pair is one dictation as the history keeps it: what the speech recognition
// heard and what the cleanup made of it.
type Pair struct {
	Raw, Cleaned string
}

// Suggestion is a correction the history argues for: the cleanup replaced
// Wrong by Right in Count dictations, which is Ratio of the dictations whose
// raw text has Wrong at all; Heard is how many dictations have Right heard
// right, which tells a misheard name from the cleanup's own choice of words.
type Suggestion struct {
	Wrong   string  `json:"wrong"`
	Right   string  `json:"right"`
	Count   int     `json:"count"`
	Ratio   float64 `json:"ratio"`
	Heard   int     `json:"heard"`
	Example string  `json:"example"`
	Score   float64 `json:"score"`
	// Kind is where it comes from: "repair" (Suggest) or "variant"
	// (Variants: sounds like a term already in the dictionary). Left is
	// how many of a variant's dictations the cleanup left it wrong in.
	Kind string `json:"kind"`
	Left int    `json:"left,omitempty"`
}

// Suggest finds words the speech recognition keeps mishearing, from how the
// cleanup repaired them. A word-level diff of raw against cleaned gives the
// replacements; only those that sound or look alike count (spacing, hyphens
// and apostrophes alone do not), and only when the cleanup replaces the word
// in at least half the dictations it occurs in — so a common word that the
// cleanup sometimes rephrases ("kan je" → "kun je") never shows. skip leaves
// out what the user already has or turned down (keys in lower case).
func Suggest(pairs []Pair, skip func(wrong, right string) bool, limit int) []Suggestion {
	type agg struct {
		ids     map[int]bool
		example string
		right   string
	}
	rawIn := map[string]int{} // lower-case word → dictations whose raw text has it
	found := map[string]*agg{}
	for id, p := range pairs {
		a := words(p.Raw)
		seen := map[string]bool{}
		for _, w := range a {
			if lw := strings.ToLower(w); !seen[lw] {
				seen[lw] = true
				rawIn[lw]++
			}
		}
		if strings.TrimSpace(p.Cleaned) == "" {
			continue
		}
		b := words(p.Cleaned)
		for _, r := range replacements(a, b) {
			if r.i2-r.i1 > 2 || r.j2-r.j1 > 3 {
				continue
			}
			f, t := strings.Join(a[r.i1:r.i2], " "), strings.Join(b[r.j1:r.j2], " ")
			ff, ft := flat(f), flat(t)
			// Spacing, hyphens and apostrophes alone are the cleanup's style
			// ("e-mail" → "e mail") — except a name the recognition split up
			// and the cleanup wrote as one ("price slurper" → "Priceslurper").
			joined := r.i2-r.i1 > r.j2-r.j1 && r.j2-r.j1 == 1 && startsUpper(t) && !strings.ContainsAny(t, "-'’")
			if ff == "" || ft == "" || (ff == ft && !joined) {
				continue
			}
			if ratio(ff, ft) < 0.6 && phonetic(ff) != phonetic(ft) {
				continue
			}
			k := strings.ToLower(f) + "\x00" + t
			g := found[k]
			if g == nil {
				lo, hi := max(0, r.i1-5), min(len(a), r.i2+5)
				g = &agg{ids: map[int]bool{}, example: strings.Join(a[lo:hi], " "), right: t}
				found[k] = g
			}
			g.ids[id] = true
		}
	}
	best := map[string]Suggestion{} // one per misheard word: its strongest repair
	for k, g := range found {
		wrong := k[:strings.IndexByte(k, 0)]
		if skip != nil && skip(wrong, strings.ToLower(g.right)) {
			continue
		}
		in := rawIn[strings.Fields(wrong)[0]]
		if in == 0 {
			in = 1
		}
		n := len(g.ids)
		r := float64(n) / float64(in)
		if r < 0.5 {
			continue
		}
		// Once is enough only for a name: written with a capital and
		// sounding the same.
		if n < 2 && !(startsUpper(g.right) && phonetic(flat(wrong)) == phonetic(flat(g.right))) {
			continue
		}
		heard := rawIn[strings.ToLower(strings.Fields(g.right)[0])]
		score := float64(n) * min(r, 1)
		if heard > 0 {
			score *= 2
		}
		s := Suggestion{Wrong: wrong, Right: g.right, Count: n, Ratio: min(r, 1), Heard: heard, Example: g.example, Score: score, Kind: "repair"}
		if cur, ok := best[wrong]; !ok || s.Score > cur.Score {
			best[wrong] = s
		}
	}
	out := make([]Suggestion, 0, len(best))
	for _, s := range best {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Wrong < out[j].Wrong
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

var wordRE = regexp.MustCompile(`[\p{L}\p{N}][\p{L}\p{N}'’\-.]*[\p{L}\p{N}]|[\p{L}\p{N}]`)

func words(s string) []string { return wordRE.FindAllString(s, -1) }

var flatRE = regexp.MustCompile(`[\s\-'’]`)

// flat is a word with case, spacing, hyphens and apostrophes taken out.
func flat(s string) string { return flatRE.ReplaceAllString(strings.ToLower(s), "") }

func startsUpper(s string) bool {
	for _, r := range s {
		return unicode.IsUpper(r)
	}
	return false
}

// fold drops accents: "één" → "een".
func fold(s string) string {
	var b strings.Builder
	for _, r := range norm.NFD.String(strings.ToLower(s)) {
		if r < 128 {
			b.WriteRune(r)
		} else if !unicode.Is(unicode.Mn, r) && unicode.IsLetter(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

var phonRules = strings.NewReplacer("ph", "f", "ck", "k", "ch", "g", "c", "k", "q", "k", "x", "ks", "z", "s",
	"y", "i", "ij", "i", "ei", "i", "w", "f", "v", "f", "dt", "t", "d", "t", "oo", "o", "ee", "e", "aa", "a", "uu", "u", "h", "")

var vowelsRE = regexp.MustCompile(`[aeiou]+`)

// phonetic is a rough sound key, Dutch and English alike: letters that
// speech recognition mixes up (c/k, f/v/w, d/t, z/s…) made the same and every
// run of vowels one vowel, so "Focas" and "Vocas" share a key.
func phonetic(s string) string {
	w := vowelsRE.ReplaceAllString(phonRules.Replace(fold(s)), "a")
	var b strings.Builder
	var last rune
	for _, r := range w {
		if r != last {
			b.WriteRune(r)
		}
		last = r
	}
	return b.String()
}

// ratio is how alike two strings are, 0 to 1, as Python's difflib reckons
// it closely enough: twice the longest common subsequence over both lengths.
func ratio(a, b string) float64 {
	x, y := []rune(fold(a)), []rune(fold(b))
	if len(x)+len(y) == 0 {
		return 1
	}
	return 2 * float64(lcs(x, y)) / float64(len(x)+len(y))
}

func lcs[T comparable](x, y []T) int {
	prev, cur := make([]int, len(y)+1), make([]int, len(y)+1)
	for i := range x {
		for j := range y {
			if x[i] == y[j] {
				cur[j+1] = prev[j] + 1
			} else {
				cur[j+1] = max(cur[j], prev[j+1])
			}
		}
		prev, cur = cur, prev
	}
	return prev[len(y)]
}

type span struct{ i1, i2, j1, j2 int }

// replacements are the stretches where a and b differ on both sides, from a
// word-level longest common subsequence (case ignored). Insertions and
// deletions alone are not repairs of a word and are left out.
func replacements(a, b []string) []span {
	la, lb := make([]string, len(a)), make([]string, len(b))
	for i := range a {
		la[i] = strings.ToLower(a[i])
	}
	for j := range b {
		lb[j] = strings.ToLower(b[j])
	}
	// Same start and end cost nothing; the table covers only the middle.
	p := 0
	for p < len(la) && p < len(lb) && la[p] == lb[p] {
		p++
	}
	ea, eb := len(la), len(lb)
	for ea > p && eb > p && la[ea-1] == lb[eb-1] {
		ea--
		eb--
	}
	n, m := ea-p, eb-p
	if n == 0 || m == 0 {
		return nil
	}
	if n*m > 4_000_000 { // a very long dictation rewritten throughout: not worth it
		return nil
	}
	// t[i][j] = LCS of la[p+i:ea] and lb[p+j:eb], kept flat.
	w := m + 1
	t := make([]uint16, (n+1)*w)
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if la[p+i] == lb[p+j] {
				t[i*w+j] = t[(i+1)*w+j+1] + 1
			} else {
				t[i*w+j] = max(t[(i+1)*w+j], t[i*w+j+1])
			}
		}
	}
	var out []span
	i, j, si, sj := 0, 0, 0, 0
	flush := func() {
		if i > si && j > sj {
			out = append(out, span{p + si, p + i, p + sj, p + j})
		}
	}
	for i < n && j < m {
		switch {
		case la[p+i] == lb[p+j]:
			flush()
			i++
			j++
			si, sj = i, j
		case t[(i+1)*w+j] >= t[i*w+j+1]:
			i++
		default:
			j++
		}
	}
	i, j = n, m
	flush()
	return out
}
