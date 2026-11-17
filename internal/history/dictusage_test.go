package history

import (
	"testing"
	"time"
)

func TestDictUsage(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AppData", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	s, err := NewStore(500, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	t0 := time.UnixMilli(1_000_000)
	if err = s.RecordDictUse("correction", []string{"Chat GPT", "git hub", "chat gpt"}, t0); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordDictUse("snippet", []string{"mijn LinkedIn"}, t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	u, err := s.DictUsage()
	if err != nil {
		t.Fatal(err)
	}
	if got := u["correction"]["chat gpt"]; got.Uses != 2 || got.LastAt != t0.UnixMilli() {
		t.Errorf("chat gpt: %+v", got)
	}
	if got := u["snippet"]["mijn linkedin"]; got.Uses != 1 || got.LastAt != t0.Add(time.Minute).UnixMilli() {
		t.Errorf("snippet: %+v", got)
	}
}
