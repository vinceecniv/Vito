package longrec

import (
	"strings"
	"testing"
	"time"

	"vito/internal/stt"
)

// Recognised speakers replace the blocks up to where the audio ended then;
// later blocks follow; names, labels and cleaned turns show everywhere.
func TestSpeakersDoc(t *testing.T) {
	store, _ := NewStore(t.TempDir())
	id := "20261109-120000-cafe0007"
	store.create(Meta{ID: id, State: StateDone, Title: "Overleg", Started: time.Date(2026, 11, 9, 12, 0, 0, 0, time.Local), Seconds: 30})
	store.AddBlock(id, Block{Index: 0, Start: 0, End: 20, Text: "goedemorgen allemaal hoe gaat het goed dank je"})
	store.AddBlock(id, Block{Index: 1, Start: 20, End: 30, Text: "dit kwam later."})
	err := store.SaveSpeakers(id, "soniox", 20, []stt.Turn{
		{Speaker: "1", Start: 0, End: 3, Text: "Goedemorgen allemaal, hoe gaat het?"},
		{Speaker: "2", Start: 3, End: 6, Text: "Goed, dank je."},
		{Speaker: "1", Start: 6, End: 9, Text: "Mooi."},
	})
	if err != nil {
		t.Fatal(err)
	}
	store.NameSpeaker(id, "2", "Anna")
	d, err := store.Doc(id, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Items) != 4 || d.Items[0].Speaker != "1" || d.Items[1].Name != "Anna" || d.Items[3].Speaker != "" {
		t.Fatalf("items %+v", d.Items)
	}
	if len(d.Speakers) != 2 || d.Speakers[1].Name != "Anna" || d.Speakers[0].Words != 6 {
		t.Fatalf("speakers %+v", d.Speakers)
	}
	d.SpeakerWord = "Spreker"
	txt := d.Text()
	if !strings.Contains(txt, "Spreker 1: Goedemorgen") || !strings.Contains(txt, "Anna: Goed, dank je.") || !strings.Contains(txt, "Spreker 1: Mooi.") || !strings.Contains(txt, "dit kwam later.") {
		t.Fatalf("text %q", txt)
	}
	// One chunk of turns and the block recorded after it, neither cleaned yet.
	if d.Blocks != 2 || d.Cleaned != 0 {
		t.Fatalf("blocks %d cleaned %d", d.Blocks, d.Cleaned)
	}

	// A cleaned chunk is read back by turn.
	sp := store.Speakers(id)
	got, err := parseChunk("[1] Goedemorgen allemaal. Hoe gaat het?\n[2] Goed, dank je.\n[1] Mooi!", sp.Turns)
	if err != nil {
		t.Fatal(err)
	}
	store.AddSpeakerClean(id, SpeakerClean{Chunk: 0, Turns: got})
	d, _ = store.Doc(id, false)
	if d.Items[0].Sentences[1].Text != "Hoe gaat het?" || d.Cleaned != 1 {
		t.Fatalf("cleaned %+v / %d", d.Items[0], d.Cleaned)
	}
	if raw, _ := store.Doc(id, true); raw.Items[2].Sentences[0].Text != "Mooi." {
		t.Fatalf("raw %+v", raw.Items[2])
	}

	// A model that merges or relabels turns is refused.
	if _, err := parseChunk("[1] Goedemorgen allemaal. Hoe gaat het? Goed, dank je.\n[1] Mooi!", sp.Turns); err == nil {
		t.Fatal("merged turns accepted")
	}
	if _, err := parseChunk("[1] a\n[1] b\n[1] c", sp.Turns); err == nil {
		t.Fatal("relabelled turn accepted")
	}

	// Removing them goes back to the blocks; names are kept for a new run.
	store.RemoveSpeakers(id)
	if d, _ := store.Doc(id, false); len(d.Speakers) != 0 || len(d.Items) != 2 {
		t.Fatalf("after removal %+v", d.Items)
	}
}

func TestSplitTurnsAndChunks(t *testing.T) {
	long := strings.Repeat("Dit is een zin van een redelijke lengte. ", 80) // ~3300 chars
	turns := splitTurns([]stt.Turn{{Speaker: "1", Start: 0, End: 100, Text: long}, {Speaker: "2", Start: 100, End: 101, Text: "Ja."}})
	if len(turns) < 3 || turns[len(turns)-1].Text != "Ja." || turns[0].End <= turns[0].Start || turns[1].Start != turns[0].End {
		t.Fatalf("%d turns, first %v–%v", len(turns), turns[0].Start, turns[0].End)
	}
	sp := &Speakers{Turns: turns}
	ch := sp.chunks()
	if ch[0][0] != 0 || ch[len(ch)-1][1] != len(turns) {
		t.Fatalf("chunks %v", ch)
	}
	for i := 1; i < len(ch); i++ {
		if ch[i][0] != ch[i-1][1] {
			t.Fatalf("gap in chunks %v", ch)
		}
	}
}
