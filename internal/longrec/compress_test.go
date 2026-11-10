package longrec

import (
	"encoding/binary"
	"math"
	"os"
	"testing"
)

// A finished session's WAV becomes an MP3 of whole frames, and back again a
// WAV whose audio lines up with the original.
func TestCompressRoundTrip(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := "20261109-120000-cafe0001"
	if err := store.create(Meta{ID: id, State: StateDone}); err != nil {
		t.Fatal(err)
	}
	// Ten seconds of a voice-like sweep, with a quiet gap so lag shows.
	const n = 10 * 16000
	orig := make([]int16, n)
	for i := range orig {
		if i/8000%2 == 1 {
			continue
		}
		f := 200 + 800*float64(i)/n
		orig[i] = int16(9000 * math.Sin(2*math.Pi*f*float64(i)/16000))
	}
	pcm := make([]byte, 2*n)
	for i, v := range orig {
		binary.LittleEndian.PutUint16(pcm[2*i:], uint16(v))
	}
	if err := os.WriteFile(store.wavPath(id), append(wavHeaderBytes(int64(len(pcm))), pcm...), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := store.Compress(id); err != nil {
		t.Fatal(err)
	}
	if store.AudioType(id) != "mp3" {
		t.Fatal("still a wav after compressing")
	}
	if _, err := os.Stat(store.wavPath(id)); err == nil {
		t.Fatal("the wav was kept")
	}
	frames := (n + mp3Frame - 1) / mp3Frame
	if got := store.AudioBytes(id); got != int64(frames*mp3FrameBytes) {
		t.Fatalf("mp3 is %d bytes, want %d", got, frames*mp3FrameBytes)
	}

	if err := store.EnsureWAV(id); err != nil {
		t.Fatal(err)
	}
	if store.AudioType(id) != "wav" {
		t.Fatal("not a wav again")
	}
	b, err := os.ReadFile(store.wavPath(id))
	if err != nil {
		t.Fatal(err)
	}
	if got := wavSeconds(store.wavPath(id)); math.Abs(got-10) > 0.1 {
		t.Fatalf("expanded to %.3f s, want 10", got)
	}
	dec := make([]int16, (len(b)-wavHeader)/2)
	for i := range dec {
		dec[i] = int16(binary.LittleEndian.Uint16(b[wavHeader+2*i:]))
	}
	// The best match must be at no shift: mp3Delay took the codec's delay out.
	best, bestLag := -1.0, 0
	for lag := -200; lag <= 200; lag += 2 {
		var c float64
		for i := 20000; i < 60000; i += 3 {
			if j := i + lag; j >= 0 && j < len(dec) {
				c += float64(orig[i]) * float64(dec[j])
			}
		}
		if c > best {
			best, bestLag = c, lag
		}
	}
	if bestLag < -4 || bestLag > 4 {
		t.Fatalf("expanded audio is %d samples off", bestLag)
	}
}

// A session still being recorded is left alone.
func TestCompressOnlyFinished(t *testing.T) {
	store, _ := NewStore(t.TempDir())
	id := "20261109-120000-cafe0002"
	store.create(Meta{ID: id, State: StateRecording})
	os.WriteFile(store.wavPath(id), append(wavHeaderBytes(3200), make([]byte, 3200)...), 0o600)
	if err := store.Compress(id); err != nil {
		t.Fatal(err)
	}
	if store.AudioType(id) != "wav" {
		t.Fatal("a recording session was compressed")
	}
}
