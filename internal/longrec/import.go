package longrec

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"vito/internal/audio"
	"vito/internal/stt"
)

// An audio file becomes a session too (Source "upload"). The page decodes it
// (any format the browser plays) to Vito's own WAV, 16 kHz mono; Vito cuts it
// at pauses as a recording is cut, and has each block transcribed from its
// slice of the file, one after the other. While that runs the session is
// StateImporting; a daemon that stops meanwhile carries on at the next start
// with the blocks still missing.

// StateImporting is a session whose audio file is being transcribed.
const StateImporting = "importing"

// NewImport makes a session for an audio file and writes its audio from wav
// (Vito's WAV: 16 kHz mono 16-bit, any header). It returns the session.
func (s *Store) NewImport(title string, wav io.Reader) (Meta, error) {
	m := Meta{ID: newID(), Title: strings.TrimSpace(title), Started: time.Now(), State: StateImporting, Source: "upload"}
	if err := s.create(m); err != nil {
		return m, err
	}
	n, err := copyWAV(wav, s.wavPath(m.ID))
	if err != nil {
		s.Delete(m.ID)
		return m, err
	}
	if n < bytesPerSec/2 {
		s.Delete(m.ID)
		return m, errors.New("the audio file is empty")
	}
	return s.UpdateMeta(m.ID, func(m *Meta) { m.Seconds = float64(n) / bytesPerSec })
}

// copyWAV checks a WAV's format and writes its samples under Vito's own
// header; it returns the bytes of audio.
func copyWAV(r io.Reader, dst string) (int64, error) {
	br := bufio.NewReaderSize(r, 64<<10)
	var riff [12]byte
	if _, err := io.ReadFull(br, riff[:]); err != nil || string(riff[0:4]) != "RIFF" || string(riff[8:12]) != "WAVE" {
		return 0, errors.New("not a WAV file")
	}
	// Chunks until "data"; "fmt " must say 16 kHz mono 16-bit PCM.
	ok := false
	for {
		var h [8]byte
		if _, err := io.ReadFull(br, h[:]); err != nil {
			return 0, errors.New("no audio in the WAV file")
		}
		size := int64(binary.LittleEndian.Uint32(h[4:]))
		switch string(h[:4]) {
		case "fmt ":
			b := make([]byte, size)
			if _, err := io.ReadFull(br, b); err != nil || len(b) < 16 {
				return 0, errors.New("broken WAV header")
			}
			format, ch := binary.LittleEndian.Uint16(b[0:]), binary.LittleEndian.Uint16(b[2:])
			rate, bits := binary.LittleEndian.Uint32(b[4:]), binary.LittleEndian.Uint16(b[14:])
			if format != 1 || ch != 1 || rate != audio.CaptureSampleRate || bits != 16 {
				return 0, fmt.Errorf("the WAV must be 16 kHz mono 16-bit (is %d Hz, %d channels, %d bit)", rate, ch, bits)
			}
			ok = true
			if size%2 == 1 {
				br.Discard(1)
			}
		case "data":
			if !ok {
				return 0, errors.New("broken WAV header")
			}
			out, err := os.Create(dst)
			if err != nil {
				return 0, err
			}
			w := bufio.NewWriterSize(out, 64<<10)
			if _, err := w.Write(wavHeaderBytes(0)); err != nil {
				out.Close()
				return 0, err
			}
			// The data size from a browser can be 0 or wrong for a stream:
			// everything up to the end is audio.
			n, err := io.Copy(w, br)
			if err != nil {
				out.Close()
				return 0, err
			}
			n -= n % 2
			if err := w.Flush(); err != nil {
				out.Close()
				return 0, err
			}
			if err := out.Truncate(int64(wavHeader) + n); err != nil {
				out.Close()
				return 0, err
			}
			if _, err := out.WriteAt(wavHeaderBytes(n), 0); err != nil {
				out.Close()
				return 0, err
			}
			return n, out.Close()
		default:
			if _, err := br.Discard(int(size + size%2)); err != nil {
				return 0, errors.New("broken WAV file")
			}
		}
	}
}

// CutPoints splits a WAV's audio where a recording would be cut: at the first
// pause after minBlock, at the latest after maxBlock. It returns the block
// boundaries in seconds, from 0 to the end.
func CutPoints(path string) ([]float64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Seek(wavHeader, io.SeekStart); err != nil {
		return nil, err
	}
	const frame = audio.CaptureSampleRate / 50 // 20 ms
	r := bufio.NewReaderSize(f, 64<<10)
	buf := make([]byte, frame*2)
	cuts := []float64{0}
	var bytes int64 // read so far: time without float drift
	var t, start, quietFrom float64
	quiet := false
	for {
		n, err := io.ReadFull(r, buf)
		if n == 0 {
			break
		}
		if peak(buf[:n]) < speechGate {
			if !quiet {
				quiet, quietFrom = true, t
			}
		} else {
			quiet = false
		}
		bytes += int64(n)
		t = float64(bytes) / bytesPerSec
		length := t - start
		if (length >= minBlock && quiet && t-quietFrom >= pauseLen) || length >= maxBlock {
			at := t
			if quiet && length < maxBlock {
				at = (quietFrom + t) / 2 // in the middle of the pause
			}
			cuts = append(cuts, at)
			start, quiet = at, false
		}
		if err != nil {
			break
		}
	}
	if t-cuts[len(cuts)-1] < 1 && len(cuts) > 1 {
		cuts[len(cuts)-1] = t // a last sliver joins the block before it
	} else {
		cuts = append(cuts, t)
	}
	return cuts, nil
}

// ImportProgress is how far an import is: blocks done of all.
type ImportProgress struct {
	Done, Total int
}

// Import transcribes a StateImporting session's blocks still missing, one by
// one, and marks it done. onBlock reports each block (for the cleanup queue
// and the page). It stops early, leaving the session importing, when ctx ends.
func Import(ctx context.Context, s *Store, id string, t stt.Transcriber, onBlock func(Block, ImportProgress)) error {
	m, err := s.Meta(id)
	if err != nil {
		return err
	}
	if m.State != StateImporting {
		return nil
	}
	wav := s.wavPath(id)
	cuts, err := CutPoints(wav)
	if err != nil {
		return err
	}
	have, _ := s.Blocks(id)
	done := map[int]bool{}
	for _, b := range have {
		done[b.Index] = true
	}
	total := len(cuts) - 1
	s.UpdateMeta(id, func(m *Meta) { m.Planned = total })
	for i := 0; i < total; i++ {
		if done[i] {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		text, terr := transcribeSlice(t, wav, cuts[i], cuts[i+1])
		blk := Block{Index: i, Start: cuts[i], End: cuts[i+1], Text: strings.TrimSpace(text)}
		if terr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			blk.Error = terr.Error()
		}
		if err := s.AddBlock(id, blk); err != nil {
			return err
		}
		done[i] = true
		if onBlock != nil {
			onBlock(blk, ImportProgress{Done: len(done), Total: total})
		}
	}
	_, err = s.UpdateMeta(id, func(m *Meta) {
		m.State = StateDone
		m.Words = 0
	})
	if err == nil {
		w := s.Words(id)
		s.UpdateMeta(id, func(m *Meta) { m.Words = w })
	}
	return err
}
