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
	// Wider windows switch to coarser buckets, as fine as their length allows:
	// four weeks as days, three months and a year as weeks.
	for _, c := range []struct {
		days     int
		unit     string
		min, max int
	}{{28, "day", 28, 28}, {92, "week", 14, 15}, {365, "week", 53, 54}} {
		st, err := s.Stats(now, 40, c.days)
		if err != nil {
			t.Fatalf("Stats(%d): %v", c.days, err)
		}
		if st.SeriesUnit != c.unit || len(st.Week) < c.min || len(st.Week) > c.max {
			t.Fatalf("%dd series = %d bars of %q, want %d-%d of %q", c.days, len(st.Week), st.SeriesUnit, c.min, c.max, c.unit)
		}
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

func TestCapSparesTheLastThreeMonths(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AppData", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)

	s, err := NewStore(2, 0)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer s.Close()

	now := time.Now()
	add := func(raw string, ago time.Duration) {
		if err := s.Append(Entry{Timestamp: now.Add(-ago), Raw: raw, Cleaned: raw, Language: "nl"}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	// Two entries from before the protected window, then four recent ones: the
	// cap of two may drop the old pair, but none of the recent four.
	add("old one", 200*24*time.Hour)
	add("old two", 120*24*time.Hour)
	for i := 0; i < 4; i++ {
		add("recent", time.Duration(i+1)*24*time.Hour)
	}
	if n, err := s.Count("", false); err != nil || n != 4 {
		t.Fatalf("Count: got %d (err %v), want the 4 recent entries", n, err)
	}
	if n, _ := s.Count("old", false); n != 0 {
		t.Fatalf("old entries beyond the cap survived: %d", n)
	}
}

func TestInsights(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AppData", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)

	s, err := NewStore(500, 0)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer s.Close()

	// One day, 10:00 local.
	at := time.Date(2026, 9, 30, 10, 0, 0, 0, time.Local)
	add := func(e Entry) {
		e.Timestamp = at
		if err := s.Append(e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	add(Entry{Language: "nl", Raw: "een twee", Cleaned: "Een twee.", CleanupUsed: true, SttMS: 300, CleanupMS: 500, InjectedMS: 900})
	add(Entry{Language: "nl", Raw: "drie", Cleaned: "drie", CleanupUsed: true, SttMS: 200, CleanupMS: 400, InjectedMS: 700})
	add(Entry{Language: "en", Raw: "four", Cleaned: "four", CleanupError: "timeout", SttMS: 250, InjectedMS: 3000})

	day := time.Date(2026, 9, 30, 0, 0, 0, 0, time.Local)
	in, err := s.Insights(day, day)
	if err != nil {
		t.Fatalf("Insights: %v", err)
	}
	if in.Dictations != 3 || in.CleanupRuns != 2 || in.CleanupChanged != 1 || in.CleanupFailed != 1 {
		t.Fatalf("counts: %+v", in)
	}
	if in.LatencyMedianMS != 900 || in.LatencyP95MS != 3000 || in.SttMedianMS != 250 || in.CleanupMedianMS != 400 {
		t.Fatalf("timings: median %d p95 %d stt %d cleanup %d", in.LatencyMedianMS, in.LatencyP95MS, in.SttMedianMS, in.CleanupMedianMS)
	}
	if len(in.Languages) != 2 || in.Languages[0] != (LangCount{"nl", 2}) {
		t.Fatalf("languages: %+v", in.Languages)
	}
	// The day after holds nothing.
	if in, _ := s.Insights(day.AddDate(0, 0, 1), day.AddDate(0, 0, 1)); in.Dictations != 0 {
		t.Fatalf("next day: %d dictations", in.Dictations)
	}
}

func TestCalendar(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AppData", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)

	s, err := NewStore(500, 0)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer s.Close()

	now := time.Date(2026, 10, 2, 15, 0, 0, 0, time.Local) // a Friday
	if err := s.Append(Entry{Timestamp: now.Add(-24 * time.Hour), Raw: "een twee drie", Cleaned: "een twee drie"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	cal, err := s.Calendar(now)
	if err != nil {
		t.Fatalf("Calendar: %v", err)
	}
	// 25 whole weeks before this one, plus Monday to Friday of this week.
	if cal.Start != "2026-04-06" || len(cal.Words) != 25*7+5 {
		t.Fatalf("start %s, %d days", cal.Start, len(cal.Words))
	}
	if got := cal.Words[len(cal.Words)-2]; got != 3 {
		t.Fatalf("yesterday: %d words, want 3", got)
	}
}
