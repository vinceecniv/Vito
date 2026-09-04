package history

import (
	"strings"
	"testing"
	"time"
)

func TestStoreAndStats(t *testing.T) {
	// Redirect the config dir (os.UserConfigDir uses %AppData% on Windows,
	// $XDG_CONFIG_HOME/$HOME on Linux) so the DB lands in a temp dir.
	dir := t.TempDir()
	t.Setenv("AppData", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)

	s, err := NewStore(500, 0)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer s.Close()

	now := time.Now()
	add := func(raw string, ago time.Duration, cleanupUsed bool, dur int64) {
		if err := s.Append(Entry{
			Timestamp: now.Add(-ago), Raw: raw, CleanupUsed: cleanupUsed,
			Cleaned: raw, DurationMS: dur, Language: "nl",
		}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	add("Hallo wereld dit is een test.", 1*time.Hour, true, 3000)
	add("Nog een zin. En nog een!", 2*time.Hour, true, 2000)
	add("Oud iets buiten de periode", 40*24*time.Hour, false, 1000)

	list, err := s.List("", false, 10, 0)
	if err != nil || len(list) != 3 {
		t.Fatalf("List: n=%d err=%v", len(list), err)
	}
	if list[0].Words == 0 {
		t.Fatal("expected computed word count")
	}
	if got := s.mustFind(t, "wereld"); got != 1 {
		t.Fatalf("search 'wereld' = %d, want 1", got)
	}

	st, err := s.Stats(now, 40, 30)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if st.Activations != 2 { // only the two within 30 days
		t.Fatalf("activations = %d, want 2", st.Activations)
	}
	if st.Words == 0 || st.Sentences == 0 {
		t.Fatalf("expected non-zero words/sentences, got %d/%d", st.Words, st.Sentences)
	}
	// The chart follows the requested window: 30 days means 30 daily bars.
	if len(st.Week) != 30 || st.SeriesUnit != "day" {
		t.Fatalf("series = %d bars of %q, want 30 of \"day\"", len(st.Week), st.SeriesUnit)
	}
	// A wider window switches to coarser buckets so the bar count stays sane.
	wide, err := s.Stats(now, 40, 365)
	if err != nil {
		t.Fatalf("Stats(365): %v", err)
	}
	if wide.SeriesUnit != "month" || len(wide.Week) < 10 || len(wide.Week) > 13 {
		t.Fatalf("365d series = %d bars of %q, want ~12 of \"month\"", len(wide.Week), wide.SeriesUnit)
	}
	// The named periods divide exactly: 4 week bars and 3 month bars.
	quarter, err := s.Stats(now, 40, 92)
	if err != nil {
		t.Fatalf("Stats(92): %v", err)
	}
	if quarter.SeriesUnit != "month" || len(quarter.Week) != 3 {
		t.Fatalf("3-month series = %d bars of %q, want 3 of \"month\"", len(quarter.Week), quarter.SeriesUnit)
	}
	weeks, err := s.Stats(now, 40, 28)
	if err != nil {
		t.Fatalf("Stats(28): %v", err)
	}
	if weeks.SeriesUnit != "week" || len(weeks.Week) != 4 {
		t.Fatalf("4-week series = %d bars of %q, want 4 of \"week\"", len(weeks.Week), weeks.SeriesUnit)
	}
}

// The hourly chart used to cost each hour from its speech duration alone,
// leaving out the AI-cleanup tokens that the cost card and the daily bars both
// include — so an hour's tooltip read lower than "today" on the card. The tokens
// are now stored per entry, so HourTotals carries them into each hour.
func TestHourlyCostIncludesCleanupTokens(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AppData", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)

	s, err := NewStore(500, 0)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer s.Close()

	// Two dictations in the same hour today, both with cleanup token usage.
	at := time.Date(2026, 7, 23, 9, 15, 0, 0, time.Local)
	for i := 0; i < 2; i++ {
		if err := s.Append(Entry{
			Timestamp: at, Raw: "een zin", Cleaned: "Een zin.", CleanupUsed: true,
			DurationMS: 3000, CleanupInTokens: 400, CleanupOutTokens: 120, Language: "nl",
		}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	_, _, _, _, inTok, outTok, _, _, err := s.HourTotals(at)
	if err != nil {
		t.Fatalf("HourTotals: %v", err)
	}
	if inTok[9] != 800 || outTok[9] != 240 {
		t.Fatalf("hour 9 tokens = %d in / %d out, want 800 / 240", inTok[9], outTok[9])
	}

	// And the hourly Stats bar for that hour carries them, so the cost the UI
	// computes matches the cost card and the daily bars.
	st, err := s.Stats(at, 40, 1)
	if err != nil {
		t.Fatalf("Stats(1): %v", err)
	}
	if st.SeriesUnit != "hour" || len(st.Week) != 24 {
		t.Fatalf("hourly series = %d bars of %q, want 24 of \"hour\"", len(st.Week), st.SeriesUnit)
	}
	if st.Week[9].CleanupInTokens != 800 || st.Week[9].CleanupOutTokens != 240 {
		t.Fatalf("hour-9 bar tokens = %d/%d, want 800/240",
			st.Week[9].CleanupInTokens, st.Week[9].CleanupOutTokens)
	}
}

func (s *Store) mustFind(t *testing.T, q string) int {
	t.Helper()
	l, err := s.List(q, false, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	return len(l)
}

// A failed AI pass keeps its reason on the entry, through List, Get and a
// backup round-trip alike — losing it there would put the history back to the
// state this exists to fix: an unpolished dictation with no explanation.
func TestCleanupErrorSurvives(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AppData", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)

	s, err := NewStore(500, 0)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer s.Close()

	const reason = "Anthropic account is out of credit: your credit balance is too low"
	if err := s.Append(Entry{ID: "e1", Raw: "ruwe tekst", Language: "nl", CleanupError: reason}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	// A dictation that went fine carries no reason at all.
	if err := s.Append(Entry{ID: "e2", Raw: "ruwe tekst", Cleaned: "Ruwe tekst.", CleanupUsed: true, Language: "nl"}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	got, ok, err := s.Get("e1")
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if got.CleanupError != reason {
		t.Fatalf("Get: CleanupError = %q, want %q", got.CleanupError, reason)
	}

	list, err := s.List("", false, 10, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, e := range list {
		want := ""
		if e.ID == "e1" {
			want = reason
		}
		if e.CleanupError != want {
			t.Fatalf("List: %s CleanupError = %q, want %q", e.ID, e.CleanupError, want)
		}
	}

	b, err := s.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if err := s.Restore(b); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	got, _, err = s.Get("e1")
	if err != nil {
		t.Fatalf("Get after restore: %v", err)
	}
	if got.CleanupError != reason {
		t.Fatalf("after backup round-trip: CleanupError = %q, want %q", got.CleanupError, reason)
	}
}

// A provider can answer with its whole response body; the stored reason is
// capped so one bad afternoon can't grow the database without bound.
func TestCleanupErrorIsCapped(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AppData", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)

	s, err := NewStore(500, 0)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer s.Close()

	long := strings.Repeat("x", maxCleanupError*3)
	if err := s.Append(Entry{ID: "e1", Raw: "ruwe tekst", CleanupError: long}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, _, err := s.Get("e1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.CleanupError) > maxCleanupError+4 {
		t.Fatalf("stored reason is %d bytes, want it capped near %d", len(got.CleanupError), maxCleanupError)
	}
}
