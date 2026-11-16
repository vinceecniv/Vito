package pill

import "testing"

func TestCrossfadeEnds(t *testing.T) {
	from, to := []byte{255, 0, 40, 255}, []byte{0, 255, 200, 255}
	dst := make([]byte, 4)
	if got := crossfade(dst, from, to, 0); string(got) != string(from) {
		t.Errorf("k=0: %v, want %v", got, from)
	}
	if got := crossfade(dst, from, to, 1); string(got) != string(to) {
		t.Errorf("k=1: %v, want %v", got, to)
	}
	if got := crossfade(dst, from, to, 0.5); got[0] < 120 || got[0] > 135 || got[3] != 255 {
		t.Errorf("k=.5: %v", got)
	}
}
