package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"vito/internal/longrec"
	"vito/internal/osfiles"
	"vito/internal/uitext"
)

// Each recording's files (Word, Markdown, text, subtitles, MP3) are kept up to
// date in a folder of its own under LongFolder (longrec.WriteFiles): a few
// seconds after the last change, so a burst of edits writes once.

const longFilesDelay = 2 * time.Second

type longFiles struct {
	mu     sync.Mutex
	timers map[string]*time.Timer
	root   string // the root last written to
}

// LongFolder is where the recordings' folders go: the setting, else
// Documents/Vito/Recordings (its last part in the interface's language).
func (d *Daemon) LongFolder() string {
	cfg := d.Config()
	if f := strings.TrimSpace(cfg.Long.Folder); f != "" {
		return f
	}
	return DefaultLongFolder(cfg.UI.Lang)
}

// DefaultLongFolder is the folder used when none is set.
func DefaultLongFolder(lang string) string {
	return filepath.Join(osfiles.Documents(), "Vito", longrec.SafeName(uitext.Translator(uitext.ResolveLang(lang))("Recordings")))
}

// scheduleLongFiles writes a session's folder shortly, once.
func (d *Daemon) scheduleLongFiles(id string) {
	l := d.long
	if l == nil || id == "" {
		return
	}
	f := &l.files
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.timers == nil {
		f.timers = map[string]*time.Timer{}
	}
	if t := f.timers[id]; t != nil {
		t.Reset(longFilesDelay)
		return
	}
	f.timers[id] = time.AfterFunc(longFilesDelay, func() {
		f.mu.Lock()
		delete(f.timers, id)
		f.mu.Unlock()
		d.writeLongFiles(id)
	})
}

// writeLongFiles brings a session's folder up to date now.
func (d *Daemon) writeLongFiles(id string) (longrec.FilesState, error) {
	l := d.long
	if l == nil {
		return longrec.FilesState{}, ErrNoLong
	}
	if _, err := l.store.Meta(id); err != nil {
		return longrec.FilesState{}, err // deleted meanwhile
	}
	before := l.store.Files(id)
	root := d.LongFolder()
	st, err := l.store.WriteFiles(id, root, uitext.Translator(uitext.ResolveLang(d.Config().UI.Lang))("with times"))
	if err != nil {
		d.log.Warn("long recording: files not written", "session", id, "folder", root, "err", err)
		return st, err
	}
	l.files.mu.Lock()
	l.files.root = root
	l.files.mu.Unlock()
	if st.Dir != before.Dir || strings.Join(st.Edited, "\n") != strings.Join(before.Edited, "\n") {
		d.emit(Event{Type: "long_files", Session: id})
	}
	return st, nil
}

// writeAllLongFiles brings every session's folder up to date: at start-up,
// and when the folder setting changed (the folders move along).
func (d *Daemon) writeAllLongFiles() {
	l := d.long
	if l == nil {
		return
	}
	list, _ := l.store.List()
	for _, m := range list {
		d.writeLongFiles(m.ID)
	}
}

// longConfigChanged follows a new folder setting.
func (d *Daemon) longConfigChanged() {
	l := d.long
	if l == nil {
		return
	}
	root := d.LongFolder()
	l.files.mu.Lock()
	changed := l.files.root != "" && l.files.root != root
	l.files.mu.Unlock()
	if changed {
		go d.writeAllLongFiles()
	}
	go d.pruneLongAudio() // a shorter keep applies at once
}

// OpenLongFolder writes a session's folder now and shows it in the file
// manager; it returns the folder.
func (d *Daemon) OpenLongFolder(id string) (string, error) {
	st, err := d.writeLongFiles(id)
	if err != nil {
		return "", err
	}
	return st.Dir, osfiles.OpenFolder(st.Dir)
}

// trashLongFiles moves a session's folder to the trash before the session
// goes. Only a folder Vito made for it (its name starts like FolderName's);
// without a trash it is left where it is.
func (d *Daemon) trashLongFiles(id string) {
	l := d.long
	if l == nil {
		return
	}
	l.files.mu.Lock()
	if t := l.files.timers[id]; t != nil {
		t.Stop()
		delete(l.files.timers, id)
	}
	l.files.mu.Unlock()
	dir := l.store.Files(id).Dir
	if dir == "" {
		return
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return
	}
	if m, err := l.store.Meta(id); err == nil && !strings.HasPrefix(filepath.Base(dir), m.Started.Local().Format("2006-01-02 15.04")) {
		return
	}
	if err := osfiles.Trash(dir); err != nil {
		if errors.Is(err, osfiles.ErrNoTrash) {
			d.log.Info("long recording: its folder stays (no trash there)", "dir", dir)
		} else {
			d.log.Warn("long recording: folder not moved to the trash", "dir", dir, "err", err)
		}
	}
}
