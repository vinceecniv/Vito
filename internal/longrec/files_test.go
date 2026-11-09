package longrec

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The folder is written, kept up to date, never overwrites what the user
// changed, and follows a new title.
func TestWriteFiles(t *testing.T) {
	store, _ := NewStore(t.TempDir())
	root := t.TempDir()
	id := "20261109-120000-cafe0003"
	started := time.Date(2026, 11, 9, 12, 0, 0, 0, time.Local)
	store.create(Meta{ID: id, State: StateDone, Title: "Overleg", Started: started})
	store.AddBlock(id, Block{Index: 0, Start: 0, End: 10, Text: "First point. Second point."})

	st, err := store.WriteFiles(id, root, "with times")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "2026-11-09 12.00 Overleg")
	if st.Dir != dir {
		t.Fatalf("dir %q", st.Dir)
	}
	for _, ext := range []string{".docx", " (with times).docx", ".md", ".txt", ".srt", ".vtt"} {
		if _, err := os.Stat(filepath.Join(dir, "Overleg"+ext)); err != nil {
			t.Fatal(err)
		}
	}
	// The Word file is a zip with the text in it.
	b, _ := os.ReadFile(filepath.Join(dir, "Overleg.docx"))
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			r, _ := f.Open()
			var buf bytes.Buffer
			buf.ReadFrom(r)
			found = strings.Contains(buf.String(), "Second point.")
		}
	}
	if !found {
		t.Fatal("text not in the docx")
	}

	// New text: Vito's own files follow.
	store.AddBlock(id, Block{Index: 1, Start: 10, End: 20, Text: "Third point."})
	store.WriteFiles(id, root, "with times")
	if md, _ := os.ReadFile(filepath.Join(dir, "Overleg.md")); !strings.Contains(string(md), "Third point.") {
		t.Fatal("md not updated")
	}

	// The user edits the Markdown: it stays, Vito's goes beside it.
	os.WriteFile(filepath.Join(dir, "Overleg.md"), []byte("mine"), 0o644)
	store.AddBlock(id, Block{Index: 2, Start: 20, End: 30, Text: "Fourth point."})
	st, _ = store.WriteFiles(id, root, "with times")
	if md, _ := os.ReadFile(filepath.Join(dir, "Overleg.md")); string(md) != "mine" {
		t.Fatal("the user's file was overwritten")
	}
	if md, _ := os.ReadFile(filepath.Join(dir, "Overleg (Vito).md")); !strings.Contains(string(md), "Fourth point.") {
		t.Fatal("Vito's version is not beside it")
	}
	if len(st.Edited) != 1 || st.Edited[0] != "Overleg.md" {
		t.Fatalf("edited %v", st.Edited)
	}

	// A new title: the folder and Vito's files are renamed, the user's file
	// stays as it is.
	store.UpdateMeta(id, func(m *Meta) { m.Title = "Teamoverleg" })
	st, err = store.WriteFiles(id, root, "with times")
	if err != nil {
		t.Fatal(err)
	}
	ndir := filepath.Join(root, "2026-11-09 12.00 Teamoverleg")
	if st.Dir != ndir {
		t.Fatalf("dir %q", st.Dir)
	}
	if _, err := os.Stat(filepath.Join(ndir, "Teamoverleg.docx")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(ndir, "Overleg.docx")); err == nil {
		t.Fatal("old docx left")
	}
	if md, _ := os.ReadFile(filepath.Join(ndir, "Overleg.md")); string(md) != "mine" {
		t.Fatal("the user's file went")
	}
	if _, err := os.Stat(filepath.Join(ndir, "Overleg (Vito).md")); err == nil {
		t.Fatal("stale Vito copy left")
	}
	if len(st.Edited) != 0 {
		t.Fatalf("edited %v", st.Edited)
	}
}

// The MP3 is copied once, again when it changes, and beside a copy the user
// replaced.
func TestWriteFilesAudio(t *testing.T) {
	store, _ := NewStore(t.TempDir())
	root := t.TempDir()
	id := "20261109-120000-cafe0004"
	store.create(Meta{ID: id, State: StateDone, Title: "Les", Started: time.Date(2026, 11, 9, 9, 0, 0, 0, time.Local)})
	os.WriteFile(store.mp3Path(id), []byte("one"), 0o644)
	st, err := store.WriteFiles(id, root, "with times")
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(st.Dir, "Les.mp3")
	if b, _ := os.ReadFile(dst); string(b) != "one" {
		t.Fatal("not copied")
	}
	os.WriteFile(store.mp3Path(id), []byte("two!"), 0o644)
	store.WriteFiles(id, root, "with times")
	if b, _ := os.ReadFile(dst); string(b) != "two!" {
		t.Fatal("not updated")
	}
	os.WriteFile(dst, []byte("user's"), 0o644)
	os.WriteFile(store.mp3Path(id), []byte("three"), 0o644)
	st, _ = store.WriteFiles(id, root, "with times")
	if b, _ := os.ReadFile(dst); string(b) != "user's" {
		t.Fatal("the user's copy was overwritten")
	}
	if b, _ := os.ReadFile(filepath.Join(st.Dir, "Les (Vito).mp3")); string(b) != "three" {
		t.Fatal("Vito's copy not beside it")
	}
	if len(st.Edited) != 1 {
		t.Fatalf("edited %v", st.Edited)
	}
}
