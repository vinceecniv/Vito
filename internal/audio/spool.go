package audio

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Spool writes every captured sample to a local WAV file so a dictation can
// always be recovered via the async STT endpoint (reliability rule #1).
type Spool struct {
	f    *os.File
	n    int // payload bytes written
	path string
}

func SpoolDir() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "vito", "spool"), nil
}

func NewSpool() (*Spool, error) {
	dir, err := SpoolDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "vito-"+time.Now().Format("20060102-150405.000")+".wav")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	s := &Spool{f: f, path: path}
	if err := s.writeHeader(); err != nil {
		f.Close()
		os.Remove(path)
		return nil, err
	}
	return s, nil
}

func (s *Spool) Path() string { return s.path }

// Duration reports the recorded audio length so far.
func (s *Spool) Duration() time.Duration {
	samples := s.n / (BytesPerSample * CaptureChannels)
	return time.Duration(samples) * time.Second / CaptureSampleRate
}

func (s *Spool) Write(b []byte) error {
	n, err := s.f.Write(b)
	s.n += n
	return err
}

// Close patches the RIFF/data sizes and syncs the file.
func (s *Spool) Close() error {
	if err := s.patchSizes(); err != nil {
		s.f.Close()
		return err
	}
	if err := s.f.Sync(); err != nil {
		s.f.Close()
		return err
	}
	return s.f.Close()
}

func (s *Spool) Remove() { _ = os.Remove(s.path) }

const wavHeaderSize = 44

func (s *Spool) writeHeader() error {
	h := make([]byte, wavHeaderSize)
	copy(h[0:], "RIFF")
	// sizes at offsets 4 and 40 are patched on Close
	copy(h[8:], "WAVE")
	copy(h[12:], "fmt ")
	binary.LittleEndian.PutUint32(h[16:], 16) // fmt chunk size
	binary.LittleEndian.PutUint16(h[20:], 1)  // PCM
	binary.LittleEndian.PutUint16(h[22:], CaptureChannels)
	binary.LittleEndian.PutUint32(h[24:], CaptureSampleRate)
	binary.LittleEndian.PutUint32(h[28:], CaptureSampleRate*CaptureChannels*BytesPerSample) // byte rate
	binary.LittleEndian.PutUint16(h[32:], CaptureChannels*BytesPerSample)                   // block align
	binary.LittleEndian.PutUint16(h[34:], 16)                                               // bits per sample
	copy(h[36:], "data")
	_, err := s.f.Write(h)
	return err
}

func (s *Spool) patchSizes() error {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], uint32(36+s.n))
	if _, err := s.f.WriteAt(b[:], 4); err != nil {
		return fmt.Errorf("patch RIFF size: %w", err)
	}
	binary.LittleEndian.PutUint32(b[:], uint32(s.n))
	if _, err := s.f.WriteAt(b[:], 40); err != nil {
		return fmt.Errorf("patch data size: %w", err)
	}
	return nil
}
