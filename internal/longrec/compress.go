package longrec

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	gomp3 "github.com/hajimehoshi/go-mp3"

	"vito/internal/audio"
	"vito/internal/mp3"
)

// A session records into a WAV: simple to recover after a crash, and what
// local speech recognition wants. Once it is finished the WAV becomes an MP3
// of 48 kbps mono (about 21 MB an hour instead of 115), which every browser
// plays and the speech services take. Continuing or restoring a session turns
// the MP3 back into a WAV first (EnsureWAV); stopping it again compresses it
// again. The MP3 is written beside the WAV and checked before it replaces it,
// so a crash in between leaves the WAV, which the next start compresses.

const (
	mp3Kbps = 48
	// mp3Frame is the samples in one frame at 16 kHz (MPEG-2: one granule).
	mp3Frame = 576
	// mp3FrameBytes is one frame's size: 576 samples at 48 kbps and 16 kHz
	// are exactly 216 bytes, so every frame is that long, unpadded.
	mp3FrameBytes = mp3Frame * mp3Kbps * 1000 / 8 / audio.CaptureSampleRate
	// mp3Delay is how many samples a decoded MP3 runs behind its source (the
	// encoder's and decoder's filter banks); dropped when it becomes a WAV
	// again, so the times of the blocks still match the audio.
	mp3Delay = 1057
)

// audioMu keeps compressing and expanding a session's audio apart (one at a
// time over all sessions; each takes seconds).
var audioMu sync.Mutex

func (s *Store) wavPath(id string) string { return s.path(id, "audio.wav") }
func (s *Store) mp3Path(id string) string { return s.path(id, "audio.mp3") }

// AudioPath is the session's audio file: the WAV while there is one (while
// recording, and until it is compressed), else the MP3.
func (s *Store) AudioPath(id string) string {
	if _, err := os.Stat(s.wavPath(id)); err != nil {
		if _, err := os.Stat(s.mp3Path(id)); err == nil {
			return s.mp3Path(id)
		}
	}
	return s.wavPath(id)
}

// AudioType is "mp3" or "wav", what AudioPath holds.
func (s *Store) AudioType(id string) string {
	if s.AudioPath(id) == s.mp3Path(id) {
		return "mp3"
	}
	return "wav"
}

// Compress turns a finished session's WAV into its MP3. Nothing to do for a
// session that is not finished or has no WAV.
func (s *Store) Compress(id string) error {
	audioMu.Lock()
	defer audioMu.Unlock()
	m, err := s.Meta(id)
	if err != nil || m.State != StateDone {
		return err
	}
	wav := s.wavPath(id)
	if _, err := os.Stat(wav); err != nil {
		return nil
	}
	part := s.mp3Path(id) + ".part"
	frames, err := encodeMP3(wav, part)
	if err == nil {
		// The file must be whole frames, as many as the samples ask for.
		var fi os.FileInfo
		if fi, err = os.Stat(part); err == nil && fi.Size() != int64(frames)*mp3FrameBytes {
			err = fmt.Errorf("mp3 is %d bytes, %d frames of %d expected", fi.Size(), frames, mp3FrameBytes)
		}
	}
	if err == nil {
		err = os.Rename(part, s.mp3Path(id))
	}
	if err != nil {
		os.Remove(part)
		return fmt.Errorf("compress %s: %w", id, err)
	}
	return os.Remove(wav)
}

// encodeMP3 writes the WAV at src (Vito's own: 16 kHz mono, 44-byte header)
// to dst as MP3, frame by frame, and returns the frames written.
func encodeMP3(src, dst string) (int, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	if _, err := in.Seek(wavHeader, io.SeekStart); err != nil {
		return 0, err
	}
	out, err := os.Create(dst)
	if err != nil {
		return 0, err
	}
	w := bufio.NewWriterSize(out, 64<<10)
	enc := mp3.NewEncoderBitrate(audio.CaptureSampleRate, 1, mp3Kbps)
	r := bufio.NewReaderSize(in, 64<<10)
	raw := make([]byte, mp3Frame*2)
	pcm := make([]int16, mp3Frame)
	frames := 0
	for {
		n, rerr := io.ReadFull(r, raw)
		if n == 0 {
			break
		}
		for i := range pcm {
			pcm[i] = 0 // the last frame is filled up with silence
			if 2*i+1 < n {
				pcm[i] = int16(binary.LittleEndian.Uint16(raw[2*i:]))
			}
		}
		data, written := enc.EncodeBufferInterleaved(pcm)
		if _, err := w.Write(data[:written]); err != nil {
			out.Close()
			return frames, err
		}
		frames++
		if rerr != nil {
			break
		}
	}
	if err := w.Flush(); err != nil {
		out.Close()
		return frames, err
	}
	return frames, out.Close()
}

// EnsureWAV gives a compressed session its WAV back, for recording on or
// transcribing from it; the MP3 goes. Nothing to do when the WAV is there.
func (s *Store) EnsureWAV(id string) error {
	audioMu.Lock()
	defer audioMu.Unlock()
	wav, src := s.wavPath(id), s.mp3Path(id)
	if _, err := os.Stat(wav); err == nil {
		return nil
	}
	if _, err := os.Stat(src); err != nil {
		return nil // no audio at all; recording on creates none either
	}
	part := wav + ".part"
	if err := decodeMP3(src, part); err != nil {
		os.Remove(part)
		return fmt.Errorf("expand %s: %w", id, err)
	}
	if err := os.Rename(part, wav); err != nil {
		os.Remove(part)
		return err
	}
	return os.Remove(src)
}

// decodeMP3 writes the MP3 at src to dst as Vito's WAV: 16 kHz mono, its
// first mp3Delay samples dropped.
func decodeMP3(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	dec, err := gomp3.NewDecoder(in)
	if err != nil {
		return err
	}
	if dec.SampleRate() != audio.CaptureSampleRate {
		return fmt.Errorf("mp3 at %d Hz", dec.SampleRate())
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := out.Write(wavHeaderBytes(0)); err != nil {
		out.Close()
		return err
	}
	w := bufio.NewWriterSize(out, 64<<10)
	r := bufio.NewReaderSize(dec, 64<<10)
	// go-mp3 gives stereo: four bytes a sample, the left channel first.
	frame := make([]byte, 4)
	var n int64
	for skip := mp3Delay; ; skip-- {
		if _, err := io.ReadFull(r, frame); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				break
			}
			out.Close()
			return err
		}
		if skip > 0 {
			continue
		}
		if _, err := w.Write(frame[:2]); err != nil {
			out.Close()
			return err
		}
		n += 2
	}
	if err := w.Flush(); err != nil {
		out.Close()
		return err
	}
	if _, err := out.WriteAt(wavHeaderBytes(n), 0); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// CompressAll compresses every finished session still holding a WAV — from
// before compression existed, or one a crash interrupted. busy says which to
// leave alone.
func (s *Store) CompressAll(busy func(id string) bool) {
	list, _ := s.List()
	for _, m := range list {
		if m.State != StateDone || busy(m.ID) {
			continue
		}
		if _, err := os.Stat(s.wavPath(m.ID)); err != nil {
			continue
		}
		_ = s.Compress(m.ID)
	}
}
