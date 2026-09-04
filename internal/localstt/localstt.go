// Package localstt runs speech recognition on this machine, as a sidecar Vito
// installs and manages itself: mudler's parakeet.cpp `parakeet-server`, a
// single static binary that speaks the OpenAI transcription API, plus NVIDIA's
// Parakeet TDT 0.6B v3 model (25 European languages, Dutch included).
//
// The user sees one button — "Install, ≈ 1 GB, once" — and after that a
// provider that costs nothing and never sends audio anywhere. Everything else
// is this package's problem: picking the right build for the OS, verifying the
// downloads, keeping the process alive, and pointing the daemon's existing
// OpenAI-compatible client at it. That last part is why the transcription code
// itself does not change: the sidecar is just another endpoint on localhost.
//
// Why parakeet.cpp and not whisper.cpp: measured on a 12-core CPU, Whisper
// large-v3-turbo needs 4–9 s per dictation because its encoder always hears 30
// seconds; Parakeet needs about a second, and a quarter of that on a GPU. On a
// machine with a GPU, Whisper is still the better engine for Dutch with English
// jargon — that route stays open through the endpoint provider.
package localstt

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The pinned release. Bumping it means updating every hash below — which is
// the point: a build Vito runs on the user's machine is one Vito has seen.
const (
	Engine  = "parakeet.cpp"
	Version = "v0.5.0"
	release = "https://github.com/mudler/parakeet.cpp/releases/download/" + Version + "/"

	// The model: Parakeet TDT 0.6B v3 in q8_0, the quantisation that keeps the
	// accuracy of f16 at two thirds of the size.
	ModelName = "tdt-0.6b-v3-q8_0"
	modelFile = ModelName + ".gguf"
	modelURL  = "https://huggingface.co/mudler/parakeet-cpp-gguf/resolve/main/" + modelFile
	modelSHA  = "4d69a4a6683f4f2d952bad794c1357ca6eb628027695b4699c5a9ad4cd07d757"
	modelSize = 940663680
)

// Variants. "cpu" is the standard build everywhere — on Apple Silicon that
// build is the Metal one, so it already uses the GPU. "vulkan" is the GPU build
// for Windows and Linux: any recent GPU, integrated Intel and AMD included.
const (
	VariantCPU    = "cpu"
	VariantVulkan = "vulkan"
)

type asset struct {
	name string
	sha  string
	size int64
}

// assets is keyed by GOOS/GOARCH/variant. The hashes are the release's own
// asset digests, checked against the files before any of this was written.
var assets = map[string]asset{
	"windows/amd64/cpu":    {"parakeet-v0.5.0-bin-win-cpu-x64.zip", "df25af4095807d83957f6e135950120e7954fd2d4aca8ad0a5de248ada6287e0", 1421017},
	"windows/amd64/vulkan": {"parakeet-v0.5.0-bin-win-vulkan-x64.zip", "717c416fab299755e8140137e3a0115121ce1acb6379d13c60f2f0613f6c13a3", 35828324},
	"darwin/arm64/cpu":     {"parakeet-v0.5.0-bin-macos-metal-arm64.tar.gz", "819999afb74cfcbb2c8bf4cfff398ef35616c016bca1a311e0ef9660bb4708ee", 2128797},
	"darwin/amd64/cpu":     {"parakeet-v0.5.0-bin-macos-cpu-x64.tar.gz", "7acddf9cc47684f6e3fba54d50768f8b301947fcb6a9ec65c64443704cc4896f", 2159847},
	"linux/amd64/cpu":      {"parakeet-v0.5.0-bin-linux-cpu-x64.tar.gz", "636a9fc48ac023096037790f9b77d7e5043b200dd6399ec0438bd648c35d79b9", 2103219},
	"linux/amd64/vulkan":   {"parakeet-v0.5.0-bin-linux-vulkan-x64.tar.gz", "36c8d4b93594ec18928c9c76b02e04b2d738e859deda8b5e3944bb34fc0646eb", 36864577},
	"linux/arm64/cpu":      {"parakeet-v0.5.0-bin-linux-cpu-arm64.tar.gz", "a7c9064c64b84f6b041252d5d2334d4a47693636e9c7c6ab2c535fcef11cf88b", 1931531},
	"linux/arm64/vulkan":   {"parakeet-v0.5.0-bin-linux-vulkan-arm64.tar.gz", "b95483070eb87ed144b9f39826a69fb67ea516c68aacc4fcf13a121a746ad7e4", 29207915},
}

func assetFor(goos, goarch, variant string) (asset, bool) {
	if variant == "" {
		variant = VariantCPU
	}
	a, ok := assets[goos+"/"+goarch+"/"+variant]
	return a, ok
}

// Variants lists the builds available for this machine, the standard one first.
func Variants() []string {
	return variantsFor(runtime.GOOS, runtime.GOARCH)
}

func variantsFor(goos, goarch string) []string {
	var out []string
	for _, v := range []string{VariantCPU, VariantVulkan} {
		if _, ok := assetFor(goos, goarch, v); ok {
			out = append(out, v)
		}
	}
	return out
}

// Supported reports whether there is a build for this machine at all.
func Supported() bool { return len(Variants()) > 0 }

// Phase is where the sidecar is in its life.
const (
	PhaseAbsent      = "absent"      // not installed
	PhaseDownloading = "downloading" // fetching binary and model
	PhaseStopped     = "stopped"     // installed, not wanted (another provider is selected)
	PhaseStarting    = "starting"    // process up, model loading, warm-up pending
	PhaseRunning     = "running"     // answered the warm-up: ready for dictation
	PhaseError       = "error"       // see Error
)

// Status is what the settings page shows, and what the daemon consults.
type Status struct {
	Supported bool     `json:"supported"`
	Variants  []string `json:"variants"` // builds available for this machine
	Installed bool     `json:"installed"`
	Engine    string   `json:"engine"`
	Version   string   `json:"version"`
	Variant   string   `json:"variant,omitempty"` // the installed build
	Model     string   `json:"model"`
	ModelMB   int64    `json:"model_mb"` // what the install downloads, for the button
	Phase     string   `json:"phase"`    // see Phase*
	Frac      float64  `json:"frac"`     // download progress 0..1 while downloading
	Port      int      `json:"port,omitempty"`
	Error     string   `json:"error,omitempty"`
	LastMS    int64    `json:"last_ms,omitempty"` // the warm-up's, then each dictation's, latency
}

// Manager owns the sidecar: its files, its process and its state.
type Manager struct {
	log  *slog.Logger
	dir  string
	emit func(Status) // called on every change; must not block

	// command builds the process; a test swaps in a stand-in server.
	command func(bin, model string, port, threads int) *exec.Cmd
	http    *http.Client

	mu         sync.Mutex
	st         Status
	desired    bool   // the config selects this provider
	variant    string // the build the config asks for
	installing bool
	cmd        *exec.Cmd
	gen        int // bumped on every start/stop; a stale monitor sees a mismatch and stands down
	stderr     *tail
}

// Dir is where the sidecar lives: under the cache directory, beside the spool,
// because everything in it can be downloaded again.
func Dir() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "vito", "local-stt"), nil
}

// New reads the installed state; nothing is started until SetDesired says so.
func New(log *slog.Logger, emit func(Status)) *Manager {
	dir, err := Dir()
	if err != nil {
		log.Warn("local stt: no cache dir", "err", err)
	}
	m := &Manager{
		log:     log,
		dir:     dir,
		emit:    emit,
		command: defaultCommand,
		// Long: the very first request on a Vulkan build compiles shaders and
		// has been seen to take half a minute.
		http: &http.Client{Timeout: 3 * time.Minute},
	}
	m.st = Status{Supported: Supported(), Variants: Variants(), Engine: Engine, Version: Version, Model: ModelName, ModelMB: modelSize >> 20, Phase: PhaseAbsent}
	if v, ok := m.installedVariant(); ok {
		m.st.Installed, m.st.Variant, m.st.Phase = true, v, PhaseStopped
	}
	return m
}

func (m *Manager) binPath() string {
	name := "parakeet-server"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(m.dir, "bin", name)
}
func (m *Manager) modelPath() string   { return filepath.Join(m.dir, "models", modelFile) }
func (m *Manager) versionPath() string { return filepath.Join(m.dir, "bin", "VERSION") }
func (m *Manager) modelOKPath() string { return m.modelPath() + ".ok" }

// installedVariant reports the build on disk, when binary, model and the
// version marker all agree with this Vito. Anything else is "not installed":
// a partial download, an older release, a model that never passed its check.
func (m *Manager) installedVariant() (string, bool) {
	if m.dir == "" {
		return "", false
	}
	if _, err := os.Stat(m.binPath()); err != nil {
		return "", false
	}
	if _, err := os.Stat(m.modelOKPath()); err != nil {
		return "", false
	}
	if st, err := os.Stat(m.modelPath()); err != nil || st.Size() != modelSize {
		return "", false
	}
	b, err := os.ReadFile(m.versionPath())
	if err != nil {
		return "", false
	}
	f := strings.Fields(string(b))
	if len(f) != 2 || f[0] != Version {
		return "", false
	}
	return f[1], true
}

func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.st
}

// set applies a change to the status under the lock and broadcasts it.
func (m *Manager) set(fn func(*Status)) {
	m.mu.Lock()
	fn(&m.st)
	st := m.st
	m.mu.Unlock()
	if m.emit != nil {
		m.emit(st)
	}
}

// BaseURL is where the daemon's OpenAI-compatible client should send audio, or
// an error that says — in the user's language, it ends up in a notification —
// why there is nowhere to send it right now.
func (m *Manager) BaseURL() (string, error) {
	st := m.Status()
	switch st.Phase {
	case PhaseRunning:
		return fmt.Sprintf("http://127.0.0.1:%d/v1", st.Port), nil
	case PhaseStarting:
		return "", errors.New("lokale spraakherkenning start nog op")
	case PhaseDownloading:
		return "", errors.New("lokale spraakherkenning wordt nog geïnstalleerd")
	case PhaseError:
		return "", fmt.Errorf("lokale spraakherkenning: %s", st.Error)
	case PhaseAbsent:
		return "", errors.New("lokale spraakherkenning is niet geïnstalleerd")
	default:
		return "", errors.New("lokale spraakherkenning is niet gestart")
	}
}

// SetDesired tells the manager what the configuration wants: run (this
// provider is selected) or not, and with which build. It reconciles in the
// background and is safe to call on every config change.
func (m *Manager) SetDesired(run bool, variant string) {
	if variant == "" {
		variant = VariantCPU
	}
	m.mu.Lock()
	m.desired, m.variant = run, variant
	m.mu.Unlock()
	go m.reconcile()
}

// reconcile makes the process match desired: start it when wanted and
// installed, stop it when not wanted. A different installed variant than the
// config asks for is not a reason to stop — the user picks the build at install
// time, and until they reinstall the one on disk is the one that works.
func (m *Manager) reconcile() {
	m.mu.Lock()
	want := m.desired && m.st.Installed && !m.installing
	running := m.cmd != nil
	m.mu.Unlock()
	switch {
	case want && !running:
		m.start()
	case !want && running:
		m.stop(PhaseStopped)
	}
}

// Stop ends the process; for shutdown.
func (m *Manager) Stop() { m.stop(PhaseStopped) }

func (m *Manager) stop(phase string) {
	m.mu.Lock()
	cmd := m.cmd
	m.cmd = nil
	m.gen++
	m.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
	m.set(func(s *Status) {
		if s.Installed {
			s.Phase, s.Port = phase, 0
		}
	})
}

// threads is how many the server may use: half the logical CPUs, so a dictation
// does not freeze the machine it is typed into, within [2, 12] — measured, 12
// is where a desktop stops gaining and hyperthreads start costing.
func threads() int {
	n := runtime.NumCPU() / 2
	if n < 2 {
		n = 2
	}
	if n > 12 {
		n = 12
	}
	return n
}

// freePort asks the OS for a port nobody is listening on. The listener is
// closed again before the server binds it — a small race, but the alternative
// (a fixed port) collides with whatever the user runs there.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func defaultCommand(bin, model string, port, threads int) *exec.Cmd {
	return exec.Command(bin, "--model", model, "--host", "127.0.0.1", "--port", strconv.Itoa(port), "--threads", strconv.Itoa(threads))
}

// start launches the server and, in the background, waits for it to answer.
func (m *Manager) start() {
	port, err := freePort()
	if err != nil {
		m.set(func(s *Status) { s.Phase, s.Error = PhaseError, "geen vrije poort: "+err.Error() })
		return
	}
	cmd := m.command(m.binPath(), m.modelPath(), port, threads())
	tl := &tail{}
	cmd.Stdout = io.Discard
	cmd.Stderr = tl
	prepare(cmd)
	if err := cmd.Start(); err != nil {
		m.set(func(s *Status) { s.Phase, s.Error = PhaseError, "starten mislukt: "+err.Error() })
		return
	}
	tieToParent(cmd)
	m.mu.Lock()
	m.cmd, m.stderr = cmd, tl
	m.gen++
	gen := m.gen
	m.mu.Unlock()
	m.set(func(s *Status) { s.Phase, s.Port, s.Error = PhaseStarting, port, "" })
	m.log.Info("local stt: server started", "pid", cmd.Process.Pid, "port", port, "threads", threads())

	go m.monitor(cmd, gen)
	go m.await(gen, port)
}

// monitor notices the process ending. Expected (stop() took it down) or not,
// in which case it is reported and — while still wanted — restarted, with a
// pause that grows so a build that cannot run here does not spin.
func (m *Manager) monitor(cmd *exec.Cmd, gen int) {
	err := cmd.Wait()
	m.mu.Lock()
	ours := m.cmd == cmd && m.gen == gen
	if ours {
		m.cmd = nil
	}
	desired := m.desired
	msg := ""
	if m.stderr != nil {
		msg = m.stderr.String()
	}
	m.mu.Unlock()
	if !ours {
		return // stop() already accounted for it
	}
	reason := "exited"
	if err != nil {
		reason = err.Error()
	}
	if msg != "" {
		reason += " — " + msg
	}
	m.log.Warn("local stt: server died", "reason", reason)
	m.set(func(s *Status) { s.Phase, s.Port, s.Error = PhaseError, 0, reason })
	if desired {
		time.Sleep(5 * time.Second)
		m.reconcile()
	}
}

// await polls until the port accepts connections, then sends one second of
// silence as a warm-up: that is what loads the model into memory (and, on a
// Vulkan build, compiles the shaders), so the first real dictation is not the
// one to pay for it. Only after the warm-up is the sidecar "running".
func (m *Manager) await(gen int, port int) {
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		if !m.current(gen) {
			return
		}
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 300*time.Millisecond)
		if err == nil {
			c.Close()
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !m.current(gen) {
		return
	}
	started := time.Now()
	if err := m.warmUp(port); err != nil {
		if !m.current(gen) {
			return
		}
		m.log.Warn("local stt: warm-up failed", "err", err)
		m.set(func(s *Status) { s.Phase, s.Error = PhaseError, "opwarmen mislukt: "+err.Error() })
		m.stop(PhaseError)
		return
	}
	ms := time.Since(started).Milliseconds()
	m.log.Info("local stt: ready", "port", port, "warmup_ms", ms)
	m.set(func(s *Status) { s.Phase, s.Error, s.LastMS = PhaseRunning, "", ms })
}

func (m *Manager) current(gen int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.gen == gen && m.cmd != nil
}

func (m *Manager) warmUp(port int) error {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("response_format", "json")
	part, err := mw.CreateFormFile("file", "warmup.wav")
	if err != nil {
		return err
	}
	_, _ = part.Write(silenceWAV(1))
	_ = mw.Close()
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/v1/audio/transcriptions", port), &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := m.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// silenceWAV is seconds of 16 kHz mono s16le silence in a WAV container — the
// same format Vito records in, and the only one the server decodes. Written
// here rather than borrowed from the audio package so this package stays free
// of cgo and can be compiled for every platform from any other.
func silenceWAV(seconds int) []byte {
	pcm := make([]byte, 16000*2*seconds)
	var buf bytes.Buffer
	buf.WriteString("RIFF")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(36+len(pcm)))
	buf.WriteString("WAVEfmt ")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(16))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(1)) // PCM
	_ = binary.Write(&buf, binary.LittleEndian, uint16(1)) // mono
	_ = binary.Write(&buf, binary.LittleEndian, uint32(16000))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(16000*2))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(2))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(16))
	buf.WriteString("data")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(len(pcm)))
	buf.Write(pcm)
	return buf.Bytes()
}

// NoteLatency records how long the last dictation took at the sidecar, for
// the settings page — "it works" and "it works in 0.3 s" are different claims.
func (m *Manager) NoteLatency(d time.Duration) {
	m.set(func(s *Status) {
		if s.Phase == PhaseRunning {
			s.LastMS = d.Milliseconds()
		}
	})
}

// Install downloads the build for this machine and the model, verifies both,
// and starts the server if the provider is selected. It returns at once; the
// progress goes out as status events. A second call while one is running is
// refused rather than queued.
func (m *Manager) Install(variant string) error {
	if variant == "" {
		variant = VariantCPU
	}
	a, ok := assetFor(runtime.GOOS, runtime.GOARCH, variant)
	if !ok {
		return fmt.Errorf("geen %s-build van %s voor %s/%s", variant, Engine, runtime.GOOS, runtime.GOARCH)
	}
	if m.dir == "" {
		return errors.New("geen cachemap beschikbaar")
	}
	m.mu.Lock()
	if m.installing {
		m.mu.Unlock()
		return errors.New("installatie loopt al")
	}
	m.installing = true
	m.mu.Unlock()
	go m.install(a, variant)
	return nil
}

func (m *Manager) install(a asset, variant string) {
	defer func() {
		m.mu.Lock()
		m.installing = false
		m.mu.Unlock()
		m.reconcile()
	}()
	// A running server holds the binary open on Windows; swapping the build
	// under it is not an upgrade path, it is a locked file.
	m.stop(PhaseDownloading)
	m.set(func(s *Status) { s.Installed, s.Phase, s.Frac, s.Error = false, PhaseDownloading, 0, "" })
	fail := func(err error) {
		m.log.Error("local stt: install failed", "err", err)
		m.set(func(s *Status) { s.Phase, s.Error = PhaseError, err.Error() })
	}
	ctx := context.Background()
	total := a.size + modelSize
	report := func(done int64) {
		m.set(func(s *Status) { s.Frac = float64(done) / float64(total) })
	}

	// The binary. Small, so always fetched afresh: it is the part that changes
	// between releases and between variants.
	archive := filepath.Join(m.dir, "bin", a.name)
	if err := os.MkdirAll(filepath.Dir(archive), 0o755); err != nil {
		fail(err)
		return
	}
	_ = os.Remove(m.versionPath())
	if err := download(ctx, m.http, release+a.name, archive, a.size, a.sha, report); err != nil {
		fail(fmt.Errorf("%s downloaden: %w", Engine, err))
		return
	}
	if err := extractServer(archive, m.binPath()); err != nil {
		fail(fmt.Errorf("%s uitpakken: %w", Engine, err))
		return
	}
	_ = os.Remove(archive)

	// The model. Big, so a finished download is kept across reinstalls and an
	// interrupted one is resumed; the .ok marker says it passed its hash once.
	if err := os.MkdirAll(filepath.Dir(m.modelPath()), 0o755); err != nil {
		fail(err)
		return
	}
	if _, err := os.Stat(m.modelOKPath()); err != nil {
		if err := download(ctx, m.http, modelURL, m.modelPath(), modelSize, modelSHA, func(done int64) { report(a.size + done) }); err != nil {
			fail(fmt.Errorf("model downloaden: %w", err))
			return
		}
		if err := os.WriteFile(m.modelOKPath(), []byte(modelSHA+"\n"), 0o644); err != nil {
			fail(err)
			return
		}
	}
	report(total)
	if err := os.WriteFile(m.versionPath(), []byte(Version+" "+variant+"\n"), 0o644); err != nil {
		fail(err)
		return
	}
	m.log.Info("local stt: installed", "engine", Engine, "version", Version, "variant", variant, "model", ModelName)
	m.set(func(s *Status) { s.Installed, s.Variant, s.Phase, s.Frac, s.Error = true, variant, PhaseStopped, 1, "" })
}

// Remove stops the server and deletes everything it installed.
func (m *Manager) Remove() error {
	m.mu.Lock()
	if m.installing {
		m.mu.Unlock()
		return errors.New("installatie loopt nog")
	}
	m.mu.Unlock()
	m.stop(PhaseAbsent)
	if m.dir != "" {
		if err := os.RemoveAll(m.dir); err != nil {
			return err
		}
	}
	m.set(func(s *Status) {
		*s = Status{Supported: s.Supported, Variants: s.Variants, Engine: Engine, Version: Version, Model: ModelName, ModelMB: modelSize >> 20, Phase: PhaseAbsent}
	})
	return nil
}

// download fetches url to dst, resuming a partial file, and verifies size and
// SHA-256 before the file gets its final name. A mismatch deletes it: a wrong
// binary must never be executed, a wrong model never loaded.
func download(ctx context.Context, client *http.Client, url, dst string, size int64, sha string, progress func(done int64)) error {
	part := dst + ".part"
	var have int64
	if st, err := os.Stat(part); err == nil {
		have = st.Size()
		if have > size {
			have = 0
			_ = os.Remove(part)
		}
	}
	if have < size {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		if have > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", have))
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		flags := os.O_CREATE | os.O_WRONLY
		switch {
		case resp.StatusCode == http.StatusPartialContent && have > 0:
			flags |= os.O_APPEND
		case resp.StatusCode == http.StatusOK:
			have = 0 // the server ignored the range: start over
			flags |= os.O_TRUNC
		default:
			return fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		f, err := os.OpenFile(part, flags, 0o644)
		if err != nil {
			return err
		}
		buf := make([]byte, 256<<10)
		last := time.Time{}
		for {
			n, rerr := resp.Body.Read(buf)
			if n > 0 {
				if _, werr := f.Write(buf[:n]); werr != nil {
					f.Close()
					return werr
				}
				have += int64(n)
				if progress != nil && (time.Since(last) > 150*time.Millisecond || have == size) {
					last = time.Now()
					progress(have)
				}
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
	}
	if have != size {
		_ = os.Remove(part)
		return fmt.Errorf("onvolledig: %d van %d bytes", have, size)
	}
	got, err := fileSHA256(part)
	if err != nil {
		return err
	}
	if got != sha {
		_ = os.Remove(part)
		return fmt.Errorf("controlesom klopt niet (%s…)", got[:12])
	}
	_ = os.Remove(dst)
	return os.Rename(part, dst)
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// tail keeps the last few lines a process wrote to stderr, for the error
// message when it dies — "exit status 1" on its own explains nothing.
type tail struct {
	mu    sync.Mutex
	lines []string
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, l := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			t.lines = append(t.lines, l)
		}
	}
	if len(t.lines) > 5 {
		t.lines = t.lines[len(t.lines)-5:]
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.Join(t.lines, " | ")
}
