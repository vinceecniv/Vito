// Package wakeword listens for a spoken wake phrase ("Hé Vito") on the
// microphone, entirely on the machine.
//
// It runs openWakeWord's pipeline: 16 kHz audio → a 32-band
// melspectrogram (10 ms frames) → Google's speech embedding (a 96-number
// summary of each 775 ms window, every 80 ms) → a small classifier over the
// last 16 embeddings (1.28 s) that gives the chance the phrase was just said.
// The first two networks are shared by every wake word; only the classifier
// is trained per phrase (tools/wakeword).
package wakeword

import (
	"fmt"
	"sync"
)

const (
	// ChunkSamples is how much audio one step consumes: 80 ms at 16 kHz.
	ChunkSamples = 1280
	melBands     = 32
	melWindow    = 76 // frames the embedding looks at
	embDim       = 96
	featWindow   = 16 // embeddings a classifier looks at, unless its input says otherwise
	// warmup steps return 0: the buffers still hold start-up values.
	warmup = 5
)

var (
	sharedOnce sync.Once
	sharedMel  *Model
	sharedEmb  *Model
	sharedErr  error
)

func shared() (*Model, *Model, error) {
	sharedOnce.Do(func() {
		load := func(name string) (*Model, error) {
			b, err := modelFS.ReadFile("models/" + name)
			if err != nil {
				return nil, err
			}
			return LoadModel(b)
		}
		if sharedMel, sharedErr = load("melspectrogram.onnx"); sharedErr != nil {
			return
		}
		sharedEmb, sharedErr = load("embedding_model.onnx")
	})
	return sharedMel, sharedEmb, sharedErr
}

// Detector scores a 16 kHz mono stream for one wake phrase. It is not safe
// for concurrent use.
type Detector struct {
	mel, emb, cls *Model
	clsInput      string
	window        int              // embeddings the classifier looks at
	stream        *streamEmbedding // nil: run the embedding whole every step

	raw     []float32 // the last ChunkSamples+480 samples
	pending []int16   // samples waiting for a full chunk
	melBuf  []float32 // melWindow × melBands, oldest first
	feat    []float32 // featWindow × embDim, oldest first
	steps   int
}

// NewDetector loads a classifier (an openWakeWord .onnx model).
func NewDetector(classifier []byte) (*Detector, error) {
	mel, emb, err := shared()
	if err != nil {
		return nil, fmt.Errorf("wakeword: feature models: %w", err)
	}
	cls, err := LoadModel(classifier)
	if err != nil {
		return nil, fmt.Errorf("wakeword: classifier: %w", err)
	}
	if len(cls.Inputs()) != 1 {
		return nil, fmt.Errorf("wakeword: classifier has %d inputs", len(cls.Inputs()))
	}
	d := &Detector{mel: mel, emb: emb, cls: cls, clsInput: cls.Inputs()[0], window: featWindow}
	if s := cls.InputShape(d.clsInput); len(s) == 3 {
		if s[2] != embDim {
			return nil, fmt.Errorf("wakeword: classifier takes %d features, not %d", s[2], embDim)
		}
		if s[1] > 0 {
			d.window = s[1]
		}
	}
	d.stream, _ = newStreamEmbedding(emb)
	d.Reset()
	return d, nil
}

// Reset forgets everything heard so far.
func (d *Detector) Reset() {
	d.raw = make([]float32, ChunkSamples+480)
	d.pending = d.pending[:0]
	d.melBuf = make([]float32, melWindow*melBands)
	for i := range d.melBuf {
		d.melBuf[i] = 1 // as openWakeWord starts
	}
	d.feat = make([]float32, d.window*embDim)
	d.steps = 0
	if d.stream != nil {
		d.stream.primed = false
	}
}

// Write feeds samples and returns one score (0–1) per completed 80 ms step.
func (d *Detector) Write(samples []int16) ([]float32, error) {
	d.pending = append(d.pending, samples...)
	var scores []float32
	for len(d.pending) >= ChunkSamples {
		s, err := d.step(d.pending[:ChunkSamples])
		if err != nil {
			return scores, err
		}
		scores = append(scores, s)
		d.pending = d.pending[ChunkSamples:]
	}
	// keep the backing array from growing without bound
	if cap(d.pending) > 8*ChunkSamples {
		d.pending = append([]int16(nil), d.pending...)
	}
	return scores, nil
}

func (d *Detector) step(chunk []int16) (float32, error) {
	// slide the raw window: 480 samples of context plus the new chunk
	copy(d.raw, d.raw[ChunkSamples:])
	for i, v := range chunk {
		d.raw[480+i] = float32(v)
	}
	out, err := d.mel.Run(map[string]*Tensor{"input": {Shape: []int{1, len(d.raw)}, Data: d.raw}})
	if err != nil {
		return 0, err
	}
	frames := out[0].Data // time × 1 × frames × 32
	n := len(frames) / melBands
	if n > melWindow {
		frames, n = frames[len(frames)-melWindow*melBands:], melWindow
	}
	copy(d.melBuf, d.melBuf[n*melBands:])
	tail := d.melBuf[len(d.melBuf)-n*melBands:]
	for i, v := range frames {
		tail[i] = v/10 + 2 // openWakeWord's transform towards the TF original
	}

	var e []float32
	if d.stream != nil {
		e = d.stream.push(d.melBuf, n)
	} else {
		in := make([]float32, len(d.melBuf))
		copy(in, d.melBuf)
		out, err = d.emb.Run(map[string]*Tensor{"input_1": {Shape: []int{1, melWindow, melBands, 1}, Data: in}})
		if err != nil {
			return 0, err
		}
		e = out[0].Data
	}
	copy(d.feat, d.feat[embDim:])
	copy(d.feat[len(d.feat)-embDim:], e)

	f := make([]float32, len(d.feat))
	copy(f, d.feat)
	out, err = d.cls.Run(map[string]*Tensor{d.clsInput: {Shape: []int{1, d.window, embDim}, Data: f}})
	if err != nil {
		return 0, err
	}
	d.steps++
	if d.steps <= warmup {
		return 0, nil
	}
	return out[0].Data[0], nil
}
