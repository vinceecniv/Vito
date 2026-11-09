package longrec

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Every session also lives as ordinary files in a folder of its own, under a
// root the user can see (by default Documents/Vito/Recordings): the text as
// Word, Markdown and plain text, subtitles, and the audio once it is an MP3.
// Vito keeps them up to date after every change. What it wrote last is
// remembered per file (files.json in the session); a file that no longer
// matches was changed by the user and is left alone — Vito's new version goes
// beside it as "<name> (Vito).<ext>".

// FilesState is what Vito last wrote into a session's folder.
type FilesState struct {
	Dir    string            `json:"dir"`
	Hashes map[string]string `json:"hashes,omitempty"` // file name → sha256 of what Vito wrote
	Audio  *audioCopy        `json:"audio,omitempty"`
	// Edited are the files the user changed; Vito's version is beside each.
	Edited []string `json:"edited,omitempty"`
}

type audioCopy struct {
	Name    string    `json:"name"`
	SrcSize int64     `json:"src_size"`
	SrcMod  time.Time `json:"src_mod"`
	Size    int64     `json:"size"`
	Mod     time.Time `json:"mod"`
}

// filesMu keeps writers of the folders apart.
var filesMu sync.Mutex

// SafeName keeps a title usable as a file name everywhere.
func SafeName(title string) string {
	var b strings.Builder
	for _, r := range title {
		switch {
		case strings.ContainsRune(`<>:"/\|?*`, r) || r < 32:
			b.WriteRune('-')
		default:
			b.WriteRune(r)
		}
	}
	s := strings.TrimSpace(clip(b.String(), 80))
	s = strings.TrimRight(s, ". ") // Windows drops trailing dots and spaces
	if s == "" {
		s = "Vito"
	}
	return s
}

// DocTitle is the title a session's files carry, and when it was started.
func DocTitle(m Meta) (title, when string) {
	when = m.Started.Local().Format("2006-01-02 15:04")
	title = strings.TrimSpace(m.Title)
	if title == "" {
		title = "Vito " + when
	}
	return title, when
}

// FolderName is a session's folder: date and time first, so they sort, and
// two recordings with one title never share a folder.
func FolderName(m Meta) string {
	title := strings.TrimSpace(m.Title)
	if title == "" {
		title = "Vito"
	}
	return m.Started.Local().Format("2006-01-02 15.04") + " " + SafeName(title)
}

// Files is the session's folder state ("" Dir before it was first written).
func (s *Store) Files(id string) FilesState {
	var st FilesState
	if b, err := os.ReadFile(s.path(id, "files.json")); err == nil {
		_ = json.Unmarshal(b, &st)
	}
	return st
}

func (s *Store) saveFiles(id string, st FilesState) error {
	b, err := json.MarshalIndent(st, "", " ")
	if err != nil {
		return err
	}
	return writeAtomic(s.path(id, "files.json"), b)
}

// WriteFiles brings the session's folder under root up to date. timed is
// the name suffix of the Word file with times ("with times", translated);
// the plain one carries none.
func (s *Store) WriteFiles(id, root, timed string) (FilesState, error) {
	filesMu.Lock()
	defer filesMu.Unlock()
	if strings.TrimSpace(root) == "" {
		return FilesState{}, errors.New("no folder for the recordings")
	}
	m, err := s.Meta(id)
	if err != nil {
		return FilesState{}, err
	}
	d, err := s.Doc(id, false)
	if err != nil {
		return FilesState{}, err
	}
	st := s.Files(id)
	if st.Hashes == nil {
		st.Hashes = map[string]string{}
	}
	dir := filepath.Join(root, FolderName(m))

	// A new title or another root: the folder moves along. When it cannot
	// (another drive, a file open), the old one stays and a new one is
	// written from scratch.
	if st.Dir != "" && st.Dir != dir {
		moved := false
		if isDir(st.Dir) && !exists(dir) {
			if os.MkdirAll(filepath.Dir(dir), 0o755) == nil && os.Rename(st.Dir, dir) == nil {
				moved = true
			}
		}
		if !moved {
			st = FilesState{Hashes: map[string]string{}}
		}
	}
	st.Dir = dir
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return st, err
	}

	title, when := DocTitle(m)
	base := SafeName(title)
	docx, err := d.DOCX(title, when, false)
	if err != nil {
		return st, err
	}
	docxTimed, err := d.DOCX(title, when, true)
	if err != nil {
		return st, err
	}
	if timed = SafeName(timed); timed == "Vito" || timed == "" {
		timed = "with times"
	}
	out := map[string][]byte{
		base + ".docx":                 docx,
		base + " (" + timed + ").docx": docxTimed,
		base + ".md":                   []byte(d.Markdown(title, when)),
		base + ".txt":                  []byte(title + "\n\n" + d.Text() + "\n"),
		base + ".srt":                  []byte(d.Subtitles(false)),
		base + ".vtt":                  []byte(d.Subtitles(true)),
	}

	// Files under an old name (the title changed): gone when still Vito's,
	// kept (and no longer followed) when the user changed them.
	for name, h := range st.Hashes {
		if _, ok := out[name]; ok {
			continue
		}
		p := filepath.Join(dir, name)
		if cur, ok := hashFile(p); ok && cur == h {
			os.Remove(p)
		}
		os.Remove(filepath.Join(dir, besideName(name)))
		delete(st.Hashes, name)
	}

	var edited []string
	for name, data := range out {
		p := filepath.Join(dir, name)
		want := hashBytes(data)
		cur, ok := hashFile(p)
		rec, known := st.Hashes[name]
		switch {
		case !ok || cur == want || (known && cur == rec):
			// Missing, the same, or still what Vito wrote: (re)write it.
			if cur != want || !ok {
				if err := writeAtomic(p, data); err != nil {
					return st, err
				}
			}
			st.Hashes[name] = want
			os.Remove(filepath.Join(dir, besideName(name)))
		default:
			// Changed by the user: theirs stays, Vito's goes beside it.
			edited = append(edited, name)
			bp := filepath.Join(dir, besideName(name))
			if bh, ok := hashFile(bp); !ok || bh != want {
				if err := writeAtomic(bp, data); err != nil {
					return st, err
				}
			}
		}
	}

	// The audio, once it is the MP3 (a WAV only exists while recording).
	// Audio removed by the setting: Vito's own copy goes too.
	if s.AudioBytes(id) == 0 && st.Audio != nil {
		p := filepath.Join(dir, st.Audio.Name)
		if fi, err := os.Stat(p); err == nil && fi.Size() == st.Audio.Size && fi.ModTime().Equal(st.Audio.Mod) {
			os.Remove(p)
		}
		os.Remove(filepath.Join(dir, besideName(st.Audio.Name)))
		st.Audio = nil
	}
	if s.AudioType(id) == "mp3" {
		if e, err := s.copyAudio(id, dir, base+".mp3", st.Audio); err == nil {
			if e.edited {
				edited = append(edited, e.c.Name)
			}
			st.Audio = e.c
		} else {
			return st, err
		}
	}
	slices.Sort(edited)
	st.Edited = edited
	return st, s.saveFiles(id, st)
}

type audioResult struct {
	c      *audioCopy
	edited bool
}

// copyAudio copies the session's MP3 to dir/name when it changed since the
// last copy, or beside it when the user replaced the copy.
func (s *Store) copyAudio(id, dir, name string, last *audioCopy) (audioResult, error) {
	src := s.mp3Path(id)
	si, err := os.Stat(src)
	if err != nil {
		return audioResult{c: last}, nil
	}
	if last != nil && last.Name != name {
		// Renamed with the title: take the old copy along if it is Vito's.
		op := filepath.Join(dir, last.Name)
		if fi, err := os.Stat(op); err == nil && fi.Size() == last.Size && fi.ModTime().Equal(last.Mod) && !exists(filepath.Join(dir, name)) {
			if os.Rename(op, filepath.Join(dir, name)) == nil {
				if nfi, err := os.Stat(filepath.Join(dir, name)); err == nil {
					c := *last
					c.Name, c.Mod = name, nfi.ModTime()
					last = &c
				}
			}
		} else {
			last = nil
		}
	}
	dst := filepath.Join(dir, name)
	di, derr := os.Stat(dst)
	ours := derr == nil && last != nil && di.Size() == last.Size && di.ModTime().Equal(last.Mod)
	fresh := last != nil && last.SrcSize == si.Size() && last.SrcMod.Equal(si.ModTime())
	switch {
	case ours && fresh:
		return audioResult{c: last}, nil
	case derr != nil || ours:
		// Missing, or Vito's own but out of date.
		os.Remove(filepath.Join(dir, besideName(name)))
		return s.writeAudio(src, si, dst, name, false)
	default:
		// The user's: Vito's copy beside it.
		bp := filepath.Join(dir, besideName(name))
		if bi, err := os.Stat(bp); err == nil && last != nil && fresh && bi.Size() == si.Size() {
			return audioResult{c: last, edited: true}, nil
		}
		r, err := s.writeAudio(src, si, bp, name, true)
		return r, err
	}
}

func (s *Store) writeAudio(src string, si os.FileInfo, dst, name string, edited bool) (audioResult, error) {
	in, err := os.Open(src)
	if err != nil {
		return audioResult{}, err
	}
	defer in.Close()
	tmp := dst + ".part"
	out, err := os.Create(tmp)
	if err != nil {
		return audioResult{}, err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return audioResult{}, err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return audioResult{}, err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return audioResult{}, err
	}
	c := &audioCopy{Name: name, SrcSize: si.Size(), SrcMod: si.ModTime()}
	if edited {
		// The followed file stays the user's; remember the source only.
		return audioResult{c: c, edited: true}, nil
	}
	if fi, err := os.Stat(dst); err == nil {
		c.Size, c.Mod = fi.Size(), fi.ModTime()
	}
	return audioResult{c: c}, nil
}

// besideName is where Vito's version of a file goes when the user changed
// theirs: "Notes.md" → "Notes (Vito).md".
func besideName(name string) string {
	ext := filepath.Ext(name)
	return strings.TrimSuffix(name, ext) + " (Vito)" + ext
}

func hashBytes(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func hashFile(p string) (string, bool) {
	f, err := os.Open(p)
	if err != nil {
		return "", false
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", false
	}
	return hex.EncodeToString(h.Sum(nil)), true
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

func isDir(p string) bool { fi, err := os.Stat(p); return err == nil && fi.IsDir() }

// writeAtomic writes beside the target and renames it into place.
func writeAtomic(p string, b []byte) error {
	tmp := p + ".part"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
