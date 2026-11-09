package longrec

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"vito/internal/stt"
)

// fakeStream hears "words" for each loud chunk it is sent.
type fakeStream struct {
	mu    sync.Mutex
	words []string
	fail  bool
	on    func(string)
}

func (f *fakeStream) Send(pcm []byte) {
	if peak(pcm) < speechGate {
		return
	}
	f.mu.Lock()
	f.words = append(f.words, "word")
	p := strings.Join(f.words, " ")
	f.mu.Unlock()
	if f.on != nil {
		f.on(p)
	}
}
func (f *fakeStream) Finish(context.Context) (string, error) {
	if f.fail {
		return "", errors.New("socket closed")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.words, " ") + ".", nil
}
func (f *fakeStream) Language() string { return "" }
func (f *fakeStream) Abort()           {}

type fakeFile struct{ calls int }

func (f *fakeFile) TranscribeFile(_ context.Context, path string) (string, error) {
	f.calls++
	if wavSeconds(path) <= 0 {
		return "", errors.New("empty")
	}
	return "from the file.", nil
}

// chunk is 100 ms of PCM, loud or quiet.
func chunk(loud bool) []byte {
	b := make([]byte, bytesPerSec/10)
	if loud {
		for i := 0; i+1 < len(b); i += 2 {
			binary.LittleEndian.PutUint16(b[i:], uint16(int16(8000)))
		}
	}
	return b
}

func shortBlocks(t *testing.T) {
	oldMin, oldMax := minBlock, maxBlock
	minBlock, maxBlock = 2, 5
	t.Cleanup(func() { minBlock, maxBlock = oldMin, oldMax })
}

func TestRecorderCutsAtPausesAndLogsBlocks(t *testing.T) {
	shortBlocks(t)
	store, _ := NewStore(t.TempDir())
	var streams []*fakeStream
	var mu sync.Mutex
	r, err := Start(Options{Store: store, NewStream: func(on func(string)) stt.Streamer {
		mu.Lock()
		defer mu.Unlock()
		s := &fakeStream{on: on}
		streams = append(streams, s)
		return s
	}}, "Test")
	if err != nil {
		t.Fatal(err)
	}
	// 3 s of speech, 1 s quiet (a pause after minBlock: cut), 3 s of speech.
	for i := 0; i < 30; i++ {
		r.Write(chunk(true))
	}
	for i := 0; i < 10; i++ {
		r.Write(chunk(false))
	}
	for i := 0; i < 30; i++ {
		r.Write(chunk(true))
	}
	if _, err := r.Mark(); err != nil {
		t.Fatal(err)
	}
	m, err := r.Stop()
	if err != nil {
		t.Fatal(err)
	}
	if m.State != StateDone || m.Seconds < 6.9 || m.Seconds > 7.1 {
		t.Fatalf("meta = %+v", m)
	}
	blocks, _ := store.Blocks(m.ID)
	if len(blocks) != 2 {
		t.Fatalf("blocks = %+v, want 2 (cut at the pause)", blocks)
	}
	if blocks[0].End != blocks[1].Start {
		t.Fatalf("a gap between blocks: %+v", blocks)
	}
	if got := wavSeconds(store.AudioPath(m.ID)); got < 6.9 {
		t.Fatalf("audio %.1f s", got)
	}
	d, err := store.Doc(m.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Marks) != 1 || d.Marks[0].Text == "" {
		t.Fatalf("marks = %+v", d.Marks)
	}
}

func TestRecorderFallsBackToTheFile(t *testing.T) {
	shortBlocks(t)
	store, _ := NewStore(t.TempDir())
	file := &fakeFile{}
	r, _ := Start(Options{Store: store, File: file, NewStream: func(on func(string)) stt.Streamer {
		return &fakeStream{fail: true}
	}}, "")
	for i := 0; i < 20; i++ {
		r.Write(chunk(true))
	}
	m, _ := r.Stop()
	blocks, _ := store.Blocks(m.ID)
	if len(blocks) != 1 || blocks[0].Text != "from the file." || file.calls != 1 {
		t.Fatalf("blocks = %+v, calls %d", blocks, file.calls)
	}
}

func TestRecoverAndRestore(t *testing.T) {
	shortBlocks(t)
	store, _ := NewStore(t.TempDir())
	r, _ := Start(Options{Store: store, NewStream: func(on func(string)) stt.Streamer { return &fakeStream{} }}, "")
	for i := 0; i < 80; i++ { // 8 s: a block of 5 s is cut and logged, 3 s are not
		r.Write(chunk(true))
	}
	time.Sleep(50 * time.Millisecond) // the cut block's finish runs in the background
	r.Abort()                         // the daemon dies: no Stop
	id := r.ID()
	// …and before its last header patch: the header announces too little.
	f, _ := os.OpenFile(store.AudioPath(id), os.O_RDWR, 0)
	f.WriteAt(wavHeaderBytes(bytesPerSec), 0)
	f.Close()

	got := store.Recover()
	if len(got) != 1 || got[0].State != StateInterrupted || got[0].Seconds < 7.9 {
		t.Fatalf("recover = %+v", got)
	}
	file := &fakeFile{}
	var added []Block
	m, err := Restore(store, id, file, func(b Block) { added = append(added, b) })
	if err != nil {
		t.Fatal(err)
	}
	if m.State != StateDone || len(added) != 1 || added[0].Start < 4.9 || added[0].End < 7.9 {
		t.Fatalf("restore: %+v added %+v", m, added)
	}
	if b, _ := os.ReadFile(store.AudioPath(id)); binary.LittleEndian.Uint32(b[40:]) != uint32(len(b)-44) {
		t.Fatal("header not repaired")
	}
}

func TestDocHeadersMarksAndTitles(t *testing.T) {
	store, _ := NewStore(t.TempDir())
	id := newID()
	store.create(Meta{ID: id, State: StateDone, Seconds: 20})
	store.AddBlock(id, Block{Index: 0, Start: 0, End: 10, Text: "eh first point. second point"})
	store.AddBlock(id, Block{Index: 1, Start: 10, End: 20, Text: "third point. fourth point."})
	store.AddClean(id, Clean{Index: 0, Text: "First point. Second point."})
	h, _ := store.AddEvent(id, Event{Type: "header", At: 10})
	store.AddEvent(id, Event{Type: "title", Of: h.ID, Title: "Proposed", Auto: true})
	mk, _ := store.AddEvent(id, Event{Type: "mark", At: 19})
	store.AddEvent(id, Event{Type: "note", Of: mk.ID, Text: "ask"})

	d, err := store.Doc(id, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Items) != 3 || d.Items[0].Kind != "para" || d.Items[1].Kind != "header" || d.Items[1].Title != "Proposed" {
		t.Fatalf("items = %+v", d.Items)
	}
	if d.Items[0].Sentences[0].Text != "First point." {
		t.Fatalf("cleaned text not used: %+v", d.Items[0])
	}
	last := d.Items[2].Sentences
	if !last[1].Marked || len(d.Marks) != 1 || d.Marks[0].Note != "ask" {
		t.Fatalf("mark: %+v %+v", last, d.Marks)
	}

	// A typed title wins over the proposal, also when one comes later.
	store.AddEvent(id, Event{Type: "title", Of: h.ID, Title: "Mine"})
	store.AddEvent(id, Event{Type: "title", Of: h.ID, Title: "Later proposal", Auto: true})
	d, _ = store.Doc(id, false)
	if d.Items[1].Title != "Mine" || d.Items[1].Auto {
		t.Fatalf("title = %+v", d.Items[1])
	}
	raw, _ := store.Doc(id, true)
	if raw.Items[0].Sentences[0].Text != "eh first point." {
		t.Fatalf("raw view: %+v", raw.Items[0])
	}
	if md := d.Markdown("T", "today"); !strings.Contains(md, "## Mine") || !strings.Contains(md, "**fourth point.**") {
		t.Fatalf("markdown:\n%s", md)
	}
	if srt := d.Subtitles(false); !strings.Contains(srt, "00:00:10,000 --> ") {
		t.Fatalf("srt:\n%s", srt)
	}
}

func TestSentences(t *testing.T) {
	got := sentences(`Hij zei: "Ja." Toen ging hij. Versie 2.5 kwam?Nee… klaar`)
	want := []string{`Hij zei: "Ja."`, "Toen ging hij.", "Versie 2.5 kwam?Nee…", "klaar"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q", got)
	}
}
