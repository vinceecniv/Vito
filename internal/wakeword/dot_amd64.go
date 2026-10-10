package wakeword

import "golang.org/x/sys/cpu"

// Nearly all of the detector's time goes into dot products; with AVX2 and
// FMA eight multiply-adds happen at once.
var useAVX2 = cpu.X86.HasAVX2 && cpu.X86.HasFMA

//go:noescape
func dotAVX2(a, b *float32, n int) float32

func dot(a, b []float32) float32 {
	if useAVX2 && len(a) >= 16 {
		_ = b[len(a)-1]
		return dotAVX2(&a[0], &b[0], len(a))
	}
	return dotGo(a, b)
}
