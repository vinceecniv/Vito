package history

import (
	"testing"
	"time"
)

func TestApps(t *testing.T) {
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
	for i, a := range []struct{ id, name string }{{"outlook", "Microsoft Outlook"}, {"slack", "Slack"}, {"outlook", "Microsoft Outlook"}, {"", ""}} {
		if err := s.Append(Entry{Timestamp: now.Add(time.Duration(i) * time.Minute), Raw: "hallo", Source: "stream", App: a.name, AppID: a.id}); err != nil {
			t.Fatal(err)
		}
	}
	apps, err := s.Apps()
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 2 || apps[0].ID != "outlook" || apps[0].Count != 2 || apps[0].Name != "Microsoft Outlook" || apps[1].ID != "slack" {
		t.Fatalf("Apps = %+v", apps)
	}
	list, _ := s.List("", false, 10, 0)
	if list[1].App != "Microsoft Outlook" || list[1].AppID != "outlook" {
		t.Errorf("entry app = %q/%q", list[1].App, list[1].AppID)
	}
	in, err := s.Insights(time.Time{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(in.Apps) != 2 || in.Apps[0].ID != "outlook" || in.Apps[0].Count != 2 || in.Apps[1].Name != "Slack" {
		t.Errorf("Insights.Apps = %+v", in.Apps)
	}
	st, err := s.AchievementInputs(40)
	if err != nil {
		t.Fatal(err)
	}
	if st.Apps != 1 { // Slack has one dictation: not yet counted
		t.Errorf("AchievementStats.Apps = %d, want 1", st.Apps)
	}
}
