package wakeword

// The melspectrogram network computes its short-time Fourier transform as two
// convolutions whose filters are a window times cosines and sines — 257
// filters of 512 taps, half a million multiplications per frame. An FFT gives
// the same numbers for a twentieth of the work. At load time each such Conv
// is checked against the filters it would be (to within float rounding) and,
// only if it matches, marked to run as an FFT.

import (
	"math"
	"math/cmplx"
)

type stftSpec struct {
	n      int // frame length (power of two)
	stride int
	win    []float64
	imag   bool
	sign   float64 // the imaginary filters may be +sin or -sin
}

// detectSTFT marks Conv nodes that are a windowed DFT. The cosine filters
// carry the window outright (frequency 0); the sine filters are checked
// against the window of a cosine twin.
func (g *graph) detectSTFT() {
	type cand struct {
		nd   *node
		w    *Tensor
		K, N int
	}
	var cands []cand
	for _, nd := range g.nodes {
		if nd.op != "Conv" || len(nd.inputs) != 2 {
			continue
		}
		w := g.inits[nd.inputs[1]]
		if w == nil || len(w.Shape) != 3 || w.Shape[1] != 1 {
			continue
		}
		K, N := w.Shape[0], w.Shape[2]
		if N&(N-1) != 0 || K != N/2+1 || nd.int("group", 1) != 1 || len(nd.ints("strides")) != 1 {
			continue
		}
		if p := nd.ints("pads"); len(p) == 2 && (p[0] != 0 || p[1] != 0) {
			continue
		}
		if d := nd.ints("dilations"); len(d) == 1 && d[0] != 1 {
			continue
		}
		cands = append(cands, cand{nd, w, K, N})
	}
	var windows [][]float64
	for _, c := range cands {
		win := make([]float64, c.N)
		for t := range win {
			win[t] = float64(c.w.Data[t])
		}
		if s := matchDFT(c.w.Data, c.K, c.N, win, false, 1); s != nil {
			s.stride = c.nd.ints("strides")[0]
			c.nd.stft = s
			windows = append(windows, win)
		}
	}
	for _, c := range cands {
		if c.nd.stft != nil {
			continue
		}
		for _, win := range windows {
			if len(win) != c.N {
				continue
			}
			for _, sign := range []float64{1, -1} {
				if s := matchDFT(c.w.Data, c.K, c.N, win, true, sign); s != nil {
					s.stride = c.nd.ints("strides")[0]
					c.nd.stft = s
					break
				}
			}
			if c.nd.stft != nil {
				break
			}
		}
	}
}

// matchDFT checks w[k][t] = win[t]·cos(2πkt/N), or sign·win[t]·sin(2πkt/N).
func matchDFT(w []float32, K, N int, win []float64, imag bool, sign float64) *stftSpec {
	var scale float64
	for _, v := range w {
		scale = math.Max(scale, math.Abs(float64(v)))
	}
	if scale == 0 {
		return nil
	}
	tol := 1e-5 * scale
	for k := 0; k < K; k++ {
		for t := 0; t < N; t++ {
			a := 2 * math.Pi * float64(k*t%N) / float64(N)
			want := win[t] * math.Cos(a)
			if imag {
				want = sign * win[t] * math.Sin(a)
			}
			if math.Abs(float64(w[k*N+t])-want) > tol {
				return nil
			}
		}
	}
	return &stftSpec{n: N, win: win, imag: imag, sign: sign}
}

// run computes the conv output [1, K, frames] for a [1, 1, L] signal.
func (s *stftSpec) run(x *Tensor) *Tensor {
	L := x.Shape[len(x.Shape)-1]
	frames := (L-s.n)/s.stride + 1
	K := s.n/2 + 1
	o := newTensor(1, K, frames)
	buf := make([]complex128, s.n)
	for f := 0; f < frames; f++ {
		seg := x.Data[f*s.stride : f*s.stride+s.n]
		for t, v := range seg {
			buf[t] = complex(float64(v)*s.win[t], 0)
		}
		fft(buf)
		for k := 0; k < K; k++ {
			// conv computes Σ x·win·cos(2πkt/N) and Σ x·win·sin(2πkt/N);
			// the DFT is Σ x·win·e^(−2πikt/N) = cos part − i·sin part
			v := real(buf[k])
			if s.imag {
				v = -imag(buf[k]) * s.sign
			}
			o.Data[k*frames+f] = float32(v)
		}
	}
	return o
}

// fft is an in-place iterative radix-2 transform; len(a) is a power of two.
func fft(a []complex128) {
	n := len(a)
	for i, j := 1, 0; i < n; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j ^= bit
		if i < j {
			a[i], a[j] = a[j], a[i]
		}
	}
	for size := 2; size <= n; size <<= 1 {
		w := cmplx.Exp(complex(0, -2*math.Pi/float64(size)))
		for start := 0; start < n; start += size {
			wk := complex(1, 0)
			for k := 0; k < size/2; k++ {
				u, v := a[start+k], a[start+k+size/2]*wk
				a[start+k], a[start+k+size/2] = u+v, u-v
				wk *= w
			}
		}
	}
}
