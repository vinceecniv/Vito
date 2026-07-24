package audio

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Kept recordings. A spool WAV is normally deleted the moment its text has been
// injected; when history.store_audio is on the file is moved here instead,
// named after the history entry it belongs to, so the UI can offer it as a
// download. Only the newest MaxRecordings are kept — this is a listen-back
// convenience, not an archive.
const MaxRecordings = 10

func RecordingsDir() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "vito", "recordings"), nil
}

// ValidRecordingID guards the filesystem against ids coming in over the API:
// only the hex ids history hands out may ever become a path.
func ValidRecordingID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return false
		}
	}
	return true
}

// SaveRecording moves a finished spool file to the recordings folder under the
// history entry's id, then prunes the oldest ones away. Spool and recordings
// live under the same cache directory, so this is a rename.
// keep names entry ids whose recording must never be pruned (starred favorites),
// regardless of age; they do not count against MaxRecordings.
func SaveRecording(spoolPath, id string, keep map[string]bool) (string, error) {
	if !ValidRecordingID(id) {
		return "", os.ErrInvalid
	}
	dir, err := RecordingsDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, id+".wav")
	if err := os.Rename(spoolPath, dst); err != nil {
		return "", err
	}
	pruneRecordings(MaxRecordings, keep)
	return dst, nil
}

// RecordingPath reports where a kept recording lives, if it still exists.
func RecordingPath(id string) (string, bool) {
	if !ValidRecordingID(id) {
		return "", false
	}
	dir, err := RecordingsDir()
	if err != nil {
		return "", false
	}
	p := filepath.Join(dir, id+".wav")
	if st, err := os.Stat(p); err != nil || st.IsDir() {
		return "", false
	}
	return p, true
}

// HaveRecordings reports which of the given entry ids have a kept recording,
// with a single directory read instead of a stat per entry.
func HaveRecordings(ids []string) map[string]bool {
	out := make(map[string]bool, len(ids))
	dir, err := RecordingsDir()
	if err != nil {
		return out
	}
	present := map[string]bool{}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range ents {
		if !e.IsDir() {
			present[strings.TrimSuffix(e.Name(), ".wav")] = true
		}
	}
	for _, id := range ids {
		if present[id] {
			out[id] = true
		}
	}
	return out
}

func RemoveRecording(id string) {
	if p, ok := RecordingPath(id); ok {
		_ = os.Remove(p)
	}
}

// RemoveAllRecordings wipes the folder. Called when history is cleared and when
// the setting is switched off — turning it off has to actually remove the audio.
func RemoveAllRecordings() error {
	dir, err := RecordingsDir()
	if err != nil {
		return err
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range ents {
		if !e.IsDir() {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
	return nil
}

// pruneRecordings keeps the newest max files by modification time, plus every
// file whose entry id is in keep (starred favorites), which are never removed and
// don't count towards max.
func pruneRecordings(max int, keep map[string]bool) {
	dir, err := RecordingsDir()
	if err != nil {
		return
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type rec struct {
		name string
		mod  int64
	}
	var files []rec
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		if keep[strings.TrimSuffix(e.Name(), ".wav")] {
			continue // a favorite's recording — kept regardless of age
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, rec{e.Name(), info.ModTime().UnixNano()})
	}
	if len(files) <= max {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod > files[j].mod })
	for _, f := range files[max:] {
		_ = os.Remove(filepath.Join(dir, f.name))
	}
}
