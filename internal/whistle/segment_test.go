package whistle

import (
	"encoding/binary"
	"math"
	"testing"
)

// pcm makes s16le audio: a sine of the given amplitude (0 = silence).
func pcm(seconds, amp float64) []byte {
	n := int(seconds * SampleRate)
	b := make([]byte, 2*n)
	for i := range n {
		v := amp * math.Sin(2*math.Pi*220*float64(i)/SampleRate)
		binary.LittleEndian.PutUint16(b[2*i:], uint16(int16(v*32767)))
	}
	return b
}

func TestSegmenterIgnoresClicks(t *testing.T) {
	s := NewSegmenter()
	s.Add(pcm(1, 0))
	// Two clicks of 60 ms each: sound, but not speech.
	s.Add(pcm(0.06, 0.5))
	s.Add(pcm(0.5, 0))
	s.Add(pcm(0.06, 0.5))
	s.Add(pcm(0.5, 0))
	if s.HasSpeech() {
		t.Fatal("two clicks counted as speech")
	}
	s.Add(pcm(1, 0.3))
	if !s.HasSpeech() {
		t.Fatal("a second of sound did not count as speech")
	}
}
