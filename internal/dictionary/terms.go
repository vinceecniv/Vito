package dictionary

import (
	"sort"
	"strings"

	"vito/internal/config"
)

// Terms are the dictionary's intended spellings as a vocabulary for the
// speech recognition, best first: the most used corrections, then the most
// recently used, then the newest entries. Each spelling once, whatever its
// case. uses gives a misheard word's count and last use (unix ms).
func Terms(corrections []config.Correction, uses func(wrong string) (n int, last int64)) []string {
	type term struct {
		text     string
		n        int
		last, at int64
	}
	byKey := map[string]*term{}
	var all []*term
	for _, c := range corrections {
		r := strings.TrimSpace(c.Right)
		if r == "" {
			continue
		}
		k := strings.ToLower(r)
		t := byKey[k]
		if t == nil {
			t = &term{text: r}
			byKey[k] = t
			all = append(all, t)
		}
		t.at = max(t.at, c.Created)
		if uses != nil {
			n, last := uses(strings.ToLower(strings.TrimSpace(c.Wrong)))
			t.n += n
			t.last = max(t.last, last)
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		a, b := all[i], all[j]
		if a.n != b.n {
			return a.n > b.n
		}
		if a.last != b.last {
			return a.last > b.last
		}
		return a.at > b.at
	})
	out := make([]string, len(all))
	for i, t := range all {
		out[i] = t.text
	}
	return out
}

// Pick takes terms in order while they fit: at most n of them, each at most
// maxLen characters and maxWords words (0: no limit), and together, joined
// by ", ", at most maxTotal characters (0: no limit).
func Pick(terms []string, n, maxLen, maxWords, maxTotal int) []string {
	var out []string
	total := 0
	for _, t := range terms {
		if n > 0 && len(out) >= n {
			break
		}
		l := len([]rune(t))
		if (maxLen > 0 && l > maxLen) || (maxWords > 0 && len(strings.Fields(t)) > maxWords) {
			continue
		}
		add := l
		if len(out) > 0 {
			add += 2
		}
		if maxTotal > 0 && total+add > maxTotal {
			break
		}
		total += add
		out = append(out, t)
	}
	return out
}
