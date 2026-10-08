package history

import (
	"testing"
	"time"
)

func TestImportIsIdempotentAndKeepsPrunedDays(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AppData", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	s, err := NewStore(500, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	now := time.Now()
	today := now.Format("2006-01-02")
	old := now.AddDate(0, 0, -40).Format("2006-01-02")
	entries := []Entry{
		{ID: "a1", Timestamp: now, Raw: "een twee drie", DurationMS: 1000},
		{ID: "a2", Timestamp: now, Raw: "vier vijf", DurationMS: 500, Favorite: true},
	}
	// The browser counted one more dictation today than it kept, and a day
	// whose entries are gone.
	days := map[string]DaySums{
		today: {Words: 8, Sentences: 3, Activations: 3, DurationMS: 2000},
		old:   {Words: 10, Sentences: 2, Activations: 2, DurationMS: 4000},
	}

	n, err := s.Import("browser-1", entries, days)
	if err != nil || n != 2 {
		t.Fatalf("Import = %d, %v", n, err)
	}
	check := func(when string) {
		t.Helper()
		w, _, a, d, _, err := s.DayTotals(old, today)
		if err != nil {
			t.Fatal(err)
		}
		if w != 18 || a != 5 || d != 6000 {
			t.Fatalf("%s: words %d activations %d duration %d, want 18 5 6000", when, w, a, d)
		}
		if c, _ := s.Count("", false); c != 2 {
			t.Fatalf("%s: %d entries, want 2", when, c)
		}
	}
	check("first import")

	// The same browser again: nothing doubles.
	if n, err := s.Import("browser-1", entries, days); err != nil || n != 0 {
		t.Fatalf("second Import = %d, %v", n, err)
	}
	check("second import")

	// It dictated once more since: only that is added.
	entries = append(entries, Entry{ID: "a3", Timestamp: now, Raw: "zes", DurationMS: 300})
	days[today] = DaySums{Words: 9, Sentences: 4, Activations: 4, DurationMS: 2300}
	if n, err := s.Import("browser-1", entries, days); err != nil || n != 1 {
		t.Fatalf("third Import = %d, %v", n, err)
	}
	if w, _, a, _, _, _ := s.DayTotals(today, today); w != 9 || a != 4 {
		t.Fatalf("today after the third import: words %d activations %d, want 9 4", w, a)
	}
	if favs, _ := s.FavoriteIDs(); !favs["a2"] {
		t.Fatal("the favorite did not come along")
	}
}

func TestOwnLeavesOutWhatSyncBroughtIn(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AppData", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	s, err := NewStore(500, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now()
	today := now.Format("2006-01-02")
	if err := s.Append(Entry{ID: "mine", Timestamp: now, Raw: "hier gedicteerd", DurationMS: 1000}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Import(SyncSource("laptop"), []Entry{{ID: "theirs", Timestamp: now, Raw: "daar gedicteerd ja", DurationMS: 2000}},
		map[string]DaySums{today: {Words: 3, Sentences: 1, Activations: 1, DurationMS: 2000}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Import("web:browser", []Entry{{ID: "moved", Timestamp: now, Raw: "uit de browser", DurationMS: 500}},
		map[string]DaySums{today: {Words: 3, Sentences: 1, Activations: 1, DurationMS: 500}}); err != nil {
		t.Fatal(err)
	}
	own, err := s.OwnEntries(now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(own) != 2 {
		t.Fatalf("own entries %d, want 2 (dictated here + moved from the browser)", len(own))
	}
	sums, err := s.OwnDaySums()
	if err != nil {
		t.Fatal(err)
	}
	if got := sums[today]; got.Words != 5 || got.Activations != 2 {
		t.Fatalf("own sums %+v, want 5 words in 2 activations", got)
	}
}
