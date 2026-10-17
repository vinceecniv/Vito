package wakeword

// The speech embedding looks at 76 melspectrogram frames, and every 80 ms
// that window moves on by 8 frames. Its network only convolves validly along
// time and pools time by 2 three times (8 in all), so after a move of 8
// frames every layer's activations are the previous ones shifted by a whole
// number of rows: only the newest rows need computing. That cuts the work
// per step about eightfold compared with running the graph on the full
// window, which is what keeps listening cheap enough to leave on.
//
// Each layer keeps its activations in a ring of time rows, so moving on is
// a change of offset rather than a copy.
//
// The network is read from the same ONNX graph, as a chain of Conv,
// LeakyRelu, Max (with a constant) and MaxPool; newStreamEmbedding refuses a
// graph of any other shape, and the detector then runs it whole.

import "fmt"

type postOp struct {
	leaky bool    // LeakyRelu with alpha v; otherwise Max(x, v)
	v     float32 //
}

type sLayer struct {
	pool bool
	// conv
	w, b       []float32
	kh, kw     int
	padL, padR int
	// pool
	ph, pw, sh, sw int
	post           []postOp // element-wise ops folded into this layer

	C, T, F int
	act     []float32 // [C][T][F]; time rows form a ring starting at head
	head    int
}

func (l *sLayer) row(c, t int) []float32 {
	p := (l.head + t) % l.T
	return l.act[(c*l.T+p)*l.F : (c*l.T+p+1)*l.F]
}

type streamEmbedding struct {
	in     *sLayer // the mel window, one channel
	layers []*sLayer
	primed bool
}

func newStreamEmbedding(m *Model) (*streamEmbedding, error) {
	g := m.g
	if len(g.inputs) != 1 || len(g.nodes) < 3 || g.nodes[0].op != "Reshape" {
		return nil, fmt.Errorf("not a chain")
	}
	in := &sLayer{C: 1, T: melWindow, F: melBands, act: make([]float32, melWindow*melBands)}
	s := &streamEmbedding{in: in}
	prev := g.nodes[0].outputs[0]
	C, T, F := 1, in.T, in.F
	last := in
	for _, n := range g.nodes[1:] {
		if n.inputs[0] != prev {
			return nil, fmt.Errorf("%s does not follow the chain", n.op)
		}
		prev = n.outputs[0]
		l := &sLayer{}
		switch n.op {
		case "Conv":
			w := g.inits[n.inputs[1]]
			if w == nil || len(w.Shape) != 4 || w.Shape[1] != C || n.int("group", 1) != 1 {
				return nil, fmt.Errorf("conv weights")
			}
			for _, v := range append(n.ints("strides"), n.ints("dilations")...) {
				if v != 1 {
					return nil, fmt.Errorf("strided or dilated conv")
				}
			}
			if p := n.ints("pads"); len(p) == 4 {
				if p[0] != 0 || p[2] != 0 {
					return nil, fmt.Errorf("time padding")
				}
				l.padL, l.padR = p[1], p[3]
			}
			l.w, l.kh, l.kw = w.Data, w.Shape[2], w.Shape[3]
			if len(n.inputs) > 2 {
				l.b = g.inits[n.inputs[2]].Data
			}
			C, T, F = w.Shape[0], T-l.kh+1, F+l.padL+l.padR-l.kw+1
		case "LeakyRelu", "Max":
			op := postOp{leaky: true, v: n.float("alpha", 0.01)}
			if n.op == "Max" {
				c := g.inits[n.inputs[1]]
				if c == nil || len(c.Data) != 1 || len(n.inputs) != 2 {
					return nil, fmt.Errorf("max")
				}
				op = postOp{v: c.Data[0]}
			}
			if last == in {
				return nil, fmt.Errorf("element-wise op on the input")
			}
			last.post = append(last.post, op)
			continue
		case "MaxPool":
			k, st := n.ints("kernel_shape"), n.ints("strides")
			for _, v := range n.ints("pads") {
				if v != 0 {
					return nil, fmt.Errorf("padded pool")
				}
			}
			if len(k) != 2 || len(st) != 2 {
				return nil, fmt.Errorf("pool")
			}
			l.pool, l.ph, l.pw, l.sh, l.sw = true, k[0], k[1], st[0], st[1]
			T, F = (T-l.ph)/l.sh+1, (F-l.pw)/l.sw+1
		case "Reshape":
			if n.outputs[0] != g.outputs[0] || C*T*F != embDim {
				return nil, fmt.Errorf("output %d×%d×%d", C, T, F)
			}
			return s, nil
		default:
			return nil, fmt.Errorf("op %s", n.op)
		}
		l.C, l.T, l.F = C, T, F
		l.act = make([]float32, C*T*F)
		s.layers = append(s.layers, l)
		last = l
	}
	return nil, fmt.Errorf("no output reshape")
}

// push takes the whole mel window, of which the last newFrames rows are new,
// and returns the embedding (shared storage; copy it to keep it).
func (s *streamEmbedding) push(window []float32, newFrames int) []float32 {
	in := s.in
	k := newFrames
	if !s.primed || k >= in.T {
		copy(in.act, window)
		in.head, k = 0, in.T
		s.primed = true
	} else {
		in.head = (in.head + k) % in.T
		for t := in.T - k; t < in.T; t++ {
			copy(in.row(0, t), window[t*in.F:(t+1)*in.F])
		}
	}
	prev := in
	for _, l := range s.layers {
		ko := k
		if l.pool {
			ko = k / l.sh
		}
		if k >= prev.T || ko >= l.T {
			ko, l.head = l.T, 0
		} else {
			l.head = (l.head + ko) % l.T
		}
		from := l.T - ko
		if l.pool {
			poolRows(prev, l, from)
		} else {
			convRows(prev, l, from)
		}
		if len(l.post) > 0 {
			for c := 0; c < l.C; c++ {
				for t := from; t < l.T; t++ {
					r := l.row(c, t)
					for _, op := range l.post {
						if op.leaky {
							for i, v := range r {
								if v < 0 {
									r[i] = v * op.v
								}
							}
						} else {
							for i, v := range r {
								r[i] = max(v, op.v)
							}
						}
					}
				}
			}
		}
		prev, k = l, ko
	}
	return prev.act
}

func convRows(in, l *sLayer, from int) {
	OF := l.F
	K := in.C * l.kh * l.kw
	n := (l.T - from) * OF
	// im2col: one contiguous patch per output position, so each output is a
	// single dot product with the filter
	patch := make([]float32, n*K)
	for t := from; t < l.T; t++ {
		for c := 0; c < in.C; c++ {
			for kh := 0; kh < l.kh; kh++ {
				row := in.row(c, t+kh)
				base := (c*l.kh + kh) * l.kw
				for f := 0; f < OF; f++ {
					p := patch[((t-from)*OF+f)*K+base:]
					for kw := 0; kw < l.kw; kw++ {
						if x := f + kw - l.padL; x >= 0 && x < in.F {
							p[kw] = row[x]
						}
					}
				}
			}
		}
	}
	for oc := 0; oc < l.C; oc++ {
		w := l.w[oc*K : (oc+1)*K]
		var bias float32
		if l.b != nil {
			bias = l.b[oc]
		}
		for t := from; t < l.T; t++ {
			r := l.row(oc, t)
			pt := patch[(t-from)*OF*K:]
			for f := range r {
				r[f] = bias + dot(w, pt[f*K:(f+1)*K])
			}
		}
	}
}

func poolRows(in, l *sLayer, from int) {
	for c := 0; c < l.C; c++ {
		for t := from; t < l.T; t++ {
			out := l.row(c, t)
			for f := range out {
				m := in.row(c, t*l.sh)[f*l.sw]
				for a := 0; a < l.ph; a++ {
					r := in.row(c, t*l.sh+a)
					for b := 0; b < l.pw; b++ {
						m = max(m, r[f*l.sw+b])
					}
				}
				out[f] = m
			}
		}
	}
}
