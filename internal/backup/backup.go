// Package backup bundles a user's whole Vito state — settings plus the history
// database (entries, day aggregates, achievements) — into one portable JSON
// file, and manages a small pool of automatic rolling copies on disk.
//
// Audio recordings are not included: they are large, separate WAV files, and a
// backup is about not losing your settings, history and progress.
package backup

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"vito/internal/config"
	"vito/internal/history"
)

// Format tags the file so an unrelated JSON can't be mistaken for a backup.
const Format = "vito-backup"

// EncFormat tags the encrypted envelope that wraps a backup on disk.
const EncFormat = "vito-backup-enc"

// CurrentVersion is the backup schema version. Bump it only on a breaking change
// to the shape below; readers accept anything they understand.
const CurrentVersion = 1

// backupKey obfuscates backups so their contents (which include API keys) aren't
// plain text sitting on disk. It is DERIVED FROM A FIXED STRING baked into every
// Vito build — so any Vito can read any Vito's backup. This is a deterrent
// against casual reading and accidental leaks, NOT real security: the key is
// public (the source is open). Treat a backup file as sensitive regardless.
var backupKey = sha256.Sum256([]byte("Vito local backup obfuscation key v1 — public by design, a deterrent not a secret"))

// envelope is what actually lands on disk: a small clear header plus the backup
// JSON encrypted with backupKey. created_at is left in the clear so the backups
// list can show a date without decrypting.
type envelope struct {
	Format    string `json:"format"`
	Version   int    `json:"version"`
	CreatedAt string `json:"created_at"`
	Enc       string `json:"enc"` // base64(nonce || AES-GCM ciphertext)
}

// Encrypt renders a backup as its on-disk encrypted envelope.
func Encrypt(f File) ([]byte, error) {
	plain, err := json.Marshal(f)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(backupKey[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	ct := gcm.Seal(nonce, nonce, plain, nil)
	env := envelope{Format: EncFormat, Version: CurrentVersion, CreatedAt: f.CreatedAt,
		Enc: base64.StdEncoding.EncodeToString(ct)}
	return json.MarshalIndent(env, "", "  ")
}

// File is a complete backup.
type File struct {
	Format     string             `json:"format"`
	Version    int                `json:"version"`
	CreatedAt  string             `json:"created_at"`  // RFC3339
	AppVersion string             `json:"app_version"` // the Vito that wrote it
	Config     *config.Config     `json:"config"`
	Data       history.BackupData `json:"data"`
}

// Make assembles a backup from the current settings and a database snapshot.
func Make(cfg config.Config, appVersion string, data history.BackupData, now time.Time) File {
	c := cfg
	return File{
		Format:     Format,
		Version:    CurrentVersion,
		CreatedAt:  now.Format(time.RFC3339),
		AppVersion: appVersion,
		Config:     &c,
		Data:       data,
	}
}

// Marshal renders the backup as indented JSON.
func (f File) Marshal() ([]byte, error) { return json.MarshalIndent(f, "", "  ") }

// Parse reads a backup file — decrypting the envelope when present, or reading a
// plain (older) backup as-is — and checks its shape. The caller applies it.
func Parse(raw []byte) (File, error) {
	var env envelope
	if json.Unmarshal(raw, &env) == nil && env.Format == EncFormat && env.Enc != "" {
		plain, err := decrypt(env.Enc)
		if err != nil {
			return File{}, err
		}
		return parseFile(plain)
	}
	return parseFile(raw)
}

func decrypt(enc string) ([]byte, error) {
	ct, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return nil, errors.New("this backup file is corrupt")
	}
	block, err := aes.NewCipher(backupKey[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(ct) < gcm.NonceSize() {
		return nil, errors.New("this backup file is corrupt")
	}
	plain, err := gcm.Open(nil, ct[:gcm.NonceSize()], ct[gcm.NonceSize():], nil)
	if err != nil {
		return nil, errors.New("could not read this backup (wrong or corrupt file)")
	}
	return plain, nil
}

func parseFile(raw []byte) (File, error) {
	var f File
	if err := json.Unmarshal(raw, &f); err != nil {
		return f, fmt.Errorf("not valid JSON: %w", err)
	}
	if f.Format != Format {
		return f, errors.New("this file is not a Vito backup")
	}
	if f.Version > CurrentVersion {
		return f, fmt.Errorf("this backup was made by a newer Vito (backup version %d)", f.Version)
	}
	if f.Config == nil {
		return f, errors.New("backup is missing its settings")
	}
	return f, nil
}

// DownloadName is the file name offered for a hand-exported backup.
func DownloadName(now time.Time) string {
	return "vito-backup-" + now.Format("2006-01-02-150405") + ".json"
}

// --- automatic rolling backups on disk ---

const autoPrefix = "vito-auto-"

// Dir is where automatic backups live: <config>/vito/backups.
func Dir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "vito", "backups"), nil
}

// Info describes one automatic backup on disk.
type Info struct {
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	CreatedAt string `json:"created_at"` // from the file's own stamp when readable
	ModMillis int64  `json:"mod_millis"` // file mtime, always present
}

// List returns the automatic backups, newest first.
func List() ([]Info, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Info
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, autoPrefix) || !strings.HasSuffix(name, ".json") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		info := Info{Name: name, Size: fi.Size(), ModMillis: fi.ModTime().UnixMilli()}
		// Best-effort read of the file's own creation stamp for display. Both the
		// encrypted envelope and a plain backup carry created_at at the top level.
		if raw, err := os.ReadFile(filepath.Join(dir, name)); err == nil {
			var probe struct {
				CreatedAt string `json:"created_at"`
			}
			if json.Unmarshal(raw, &probe) == nil {
				info.CreatedAt = probe.CreatedAt
			}
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ModMillis > out[j].ModMillis })
	return out, nil
}

// Read loads one automatic backup by name. The name is validated to a bare
// backup file so it can't escape the backups directory.
func Read(name string) (File, []byte, error) {
	if !validAutoName(name) {
		return File{}, nil, errors.New("invalid backup name")
	}
	dir, err := Dir()
	if err != nil {
		return File{}, nil, err
	}
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return File{}, nil, err
	}
	f, err := Parse(raw)
	return f, raw, err
}

// Write saves a backup into the automatic pool and prunes to keep newest.
func Write(f File, keep int, now time.Time) (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	data, err := Encrypt(f)
	if err != nil {
		return "", err
	}
	name := autoPrefix + now.Format("20060102-150405") + ".json"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return "", err
	}
	prune(dir, keep)
	return name, nil
}

// prune deletes all but the newest keep automatic backups.
func prune(dir string, keep int) {
	if keep <= 0 {
		return
	}
	list, err := List()
	if err != nil {
		return
	}
	for i, info := range list {
		if i < keep {
			continue
		}
		_ = os.Remove(filepath.Join(dir, info.Name))
	}
}

// NewestAge returns how long ago the most recent automatic backup was written,
// and whether there is one at all.
func NewestAge(now time.Time) (time.Duration, bool) {
	list, err := List()
	if err != nil || len(list) == 0 {
		return 0, false
	}
	return now.Sub(time.UnixMilli(list[0].ModMillis)), true
}

func validAutoName(name string) bool {
	return strings.HasPrefix(name, autoPrefix) && strings.HasSuffix(name, ".json") &&
		!strings.ContainsAny(name, `/\`) && name != autoPrefix+".json"
}
