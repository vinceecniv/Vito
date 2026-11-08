package pill

import "testing"

func TestResample(t *testing.T) {
	// A 4×2 picture, left half opaque white, right half clear.
	src := make([]byte, 4*2*4)
	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			copy(src[(y*4+x)*4:], []byte{255, 255, 255, 255})
		}
	}
	dst := make([]byte, 2*1*4)
	resample(dst, 2, 1, src, 4, 2)
	if dst[3] < 250 || dst[7] > 5 {
		t.Fatalf("halved: %v", dst)
	}
	// Scaled by 1.5 the edge blends, the ends keep their value.
	dst = make([]byte, 6*3*4)
	resample(dst, 6, 3, src, 4, 2)
	if dst[3] != 255 || dst[(6-1)*4+3] != 0 {
		t.Fatalf("ends: %v", dst[:24])
	}
	same := make([]byte, len(src))
	resample(same, 4, 2, src, 4, 2)
	if string(same) != string(src) {
		t.Fatal("same size changed")
	}
}
