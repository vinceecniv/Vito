package history

import (
	"encoding/json"
	"os"
	"testing"

	"vito/internal/dictionary"
)

// VITO_SUGGEST_DIR=<dir> prints the dictionary suggestions for a real
// history: a copy of a config folder, so <dir>/vito/vito.db.
func TestSuggestRealHistory(t *testing.T) {
	dir := os.Getenv("VITO_SUGGEST_DIR")
	if dir == "" {
		t.Skip("set VITO_SUGGEST_DIR")
	}
	t.Setenv("APPDATA", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	s, err := NewStore(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	pairs, err := s.DictPairs(3000)
	if err != nil {
		t.Fatal(err)
	}
	// VITO_SUGGEST_CONFIG: a config.json whose dictionary to use, as the
	// server does (its intended spellings as terms, its corrections skipped).
	var terms []string
	have := map[string]bool{}
	if p := os.Getenv("VITO_SUGGEST_CONFIG"); p != "" {
		var c struct {
			Dictionary struct {
				Corrections []struct{ Wrong, Right string }
			}
		}
		b, err := os.ReadFile(p)
		if err == nil {
			err = json.Unmarshal(b, &c)
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, x := range c.Dictionary.Corrections {
			terms = append(terms, x.Right)
			have[DictKey(x.Wrong)] = true
		}
	}
	skip := func(w, r string) bool { return have[w] }
	for _, g := range dictionary.Merge(40, dictionary.Suggest(pairs, skip, 0), dictionary.Variants(pairs, terms, skip)) {
		t.Logf("%-7s %4.1f %2dx left %d heard %2d  %q -> %q  «%s»", g.Kind, g.Score, g.Count, g.Left, g.Heard, g.Wrong, g.Right, g.Example)
	}
}
