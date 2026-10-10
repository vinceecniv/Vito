package wakeword

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// readF32 reads a reference file written by tools/wakeword/make_reference.py.
func readF32(t *testing.T, path string) *Tensor {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rank := int(binary.LittleEndian.Uint32(b))
	shape := make([]int, rank)
	for i := range shape {
		shape[i] = int(binary.LittleEndian.Uint32(b[4+4*i:]))
	}
	b = b[4+4*rank:]
	data := make([]float32, len(b)/4)
	for i := range data {
		data[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return &Tensor{Shape: shape, Data: data}
}

func maxDiff(a, b []float32) float64 {
	var m float64
	for i := range a {
		m = math.Max(m, math.Abs(float64(a[i]-b[i])))
	}
	return m
}

// refDir holds onnxruntime's outputs for the networks, and the classifier
// they were made with. Committed for Vito's own models; set VITO_WW_REF to
// check against another directory.
func refDir(t *testing.T) string {
	if d := os.Getenv("VITO_WW_REF"); d != "" {
		return d
	}
	d := filepath.Join("testdata")
	if _, err := os.Stat(filepath.Join(d, "ww_in.f32")); err != nil {
		t.Skip("no reference data")
	}
	return d
}

// classifier is the reference dir's own .onnx, or else the shipped hey_vito.
func classifier(t *testing.T, dir string) []byte {
	m, _ := filepath.Glob(filepath.Join(dir, "*.onnx"))
	for _, p := range m {
		switch filepath.Base(p) {
		case "melspectrogram.onnx", "embedding_model.onnx":
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	b, err := classifierFor("hey_vito")
	if err != nil {
		t.Skip("no classifier")
	}
	return b
}

// streamAudio is the clip make_reference.py streamed: 1 s of silence, the
// clip, 1 s of silence, cut to whole 80 ms steps.
func streamAudio(t *testing.T, dir string) []int16 {
	if _, err := os.Stat(filepath.Join(dir, "stream_in.f32")); err == nil {
		a := readF32(t, filepath.Join(dir, "stream_in.f32")).Data
		pcm := make([]int16, len(a))
		for i, v := range a {
			pcm[i] = int16(v)
		}
		return pcm
	}
	w, _ := filepath.Glob(filepath.Join(dir, "*.wav"))
	if len(w) == 0 {
		t.Skip("no reference clip")
	}
	b, err := os.ReadFile(w[0])
	if err != nil {
		t.Fatal(err)
	}
	clip, err := decodeWAV(b)
	if err != nil {
		t.Fatal(err)
	}
	pcm := append(append(make([]int16, 16000), clip...), make([]int16, 16000)...)
	return pcm[:len(pcm)/ChunkSamples*ChunkSamples]
}

func TestNetworksMatchOnnxRuntime(t *testing.T) {
	dir := refDir(t)
	mel, emb, err := shared()
	if err != nil {
		t.Fatal(err)
	}
	cls, err := LoadModel(classifier(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name  string
		m     *Model
		input string
		tol   float64
	}{
		{"mel", mel, "input", 1e-3},
		{"emb", emb, "input_1", 1e-3},
		{"ww", cls, cls.Inputs()[0], 1e-4},
	} {
		in := readF32(t, filepath.Join(dir, c.name+"_in.f32"))
		want := readF32(t, filepath.Join(dir, c.name+"_out.f32"))
		out, err := c.m.Run(map[string]*Tensor{c.input: in})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if len(out[0].Data) != len(want.Data) {
			t.Fatalf("%s: %d values (%v), want %d (%v)", c.name, len(out[0].Data), out[0].Shape, len(want.Data), want.Shape)
		}
		// the melspectrogram is in dB-like units up to ~100; compare relative to its scale
		if d := maxDiff(out[0].Data, want.Data); d > c.tol*math.Max(1, scale(want.Data)) {
			t.Errorf("%s: max difference %g", c.name, d)
		}
	}
}

func scale(x []float32) float64 {
	var m float64
	for _, v := range x {
		m = math.Max(m, math.Abs(float64(v)))
	}
	return m
}

func TestStreamMatchesOpenWakeWord(t *testing.T) {
	dir := refDir(t)
	d, err := NewDetector(classifier(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	pcm := streamAudio(t, dir)
	want := readF32(t, filepath.Join(dir, "stream_out.f32")).Data
	// feed in odd-sized pieces: the chunking must not matter
	var got []float32
	for i := 0; i < len(pcm); i += 1000 {
		s, err := d.Write(pcm[i:min(i+1000, len(pcm))])
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, s...)
	}
	if len(got) != len(want) {
		t.Fatalf("%d scores, want %d", len(got), len(want))
	}
	if diff := maxDiff(got, want); diff > 0.01 {
		t.Errorf("scores differ by up to %g\n got %v\nwant %v", diff, got, want)
	}
	var peak float32
	for _, s := range got {
		peak = max(peak, s)
	}
	t.Logf("peak score %.3f over %d steps", peak, len(got))
}

func BenchmarkStep(b *testing.B) {
	dir := os.Getenv("VITO_WW_REF")
	if dir == "" {
		dir = "testdata"
	}
	cls, err := classifierFor("hey_vito")
	if err != nil {
		b.Skip("no classifier")
	}
	d, err := NewDetector(cls)
	if err != nil {
		b.Fatal(err)
	}
	chunk := make([]int16, ChunkSamples)
	for i := range chunk {
		chunk[i] = int16(1000 * math.Sin(float64(i)/7))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := d.Write(chunk); err != nil {
			b.Fatal(err)
		}
	}
}

// The streamed embedding must equal running the network on the full window.
func TestStreamEmbeddingMatchesWhole(t *testing.T) {
	_, emb, err := shared()
	if err != nil {
		t.Fatal(err)
	}
	s, err := newStreamEmbedding(emb)
	if err != nil {
		t.Fatal(err)
	}
	win := make([]float32, melWindow*melBands)
	for i := range win {
		win[i] = float32(math.Sin(float64(i)*0.37)) + 1
	}
	for step := 0; step < 20; step++ {
		copy(win, win[8*melBands:])
		for i := len(win) - 8*melBands; i < len(win); i++ {
			win[i] = float32(math.Cos(float64(i*step)*0.11)) * 2
		}
		got := append([]float32(nil), s.push(win, 8)...)
		in := append([]float32(nil), win...)
		out, err := emb.Run(map[string]*Tensor{"input_1": {Shape: []int{1, melWindow, melBands, 1}, Data: in}})
		if err != nil {
			t.Fatal(err)
		}
		if d := maxDiff(got, out[0].Data); d > 1e-4 {
			t.Fatalf("step %d: differs by %g", step, d)
		}
	}
}

func TestMelConvsRunAsFFT(t *testing.T) {
	mel, _, err := shared()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, nd := range mel.g.nodes {
		if nd.stft != nil {
			n++
		}
	}
	if n != 2 {
		t.Errorf("%d of the STFT convolutions recognised, want 2", n)
	}
}

func TestDot(t *testing.T) {
	for n := 0; n < 70; n++ {
		a, b := make([]float32, n), make([]float32, n)
		for i := range a {
			a[i], b[i] = float32(i%7)-3, float32(i%5)*0.5
		}
		if got, want := dot(a, b), dotGo(a, b); math.Abs(float64(got-want)) > 1e-3 {
			t.Errorf("n=%d: %g, want %g", n, got, want)
		}
	}
}
