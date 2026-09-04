package localstt

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Every platform Vito ships for has a standard build; the GPU build exists
// where Vulkan does. Apple Silicon's standard build is the Metal one.
func TestAssetsPerPlatform(t *testing.T) {
	for _, tc := range []struct {
		goos, goarch string
		want         []string
	}{
		{"windows", "amd64", []string{"cpu", "vulkan"}},
		{"linux", "amd64", []string{"cpu", "vulkan"}},
		{"linux", "arm64", []string{"cpu", "vulkan"}},
		{"darwin", "arm64", []string{"cpu"}},
		{"darwin", "amd64", []string{"cpu"}},
		{"windows", "arm64", nil},
		{"freebsd", "amd64", nil},
	} {
		got := variantsFor(tc.goos, tc.goarch)
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("%s/%s: variants %v, want %v", tc.goos, tc.goarch, got, tc.want)
		}
	}
	if a, _ := assetFor("darwin", "arm64", ""); !strings.Contains(a.name, "metal") {
		t.Errorf("empty variant should be the standard build; darwin/arm64 got %s", a.name)
	}
	for k, a := range assets {
		if len(a.sha) != 64 || a.size <= 0 {
			t.Errorf("%s: incomplete pin %+v", k, a)
		}
	}
}

func sha(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// The archive readers find the server binary by name in either format and
// ignore everything else that ships beside it.
func TestExtractServer(t *testing.T) {
	dir := t.TempDir()
	payload := []byte("#!/bin/sh\necho fake server\n")

	zipPath := filepath.Join(dir, "a.zip")
	var zb bytes.Buffer
	zw := zip.NewWriter(&zb)
	for name, data := range map[string][]byte{"parakeet-v0.5.0-bin-win-cpu-x64/README.md": []byte("readme"), "parakeet-v0.5.0-bin-win-cpu-x64/parakeet-cli.exe": []byte("cli"), "parakeet-v0.5.0-bin-win-cpu-x64/parakeet-server.exe": payload} {
		w, _ := zw.Create(name)
		_, _ = w.Write(data)
	}
	_ = zw.Close()
	_ = os.WriteFile(zipPath, zb.Bytes(), 0o644)

	tgzPath := filepath.Join(dir, "a.tar.gz")
	var tb bytes.Buffer
	gz := gzip.NewWriter(&tb)
	tw := tar.NewWriter(gz)
	for name, data := range map[string][]byte{"parakeet-v0.5.0-bin-linux-cpu-x64/parakeet-cli": []byte("cli"), "parakeet-v0.5.0-bin-linux-cpu-x64/parakeet-server": payload} {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(data)), Typeflag: tar.TypeReg})
		_, _ = tw.Write(data)
	}
	_ = tw.Close()
	_ = gz.Close()
	_ = os.WriteFile(tgzPath, tb.Bytes(), 0o644)

	for _, archive := range []string{zipPath, tgzPath} {
		dst := filepath.Join(dir, filepath.Base(archive)+".out", "parakeet-server")
		if err := extractServer(archive, dst); err != nil {
			t.Fatalf("%s: %v", archive, err)
		}
		got, err := os.ReadFile(dst)
		if err != nil || !bytes.Equal(got, payload) {
			t.Errorf("%s: extracted %q, %v", archive, got, err)
		}
		if runtime.GOOS != "windows" {
			if st, _ := os.Stat(dst); st.Mode()&0o111 == 0 {
				t.Errorf("%s: extracted file is not executable", archive)
			}
		}
	}
	// No server inside: an error, not an empty file.
	var eb bytes.Buffer
	ew := zip.NewWriter(&eb)
	w, _ := ew.Create("only/README.md")
	_, _ = w.Write([]byte("x"))
	_ = ew.Close()
	empty := filepath.Join(dir, "empty.zip")
	_ = os.WriteFile(empty, eb.Bytes(), 0o644)
	if err := extractServer(empty, filepath.Join(dir, "nope")); err == nil {
		t.Error("archive without a server should fail")
	}
}

// download verifies size and hash, resumes a partial file with a Range
// request, and refuses to keep anything that does not match.
func TestDownloadVerifiesAndResumes(t *testing.T) {
	data := bytes.Repeat([]byte("vito-parakeet-"), 4000) // 56 KB
	var ranges []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ranges = append(ranges, r.Header.Get("Range"))
		if rg := r.Header.Get("Range"); strings.HasPrefix(rg, "bytes=") {
			from, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(rg, "bytes="), "-"))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(data[from:])
			return
		}
		_, _ = w.Write(data)
	}))
	defer srv.Close()
	dir := t.TempDir()
	dst := filepath.Join(dir, "model.gguf")

	// A partial file from an earlier attempt: only the rest is fetched.
	_ = os.WriteFile(dst+".part", data[:20000], 0o644)
	var last int64
	if err := download(context.Background(), srv.Client(), srv.URL, dst, int64(len(data)), sha(data), func(n int64) { last = n }); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dst); !bytes.Equal(got, data) {
		t.Error("resumed file differs from the original")
	}
	if len(ranges) != 1 || ranges[0] != "bytes=20000-" || last != int64(len(data)) {
		t.Errorf("ranges=%v last=%d", ranges, last)
	}
	if _, err := os.Stat(dst + ".part"); err == nil {
		t.Error(".part should be renamed away")
	}

	// Wrong hash: the file must not survive.
	bad := filepath.Join(dir, "bad.gguf")
	if err := download(context.Background(), srv.Client(), srv.URL, bad, int64(len(data)), sha([]byte("other")), nil); err == nil {
		t.Fatal("hash mismatch should fail")
	}
	if _, err := os.Stat(bad); err == nil {
		t.Error("file with a bad hash was kept")
	}
	// Wrong size: likewise.
	if err := download(context.Background(), srv.Client(), srv.URL, bad, int64(len(data))+1, sha(data), nil); err == nil {
		t.Fatal("size mismatch should fail")
	}
}

// The manager drives a stand-in server (this test binary re-executed, see
// TestMain) through its life: start, wait for the port, warm up, report
// running, note latency, and stop.
func TestManagerLifecycle(t *testing.T) {
	dir := t.TempDir()
	m := newTestManager(t, dir)
	m.installFake(t)

	// The manager broadcasts from its own goroutines, so the record of what it
	// broadcast needs a lock of its own.
	var evMu sync.Mutex
	var events []Status
	m.emit = func(s Status) { evMu.Lock(); events = append(events, s); evMu.Unlock() }
	m.SetDesired(true, VariantCPU)
	waitPhase(t, m, PhaseRunning, 15*time.Second)

	st := m.Status()
	if st.Port == 0 || st.LastMS < 0 || !st.Installed || st.Variant != VariantCPU {
		t.Fatalf("running status = %+v", st)
	}
	if base, err := m.BaseURL(); err != nil || base != fmt.Sprintf("http://127.0.0.1:%d/v1", st.Port) {
		t.Errorf("BaseURL = %q, %v", base, err)
	}
	// The endpoint is the real thing: the stand-in answers like parakeet-server.
	resp, err := http.Post(fmt.Sprintf("http://127.0.0.1:%d/v1/audio/transcriptions", st.Port), "text/plain", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	m.NoteLatency(321 * time.Millisecond)
	if m.Status().LastMS != 321 {
		t.Error("latency not recorded")
	}

	// Deselecting the provider stops the process and keeps the install.
	m.SetDesired(false, VariantCPU)
	waitPhase(t, m, PhaseStopped, 5*time.Second)
	if _, err := m.BaseURL(); err == nil {
		t.Error("stopped engine should have no base URL")
	}
	if c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", st.Port), 200*time.Millisecond); err == nil {
		c.Close()
		t.Error("server still listening after stop")
	}
	if !m.Status().Installed {
		t.Error("stop must not uninstall")
	}
	sawStarting := false
	evMu.Lock()
	for _, e := range events {
		if e.Phase == PhaseStarting {
			sawStarting = true
		}
	}
	evMu.Unlock()
	if !sawStarting {
		t.Error("no 'starting' event was broadcast")
	}
}

// A server that dies is reported, and — while still wanted — brought back.
func TestManagerRestartsAfterCrash(t *testing.T) {
	dir := t.TempDir()
	m := newTestManager(t, dir)
	m.installFake(t)
	m.SetDesired(true, VariantCPU)
	waitPhase(t, m, PhaseRunning, 15*time.Second)
	m.mu.Lock()
	pid := m.cmd.Process.Pid
	proc := m.cmd.Process
	m.mu.Unlock()
	_ = proc.Kill()
	waitPhase(t, m, PhaseError, 5*time.Second)
	// The restart waits a few seconds on purpose; give it that.
	waitPhase(t, m, PhaseRunning, 20*time.Second)
	m.mu.Lock()
	newPID := m.cmd.Process.Pid
	m.mu.Unlock()
	if newPID == pid {
		t.Error("expected a new process after the crash")
	}
	m.Stop()
}

// Remove takes the whole directory with it.
func TestManagerRemove(t *testing.T) {
	dir := t.TempDir()
	m := newTestManager(t, dir)
	m.installFake(t)
	if !m.Status().Installed {
		t.Fatal("fake install not recognised")
	}
	if err := m.Remove(); err != nil {
		t.Fatal(err)
	}
	if st := m.Status(); st.Installed || st.Phase != PhaseAbsent {
		t.Errorf("after remove: %+v", st)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Error("directory should be gone")
	}
}

// --- test scaffolding -------------------------------------------------------

func newTestManager(t *testing.T, dir string) *Manager {
	t.Helper()
	m := New(slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	m.dir = dir
	m.st.Installed, m.st.Phase = false, PhaseAbsent
	// The stand-in: this test binary, told to behave like parakeet-server.
	m.command = func(bin, model string, port, threads int) *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=TestHelperServer", "--", strconv.Itoa(port))
		cmd.Env = append(os.Environ(), "VITO_FAKE_PARAKEET=1")
		return cmd
	}
	t.Cleanup(m.Stop)
	return m
}

// installFake lays down what installedVariant looks for, without downloads.
func (m *Manager) installFake(t *testing.T) {
	t.Helper()
	for _, p := range []string{m.binPath(), m.modelPath()} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.WriteFile(m.binPath(), []byte("fake"), 0o755)
	f, _ := os.Create(m.modelPath())
	_ = f.Truncate(modelSize) // sparse: the size is what is checked
	f.Close()
	_ = os.WriteFile(m.modelOKPath(), []byte(modelSHA+"\n"), 0o644)
	_ = os.WriteFile(m.versionPath(), []byte(Version+" "+VariantCPU+"\n"), 0o644)
	v, ok := m.installedVariant()
	if !ok || v != VariantCPU {
		t.Fatalf("fake install not detected: %q %v", v, ok)
	}
	m.set(func(s *Status) { s.Installed, s.Variant, s.Phase = true, v, PhaseStopped })
}

func waitPhase(t *testing.T, m *Manager, phase string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if st := m.Status(); st.Phase == phase {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("phase %s not reached in %s; status %+v", phase, timeout, m.Status())
}

// TestHelperServer is not a test: re-executed as the fake parakeet-server, it
// listens on the given port and answers transcription requests with an empty
// text, the way the real one answers silence.
func TestHelperServer(t *testing.T) {
	if os.Getenv("VITO_FAKE_PARAKEET") != "1" {
		return
	}
	port := os.Args[len(os.Args)-1]
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/audio/transcriptions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"text":""}`)
	})
	if err := http.ListenAndServe("127.0.0.1:"+port, mux); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
