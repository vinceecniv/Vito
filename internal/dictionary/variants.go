package dictionary

import (
	"sort"
	"strings"
)

// Variants finds raw words that sound or look like a term already in the
// dictionary (an intended spelling) without being covered by a correction:
// "Pricelerper", "Kama Express", "windmil". Unlike Suggest it needs no
// repair by the cleanup, so it also finds what the cleanup left wrong.
// A stretch of one word more or fewer than the term is tried too, and of
// overlapping matches the closest wins ("camera-express" over "ehm
// camera-express"). Containing the term is no variant ("in Windmill",
// "consignaties"); sounding alike alone is not enough either, the spelling
// must be near as well ("drukken" is no "tray-icon"). skip leaves out what
// the user has or turned down (keys in lower case).
func Variants(pairs []Pair, terms []string, skip func(wrong, right string) bool) []Suggestion {
	type term struct {
		text, flat, sound string
		words             int
	}
	var ts []term
	seenTerm := map[string]bool{}
	for _, t := range terms {
		t = strings.TrimSpace(t)
		f := flat(t)
		if len([]rune(f)) < 4 || seenTerm[f] {
			continue
		}
		seenTerm[f] = true
		ts = append(ts, term{t, f, phonetic(f), len(strings.Fields(t))})
	}
	if len(ts) == 0 {
		return nil
	}
	type agg struct {
		right, example string
		n, left        int
	}
	// How many dictations each word occurs in: a stretch that adds a common
	// word to a term ("de detectie", "bijlage en") is not a variant of it.
	rawIn := map[string]int{}
	for _, p := range pairs {
		seen := map[string]bool{}
		for _, w := range words(p.Raw) {
			if lw := strings.ToLower(w); !seen[lw] {
				seen[lw] = true
				rawIn[lw]++
			}
		}
	}
	common := max(3, len(pairs)*3/100)
	found := map[string]*agg{}
	for _, p := range pairs {
		a := words(p.Raw)
		cleaned := flat(p.Cleaned)
		for _, t := range ts {
			type hit struct {
				i, n int
				r    float64
			}
			var hits []hit
			for _, n := range []int{t.words - 1, t.words, t.words + 1} {
				if n < 1 {
					continue
				}
				for i := 0; i+n <= len(a); i++ {
					g := strings.Join(a[i:i+n], " ")
					fg := flat(g)
					lg, lt := len([]rune(fg)), len([]rune(t.flat))
					if lg < 4 || fg == t.flat || strings.Contains(fg, t.flat) {
						continue
					}
					if d := lg - lt; d > max(2, lt/4) || -d > max(2, lt/4) {
						continue
					}
					r := ratio(fg, t.flat)
					if !(r >= 0.8 || (r >= 0.7 && phonetic(fg) == t.sound)) {
						continue
					}
					if n > 1 && hasCommonExtra(a[i:i+n], t.text, rawIn, common) {
						continue
					}
					// A word that is the term with an ending more or less
					// ("bijlage" for "bijlagen") and is used often is a
					// word of its own, not a mishearing.
					if n == 1 && rawIn[strings.ToLower(g)] >= 3 && (strings.HasPrefix(t.flat, fg) || strings.HasPrefix(fg, string([]rune(t.flat)[:max(0, lt-2)]))) && abs(lg-lt) <= 2 {
						continue
					}
					hits = append(hits, hit{i, n, r})
				}
			}
			sort.SliceStable(hits, func(x, y int) bool { return hits[x].r > hits[y].r })
			taken := make([]bool, len(a))
			seen := map[string]bool{}
		next:
			for _, h := range hits {
				for k := h.i; k < h.i+h.n; k++ {
					if taken[k] {
						continue next
					}
				}
				for k := h.i; k < h.i+h.n; k++ {
					taken[k] = true
				}
				wrong := strings.ToLower(strings.Join(a[h.i:h.i+h.n], " "))
				if seen[wrong] || (skip != nil && skip(wrong, strings.ToLower(t.text))) {
					continue
				}
				seen[wrong] = true
				k := wrong + "\x00" + t.text
				g := found[k]
				if g == nil {
					lo, hi := max(0, h.i-5), min(len(a), h.i+h.n+5)
					g = &agg{right: t.text, example: strings.Join(a[lo:hi], " ")}
					found[k] = g
				}
				g.n++
				if !strings.Contains(cleaned, t.flat) {
					g.left++
				}
			}
		}
	}
	var out []Suggestion
	for k, g := range found {
		wrong := k[:strings.IndexByte(k, 0)]
		// What the cleanup left wrong counts double: there the dictionary
		// is the only repair.
		score := float64(g.n) + float64(g.left)
		out = append(out, Suggestion{Wrong: wrong, Right: g.right, Count: g.n, Ratio: 1, Example: g.example,
			Score: score, Kind: "variant", Left: g.left})
	}
	return out
}

// Merge puts suggestions from both sources in one list, one per misheard
// word (the higher score), best first.
func Merge(limit int, lists ...[]Suggestion) []Suggestion {
	best := map[string]Suggestion{}
	for _, l := range lists {
		for _, s := range l {
			if cur, ok := best[s.Wrong]; !ok || s.Score > cur.Score {
				best[s.Wrong] = s
			}
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

// hasCommonExtra tells whether a stretch holds a word the term does not that
// occurs in many dictations ("de", "en", "bij").
func hasCommonExtra(ws []string, term string, rawIn map[string]int, common int) bool {
	own := map[string]bool{}
	for _, w := range words(term) {
		own[strings.ToLower(w)] = true
	}
	for _, w := range ws {
		if lw := strings.ToLower(w); !own[lw] && rawIn[lw] > common {
			return true
		}
	}
	return false
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
