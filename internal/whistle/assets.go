package whistle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

// The engine and model are downloaded rather than built into Vito: most people
// on the desktop use a bigger model, the model can be updated on its own, and
// the web version fetches the same file. Both are pinned to a revision and a
// SHA-256 taken from Hugging Face's API; bumping either means re-pinning every
// digest below.
const (
	engineRev = "2ae11323dc000f5e70c49f7403efa6af12ba9e67" // Cactus-Compute/needle3
	modelRev  = "b358ddadd89b7a713b5aa131f23032d3cca1b251" // Cactus-Compute/whistle
)

type asset struct {
	url  string
	size int64
	sha  string
}

func engineURL(p string) string {
	return "https://huggingface.co/Cactus-Compute/needle3/resolve/" + engineRev + "/" + p
}

// engines are Cactus's prebuilt binaries per GOOS/GOARCH. There is none for
// Intel Macs.
var engines = map[string]asset{
	"windows/amd64": {engineURL("windows-x86_64/needle.exe"), 1563136, "e8863ca0c06a47d406777077f1ba728b58e57d77fedff252dbe6827d588acc8c"},
	"windows/arm64": {engineURL("windows-arm64/needle.exe"), 1352704, "8a333b4829f4a6f230e26ab1998c17ad442a97c650934448db1c63eed91b0cea"},
	"linux/amd64":   {engineURL("linux-x86_64/needle"), 1541680, "f38dc4b0345d66b4e385734ad0f12af43ac6e2cfa1752d0795af5c137200c8e4"},
	"linux/arm64":   {engineURL("linux-arm64/needle"), 1431816, "6fc25a97def475e1c7d1933a4a219d4e37076be77974e76dd331d9e0cd97f3cb"},
	"darwin/arm64":  {engineURL("macos-arm64/needle"), 1089896, "342fa2c6f140e702354a99c4201c9057535ec908eed35c7382e911a19d1d2724"},
}

var model = asset{
	"https://huggingface.co/Cactus-Compute/whistle/resolve/" + modelRev + "/whistle.cact",
	16919407, "b6e02f048568ac5d01a2042556c658061e699acbc0aa2a1439f52f3d461dffeb",
}

// Supported reports whether Cactus ships an engine for this platform.
func Supported() bool { _, ok := engines[runtime.GOOS+"/"+runtime.GOARCH]; return ok }

// Status is what the settings page shows.
type Status struct {
	Phase string `json:"phase"` // unsupported | absent | downloading | ready | error
	Done  int64  `json:"done,omitempty"`
	Total int64  `json:"total,omitempty"`
	Error string `json:"error,omitempty"`
}

// Manager keeps the engine and model in <UserCacheDir>/vito/whistle.
type Manager struct {
	log  *slog.Logger
	emit func(Status)
	dir  string

	mu     sync.Mutex
	st     Status
	cancel context.CancelFunc
}

// NewManager looks for an installed copy; emit receives every status change.
func NewManager(log *slog.Logger, emit func(Status)) *Manager {
	m := &Manager{log: log, emit: emit}
	if !Supported() {
		m.st = Status{Phase: "unsupported"}
		return m
	}
	base, err := os.UserCacheDir()
	if err != nil {
		m.st = Status{Phase: "error", Error: err.Error()}
		return m
	}
	m.dir = filepath.Join(base, "vito", "whistle")
	m.st = Status{Phase: "absent"}
	if m.installed() {
		m.st = Status{Phase: "ready"}
	}
	return m
}

func (m *Manager) binPath() string {
	name := "needle"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(m.dir, engineRev[:12], name)
}
func (m *Manager) modelPath() string { return filepath.Join(m.dir, "whistle-"+modelRev[:12]+".cact") }

// installed means both files are there and were verified when they arrived
// (the .ok markers), so a start-up doesn't hash 18 MB every time.
func (m *Manager) installed() bool {
	for _, p := range []string{m.binPath(), m.modelPath()} {
		if _, err := os.Stat(p + ".ok"); err != nil {
			return false
		}
	}
	return true
}

// Status returns the current state.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.st
}

func (m *Manager) set(st Status) {
	m.mu.Lock()
	m.st = st
	m.mu.Unlock()
	if m.emit != nil {
		m.emit(st)
	}
}

// Engine returns the engine when it is installed.
func (m *Manager) Engine() (Engine, bool) {
	if m.Status().Phase != "ready" {
		return Engine{}, false
	}
	return Engine{Bin: m.binPath(), Model: m.modelPath()}, true
}

// Install downloads what is missing, in the background.
func (m *Manager) Install() error {
	m.mu.Lock()
	switch m.st.Phase {
	case "unsupported":
		m.mu.Unlock()
		return fmt.Errorf("whistle: no engine for %s/%s", runtime.GOOS, runtime.GOARCH)
	case "downloading", "ready":
		m.mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.mu.Unlock()
	go m.install(ctx)
	return nil
}

func (m *Manager) install(ctx context.Context) {
	eng := engines[runtime.GOOS+"/"+runtime.GOARCH]
	total := eng.size + model.size
	var done int64
	m.set(Status{Phase: "downloading", Total: total})
	last := time.Now()
	progress := func(n int64) {
		done += n
		if time.Since(last) > 200*time.Millisecond {
			last = time.Now()
			m.set(Status{Phase: "downloading", Done: done, Total: total})
		}
	}
	client := &http.Client{Timeout: 10 * time.Minute}
	for _, it := range []struct {
		a    asset
		path string
	}{{eng, m.binPath()}, {model, m.modelPath()}} {
		if _, err := os.Stat(it.path + ".ok"); err == nil {
			progress(it.a.size)
			continue
		}
		if err := fetch(ctx, client, it.a, it.path, progress); err != nil {
			m.log.Warn("whistle download failed", "url", it.a.url, "err", err)
			m.set(Status{Phase: "error", Error: err.Error()})
			return
		}
	}
	m.log.Info("whistle installed", "dir", m.dir)
	m.set(Status{Phase: "ready"})
}

// Remove deletes the engine and model.
func (m *Manager) Remove() error {
	m.mu.Lock()
	if m.cancel != nil {
		m.cancel()
	}
	m.mu.Unlock()
	if m.dir == "" {
		return nil
	}
	if err := os.RemoveAll(m.dir); err != nil {
		return err
	}
	m.set(Status{Phase: "absent"})
	return nil
}

// fetch downloads a to dst through a temporary file, checks size and SHA-256,
// and leaves a .ok marker beside it once both match.
func fetch(ctx context.Context, client *http.Client, a asset, dst string, progress func(int64)) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download: %s", resp.Status)
	}
	tmp := dst + ".part"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	h := sha256.New()
	buf := make([]byte, 64<<10)
	var n int64
	for {
		k, rerr := resp.Body.Read(buf)
		if k > 0 {
			if _, werr := f.Write(buf[:k]); werr != nil {
				f.Close()
				return werr
			}
			h.Write(buf[:k])
			n += int64(k)
			progress(int64(k))
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			return rerr
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); n != a.size || got != a.sha {
		os.Remove(tmp)
		return fmt.Errorf("download: %s does not match its pinned digest", filepath.Base(dst))
	}
	if err := os.Rename(tmp, dst); err != nil {
		return err
	}
	return os.WriteFile(dst+".ok", []byte(a.sha+"\n"), 0o644)
}
