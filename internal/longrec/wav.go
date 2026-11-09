package longrec

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"time"

	"vito/internal/audio"
)

const (
	wavHeader   = 44
	bytesPerSec = audio.CaptureSampleRate * audio.CaptureChannels * audio.BytesPerSample
)

// wavFile is a session's audio: one WAV the samples are appended to as they
// come, its sizes patched every few seconds so a crash leaves at most those
// seconds unannounced (and repairWAV announces them at the next start).
type wavFile struct {
	f       *os.File
	n       int64 // payload bytes
	patched time.Time
}

func createWAV(path string) (*wavFile, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	if _, err := f.Write(wavHeaderBytes(0)); err != nil {
		f.Close()
		return nil, err
	}
	return &wavFile{f: f, patched: time.Now()}, nil
}

func (w *wavFile) write(b []byte) error {
	n, err := w.f.Write(b)
	w.n += int64(n)
	if err != nil {
		return err
	}
	if time.Since(w.patched) >= 5*time.Second {
		w.patched = time.Now()
		return w.patch()
	}
	return nil
}

func (w *wavFile) seconds() float64 { return float64(w.n) / bytesPerSec }

func (w *wavFile) patch() error {
	_, err := w.f.WriteAt(wavHeaderBytes(w.n)[:44], 0)
	return err
}

func (w *wavFile) close() error {
	if err := w.patch(); err != nil {
		w.f.Close()
		return err
	}
	if err := w.f.Sync(); err != nil {
		w.f.Close()
		return err
	}
	return w.f.Close()
}

func wavHeaderBytes(n int64) []byte {
	h := make([]byte, wavHeader)
	copy(h[0:], "RIFF")
	binary.LittleEndian.PutUint32(h[4:], uint32(36+n))
	copy(h[8:], "WAVE")
	copy(h[12:], "fmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	binary.LittleEndian.PutUint16(h[20:], 1)
	binary.LittleEndian.PutUint16(h[22:], audio.CaptureChannels)
	binary.LittleEndian.PutUint32(h[24:], audio.CaptureSampleRate)
	binary.LittleEndian.PutUint32(h[28:], bytesPerSec)
	binary.LittleEndian.PutUint16(h[32:], audio.CaptureChannels*audio.BytesPerSample)
	binary.LittleEndian.PutUint16(h[34:], 16)
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], uint32(n))
	return h
}

// repairWAV makes the header announce everything the file holds — after a
// crash it announces only what was there at the last patch. It returns the
// length in seconds.
func repairWAV(path string) (float64, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	n := st.Size() - wavHeader
	if n < 0 {
		return 0, fmt.Errorf("%s: shorter than a WAV header", path)
	}
	n -= n % (audio.CaptureChannels * audio.BytesPerSample) // a half-written sample
	if _, err := f.WriteAt(wavHeaderBytes(n), 0); err != nil {
		return 0, err
	}
	return float64(n) / bytesPerSec, f.Sync()
}

// wavSeconds reads a session's audio length from the file's size.
func wavSeconds(path string) float64 {
	st, err := os.Stat(path)
	if err != nil || st.Size() < wavHeader {
		return 0
	}
	return float64(st.Size()-wavHeader) / bytesPerSec
}

// sliceWAV writes the audio from `from` to `to` seconds of src into a WAV of
// its own, for a block transcribed from the file.
func sliceWAV(src, dst string, from, to float64) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	align := func(sec float64) int64 {
		b := int64(sec * bytesPerSec)
		return b - b%(audio.CaptureChannels*audio.BytesPerSample)
	}
	a, b := align(from), align(to)
	if b <= a {
		return fmt.Errorf("empty slice %.1f–%.1f s", from, to)
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := out.Write(wavHeaderBytes(b - a)); err != nil {
		out.Close()
		return err
	}
	if _, err := in.Seek(wavHeader+a, io.SeekStart); err != nil {
		out.Close()
		return err
	}
	if _, err := io.CopyN(out, in, b-a); err != nil && err != io.EOF {
		out.Close()
		return err
	}
	return out.Close()
}

// openWAV reopens a session's audio to append to it (a session continued
// later). The header is repaired first, so the length is what the file holds.
func openWAV(path string) (*wavFile, error) {
	if _, err := repairWAV(path); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	n := st.Size() - wavHeader
	n -= n % (audio.CaptureChannels * audio.BytesPerSample)
	if _, err := f.Seek(wavHeader+n, io.SeekStart); err != nil {
		f.Close()
		return nil, err
	}
	return &wavFile{f: f, n: n, patched: time.Now()}, nil
}
