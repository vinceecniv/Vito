package whistle

import (
	"encoding/binary"
	"math"
)

// The model hears at most 30 s at a time, and is best given whole phrases, so
// a dictation is cut into segments at the pauses between them: cutting through
// a word costs that word, cutting in a pause costs nothing. Each finished
// segment is transcribed once; the open one is transcribed again now and then
// for the live text.
const (
	frameBytes   = SampleRate * 2 * 30 / 1000 // 30 ms
	pauseFrames  = 15                         // 450 ms of quiet ends a segment...
	minSegFrames = 50                         // ...once it is at least 1.5 s long
	maxSegFrames = 22 * 1000 / 30             // never longer than 22 s
	lookFrames   = 4 * 1000 / 30              // a forced cut lands in the quietest frame of the last 4 s
)

// Segmenter finds the cuts in a growing stream of 16 kHz mono s16le PCM.
type Segmenter struct {
	pos      int       // bytes seen
	pending  []byte    // an unfinished frame
	start    int       // byte offset where the open segment begins
	levels   []float64 // dB per frame of the open segment
	floor    float64   // running noise floor, dB
	speech   bool      // the open segment has had speech
	quietRun int       // frames of quiet at the end
}

// NewSegmenter starts at offset 0.
func NewSegmenter() *Segmenter { return &Segmenter{floor: -60} }

// Start is where the open segment begins: the audio from here on is not yet in
// a finished segment.
func (s *Segmenter) Start() int { return s.start }

// HasSpeech reports whether the open segment has heard anything worth sending.
func (s *Segmenter) HasSpeech() bool { return s.speech }

// Add feeds PCM and returns the byte offsets where segments ended, in order.
func (s *Segmenter) Add(pcm []byte) []int {
	var cuts []int
	data := append(s.pending, pcm...)
	n := len(data) / frameBytes * frameBytes
	for off := 0; off < n; off += frameBytes {
		db := level(data[off : off+frameBytes])
		s.pos += frameBytes
		// The floor follows quiet stretches down at once and creeps up slowly,
		// so steady background noise isn't taken for speech for long.
		if db < s.floor {
			s.floor = db
		} else {
			s.floor += 0.05
		}
		loud := db > math.Max(s.floor+12, -55)
		s.levels = append(s.levels, db)
		if loud {
			s.speech = true
			s.quietRun = 0
		} else {
			s.quietRun++
		}
		switch {
		case s.speech && s.quietRun >= pauseFrames && len(s.levels) >= minSegFrames:
			cuts = append(cuts, s.cut(len(s.levels)-s.quietRun/2))
		case !s.speech && len(s.levels) > 70:
			// Only quiet so far: let the start move along rather than send
			// seconds of nothing, keeping half a second before it as lead-in.
			drop := len(s.levels) - 17
			s.start += drop * frameBytes
			s.levels = append(s.levels[:0], s.levels[drop:]...)
		case len(s.levels) >= maxSegFrames:
			q, lo := len(s.levels)-1, math.Inf(1)
			for i := len(s.levels) - lookFrames; i < len(s.levels); i++ {
				if s.levels[i] < lo {
					q, lo = i, s.levels[i]
				}
			}
			cuts = append(cuts, s.cut(q+1))
		}
	}
	s.pending = append([]byte(nil), data[n:]...)
	return cuts
}

// cut ends the open segment after frame f (counted within it) and returns the
// byte offset; what follows becomes the new open segment.
func (s *Segmenter) cut(f int) int {
	at := s.start + f*frameBytes
	rest := append([]float64(nil), s.levels[f:]...)
	s.start, s.levels = at, rest
	s.speech = false
	for _, db := range rest {
		if db > math.Max(s.floor+12, -55) {
			s.speech = true
		}
	}
	s.quietRun = 0
	return at
}

// level is a frame's loudness in dBFS.
func level(frame []byte) float64 {
	var sum float64
	n := len(frame) / 2
	for i := 0; i < n; i++ {
		v := float64(int16(binary.LittleEndian.Uint16(frame[2*i:]))) / 32768
		sum += v * v
	}
	if sum == 0 {
		return -100
	}
	return 10 * math.Log10(sum/float64(n))
}
