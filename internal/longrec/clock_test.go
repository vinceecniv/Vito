package longrec

import (
	"testing"

	"vito/internal/stt"
)

func TestSplitClockWords(t *testing.T) {
	c := clock{start: 10, end: 20, words: []stt.WordTime{{Off: 0, At: 10}, {Off: 4, At: 11}, {Off: 10, At: 15}, {Off: 15, At: 16.5}}, scale: 1}
	ps := splitClock("Een twee. Drie vier.", c)
	if len(ps) != 1 || len(ps[0].sents) != 2 {
		t.Fatalf("%+v", ps)
	}
	a, b := ps[0].sents[0], ps[0].sents[1]
	if a.Start != 10 || b.Start != 15 {
		t.Errorf("starts %v %v", a.Start, b.Start)
	}
	if len(a.W) != 2 || a.W[1] != 11 || len(b.W) != 2 || b.W[1] != 16.5 {
		t.Errorf("words %v %v", a.W, b.W)
	}
}
