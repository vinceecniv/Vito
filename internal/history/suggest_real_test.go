package history

import (
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
	for _, g := range dictionary.Suggest(pairs, nil, 20) {
		t.Logf("%5.1f %2dx %3.0f%% heard %2d  %q -> %q  «%s»", g.Score, g.Count, g.Ratio*100, g.Heard, g.Wrong, g.Right, g.Example)
	}
}
