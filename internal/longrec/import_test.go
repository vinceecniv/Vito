package longrec

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"testing"
)

// An audio file is cut at its pauses and every block transcribed; the
// session ends done, with its length.
func TestImport(t *testing.T) {
	store, _ := NewStore(t.TempDir())
	// 200 s of tone with a second of silence every 40 s.
	const secs = 200
	pcm := make([]byte, secs*bytesPerSec)
	for i := 0; i < secs*16000; i++ {
		sec := i / 16000
		if sec%40 == 39 {
			continue
		}
		v := int16(8000 * math.Sin(2*math.Pi*300*float64(i)/16000))
		binary.LittleEndian.PutUint16(pcm[2*i:], uint16(v))
	}
	// A browser's WAV: an extra chunk before the data, data size 0.
	var b bytes.Buffer
	b.WriteString("RIFF\x00\x00\x00\x00WAVE")
	b.WriteString("LIST\x04\x00\x00\x00abcd")
	fmtc := make([]byte, 24)
	copy(fmtc, "fmt ")
	binary.LittleEndian.PutUint32(fmtc[4:], 16)
	binary.LittleEndian.PutUint16(fmtc[8:], 1)
	binary.LittleEndian.PutUint16(fmtc[10:], 1)
	binary.LittleEndian.PutUint32(fmtc[12:], 16000)
	binary.LittleEndian.PutUint32(fmtc[16:], 32000)
	binary.LittleEndian.PutUint16(fmtc[20:], 2)
	binary.LittleEndian.PutUint16(fmtc[22:], 16)
	b.Write(fmtc)
	b.WriteString("data\x00\x00\x00\x00")
	b.Write(pcm)

	m, err := store.NewImport("College", &b)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(m.Seconds-secs) > 0.01 || m.State != StateImporting || m.Source != "upload" {
		t.Fatalf("meta %+v", m)
	}
	cuts, _ := CutPoints(store.wavPath(m.ID))
	// Cut in the pauses at 79–80 s (the first after a minute) and 159–160 s.
	if len(cuts) != 4 || cuts[1] < 79 || cuts[1] > 80 || cuts[2] < 159 || cuts[2] > 160 || cuts[3] != secs {
		t.Fatalf("cuts %v", cuts)
	}
	var n int
	err = Import(context.Background(), store, m.ID, &fakeFile{}, func(Block, ImportProgress) { n++ })
	if err != nil {
		t.Fatal(err)
	}
	m, _ = store.Meta(m.ID)
	blocks, _ := store.Blocks(m.ID)
	if m.State != StateDone || len(blocks) != 3 || n != 3 || m.Planned != 3 || m.Words != 9 {
		t.Fatalf("meta %+v, %d blocks", m, len(blocks))
	}
}
