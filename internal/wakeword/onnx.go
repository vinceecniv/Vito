package wakeword

// A small ONNX interpreter: just the operators the three networks of a wake
// word (openWakeWord's melspectrogram and speech embedding, plus the trained
// classifier) are built from, all in float32. It keeps Vito a single binary —
// ONNX Runtime would be a 15 MB library per platform for a few kB of maths.
//
// The protobuf is read field by field with a minimal wire decoder, so no
// generated code is needed either.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// Tensor is a dense float32 array. Integer and boolean tensors (shapes,
// axes, If conditions) are held as floats too; every value they carry in
// these graphs is small enough to be exact.
type Tensor struct {
	Shape []int
	Data  []float32
}

func newTensor(shape ...int) *Tensor {
	return &Tensor{Shape: append([]int(nil), shape...), Data: make([]float32, numel(shape))}
}

func numel(shape []int) int {
	n := 1
	for _, d := range shape {
		n *= d
	}
	return n
}

type attr struct {
	f      float32
	i      int64
	s      []byte
	t      *Tensor
	g      *graph
	floats []float32
	ints   []int64
}

type node struct {
	op      string
	inputs  []string
	outputs []string
	attrs   map[string]*attr
	stft    *stftSpec // set when this Conv is a windowed DFT (stft.go)
}

type graph struct {
	nodes   []*node
	inits   map[string]*Tensor
	inputs  []string
	outputs []string
	shapes  map[string][]int // declared input shapes; -1 for a symbolic dimension
}

// Model is a loaded ONNX graph.
type Model struct {
	g *graph
}

// LoadModel parses an ONNX model.
func LoadModel(b []byte) (*Model, error) {
	var g *graph
	err := fields(b, func(num int, wt int, v uint64, data []byte) error {
		if num == 7 && wt == 2 {
			var err error
			g, err = parseGraph(data)
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if g == nil {
		return nil, errors.New("onnx: no graph")
	}
	// Graph inputs that are also initializers are constants, not inputs.
	var in []string
	for _, n := range g.inputs {
		if _, ok := g.inits[n]; !ok {
			in = append(in, n)
		}
	}
	g.inputs = in
	g.detectSTFT()
	return &Model{g: g}, nil
}

// Inputs returns the names of the model's inputs.
func (m *Model) Inputs() []string { return m.g.inputs }

// InputShape returns an input's declared shape (-1 where it is symbolic).
func (m *Model) InputShape(name string) []int { return m.g.shapes[name] }

// Run evaluates the graph and returns its outputs in order.
func (m *Model) Run(inputs map[string]*Tensor) ([]*Tensor, error) {
	env := map[string]*Tensor{}
	for k, v := range inputs {
		env[k] = v
	}
	if err := m.g.run(env, nil); err != nil {
		return nil, err
	}
	out := make([]*Tensor, len(m.g.outputs))
	for i, n := range m.g.outputs {
		out[i] = env[n]
		if out[i] == nil {
			return nil, fmt.Errorf("onnx: output %q not computed", n)
		}
	}
	return out, nil
}

// ---- protobuf wire format ---------------------------------------------------

func fields(b []byte, fn func(num, wt int, v uint64, data []byte) error) error {
	for len(b) > 0 {
		key, n := binary.Uvarint(b)
		if n <= 0 {
			return errors.New("onnx: bad varint")
		}
		b = b[n:]
		num, wt := int(key>>3), int(key&7)
		var v uint64
		var data []byte
		switch wt {
		case 0:
			v, n = binary.Uvarint(b)
			if n <= 0 {
				return errors.New("onnx: bad varint")
			}
			b = b[n:]
		case 1:
			if len(b) < 8 {
				return errors.New("onnx: short fixed64")
			}
			v = binary.LittleEndian.Uint64(b)
			b = b[8:]
		case 2:
			l, n := binary.Uvarint(b)
			if n <= 0 || uint64(len(b)-n) < l {
				return errors.New("onnx: bad length")
			}
			data = b[n : n+int(l)]
			b = b[n+int(l):]
		case 5:
			if len(b) < 4 {
				return errors.New("onnx: short fixed32")
			}
			v = uint64(binary.LittleEndian.Uint32(b))
			b = b[4:]
		default:
			return fmt.Errorf("onnx: wire type %d", wt)
		}
		if err := fn(num, wt, v, data); err != nil {
			return err
		}
	}
	return nil
}

// packed or repeated varints
func appendVarints(dst []int64, wt int, v uint64, data []byte) []int64 {
	if wt == 0 {
		return append(dst, int64(v))
	}
	for len(data) > 0 {
		x, n := binary.Uvarint(data)
		if n <= 0 {
			break
		}
		dst = append(dst, int64(x))
		data = data[n:]
	}
	return dst
}

func appendFloats(dst []float32, wt int, v uint64, data []byte) []float32 {
	if wt == 5 {
		return append(dst, math.Float32frombits(uint32(v)))
	}
	for i := 0; i+4 <= len(data); i += 4 {
		dst = append(dst, math.Float32frombits(binary.LittleEndian.Uint32(data[i:])))
	}
	return dst
}

func parseGraph(b []byte) (*graph, error) {
	g := &graph{inits: map[string]*Tensor{}, shapes: map[string][]int{}}
	err := fields(b, func(num, wt int, v uint64, data []byte) error {
		switch num {
		case 1:
			n, err := parseNode(data)
			if err != nil {
				return err
			}
			g.nodes = append(g.nodes, n)
		case 5:
			name, t, err := parseTensor(data)
			if err != nil {
				return err
			}
			g.inits[name] = t
		case 11, 12:
			var name string
			var shape []int
			fields(data, func(num, wt int, v uint64, d []byte) error {
				switch num {
				case 1:
					name = string(d)
				case 2:
					shape = parseShape(d)
				}
				return nil
			})
			if num == 11 {
				g.inputs = append(g.inputs, name)
				g.shapes[name] = shape
			} else {
				g.outputs = append(g.outputs, name)
			}
		}
		return nil
	})
	return g, err
}

// parseShape reads TypeProto.tensor_type.shape.
func parseShape(b []byte) []int {
	var shape []int
	fields(b, func(num, wt int, v uint64, tt []byte) error {
		if num != 1 { // tensor_type
			return nil
		}
		return fields(tt, func(num, wt int, v uint64, sh []byte) error {
			if num != 2 { // shape
				return nil
			}
			return fields(sh, func(num, wt int, v uint64, dim []byte) error {
				if num != 1 {
					return nil
				}
				d := -1
				fields(dim, func(num, wt int, v uint64, _ []byte) error {
					if num == 1 {
						d = int(v)
					}
					return nil
				})
				shape = append(shape, d)
				return nil
			})
		})
	})
	return shape
}

func parseNode(b []byte) (*node, error) {
	n := &node{attrs: map[string]*attr{}}
	err := fields(b, func(num, wt int, v uint64, data []byte) error {
		switch num {
		case 1:
			n.inputs = append(n.inputs, string(data))
		case 2:
			n.outputs = append(n.outputs, string(data))
		case 4:
			n.op = string(data)
		case 5:
			name, a, err := parseAttr(data)
			if err != nil {
				return err
			}
			n.attrs[name] = a
		}
		return nil
	})
	return n, err
}

func parseAttr(b []byte) (string, *attr, error) {
	a := &attr{}
	var name string
	err := fields(b, func(num, wt int, v uint64, data []byte) error {
		switch num {
		case 1:
			name = string(data)
		case 2:
			a.f = math.Float32frombits(uint32(v))
		case 3:
			a.i = int64(v)
		case 4:
			a.s = data
		case 5:
			_, t, err := parseTensor(data)
			if err != nil {
				return err
			}
			a.t = t
		case 6:
			g, err := parseGraph(data)
			if err != nil {
				return err
			}
			a.g = g
		case 7:
			a.floats = appendFloats(a.floats, wt, v, data)
		case 8:
			a.ints = appendVarints(a.ints, wt, v, data)
		}
		return nil
	})
	return name, a, err
}

func parseTensor(b []byte) (string, *Tensor, error) {
	var (
		name   string
		dims   []int64
		dtype  int
		raw    []byte
		floats []float32
		ints   []int64
		hasRaw bool
	)
	err := fields(b, func(num, wt int, v uint64, data []byte) error {
		switch num {
		case 1:
			dims = appendVarints(dims, wt, v, data)
		case 2:
			dtype = int(v)
		case 4:
			floats = appendFloats(floats, wt, v, data)
		case 5, 7:
			ints = appendVarints(ints, wt, v, data)
		case 8:
			name = string(data)
		case 9:
			raw, hasRaw = data, true
		}
		return nil
	})
	if err != nil {
		return "", nil, err
	}
	shape := make([]int, len(dims))
	for i, d := range dims {
		shape[i] = int(d)
	}
	t := newTensor(shape...)
	n := len(t.Data)
	switch {
	case hasRaw:
		size := map[int]int{1: 4, 2: 1, 3: 1, 5: 2, 6: 4, 7: 8, 9: 1, 11: 8}[dtype]
		if size == 0 {
			return "", nil, fmt.Errorf("onnx: tensor %q: data type %d", name, dtype)
		}
		if len(raw) != n*size {
			return "", nil, fmt.Errorf("onnx: tensor %q: %d bytes for %d values", name, len(raw), n)
		}
		for i := 0; i < n; i++ {
			p := raw[i*size:]
			switch dtype {
			case 1:
				t.Data[i] = math.Float32frombits(binary.LittleEndian.Uint32(p))
			case 2, 9:
				t.Data[i] = float32(p[0])
			case 3:
				t.Data[i] = float32(int8(p[0]))
			case 5:
				t.Data[i] = float32(int16(binary.LittleEndian.Uint16(p)))
			case 6:
				t.Data[i] = float32(int32(binary.LittleEndian.Uint32(p)))
			case 7:
				t.Data[i] = float32(int64(binary.LittleEndian.Uint64(p)))
			case 11:
				t.Data[i] = float32(math.Float64frombits(binary.LittleEndian.Uint64(p)))
			}
		}
	case len(floats) > 0:
		copy(t.Data, floats)
	case len(ints) > 0:
		for i := 0; i < n && i < len(ints); i++ {
			t.Data[i] = float32(ints[i])
		}
	}
	return name, t, nil
}

// ---- evaluation -----------------------------------------------------------

func (g *graph) run(env map[string]*Tensor, outer map[string]*Tensor) error {
	get := func(name string) *Tensor {
		if name == "" {
			return nil
		}
		if t, ok := env[name]; ok {
			return t
		}
		if t, ok := g.inits[name]; ok {
			return t
		}
		if outer != nil {
			return outer[name]
		}
		return nil
	}
	for _, n := range g.nodes {
		in := make([]*Tensor, len(n.inputs))
		for i, name := range n.inputs {
			in[i] = get(name)
			if in[i] == nil && name != "" {
				return fmt.Errorf("onnx: %s: missing input %q", n.op, name)
			}
		}
		if n.op == "If" {
			branch := n.attrs["else_branch"]
			if in[0].Data[0] != 0 {
				branch = n.attrs["then_branch"]
			}
			scope := map[string]*Tensor{}
			for k, v := range outer {
				scope[k] = v
			}
			for k, v := range g.inits {
				scope[k] = v
			}
			for k, v := range env {
				scope[k] = v
			}
			sub := map[string]*Tensor{}
			if err := branch.g.run(sub, scope); err != nil {
				return err
			}
			for i, o := range n.outputs {
				env[o] = sub[branch.g.outputs[i]]
				if env[o] == nil {
					env[o] = branch.g.inits[branch.g.outputs[i]]
				}
			}
			continue
		}
		out, err := apply(n, in)
		if err != nil {
			return fmt.Errorf("onnx: %s (%s): %w", n.op, n.outputs[0], err)
		}
		for i, o := range n.outputs {
			if i < len(out) {
				env[o] = out[i]
			}
		}
	}
	return nil
}

func (n *node) int(name string, def int64) int64 {
	if a, ok := n.attrs[name]; ok {
		return a.i
	}
	return def
}

func (n *node) ints(name string) []int {
	a, ok := n.attrs[name]
	if !ok {
		return nil
	}
	r := make([]int, len(a.ints))
	for i, v := range a.ints {
		r[i] = int(v)
	}
	return r
}

func (n *node) float(name string, def float32) float32 {
	if a, ok := n.attrs[name]; ok {
		return a.f
	}
	return def
}

func toInts(t *Tensor) []int {
	r := make([]int, len(t.Data))
	for i, v := range t.Data {
		r[i] = int(v)
	}
	return r
}

func apply(n *node, in []*Tensor) ([]*Tensor, error) {
	one := func(t *Tensor, err error) ([]*Tensor, error) {
		if err != nil {
			return nil, err
		}
		return []*Tensor{t}, nil
	}
	switch n.op {
	case "Constant":
		if a, ok := n.attrs["value"]; ok && a.t != nil {
			return []*Tensor{a.t}, nil
		}
		if a, ok := n.attrs["value_float"]; ok {
			return []*Tensor{{Shape: []int{}, Data: []float32{a.f}}}, nil
		}
		if a, ok := n.attrs["value_ints"]; ok {
			t := newTensor(len(a.ints))
			for i, v := range a.ints {
				t.Data[i] = float32(v)
			}
			return []*Tensor{t}, nil
		}
		return nil, errors.New("unsupported constant")
	case "Identity":
		return []*Tensor{in[0]}, nil
	case "Add":
		return one(broadcast(in[0], in[1], func(a, b float32) float32 { return a + b }))
	case "Sub":
		return one(broadcast(in[0], in[1], func(a, b float32) float32 { return a - b }))
	case "Mul":
		return one(broadcast(in[0], in[1], func(a, b float32) float32 { return a * b }))
	case "Div":
		return one(broadcast(in[0], in[1], func(a, b float32) float32 { return a / b }))
	case "Pow":
		if len(in[1].Data) == 1 && in[1].Data[0] == 2 {
			return one(unary(in[0], func(a float32) float32 { return a * a }), nil)
		}
		return one(broadcast(in[0], in[1], func(a, b float32) float32 { return float32(math.Pow(float64(a), float64(b))) }))
	case "Max":
		t := in[0]
		for _, o := range in[1:] {
			var err error
			if t, err = broadcast(t, o, func(a, b float32) float32 {
				if a > b {
					return a
				}
				return b
			}); err != nil {
				return nil, err
			}
		}
		return []*Tensor{t}, nil
	case "GreaterOrEqual":
		return one(broadcast(in[0], in[1], func(a, b float32) float32 { return b2f(a >= b) }))
	case "Greater":
		return one(broadcast(in[0], in[1], func(a, b float32) float32 { return b2f(a > b) }))
	case "Log":
		return one(unary(in[0], func(a float32) float32 { return float32(math.Log(float64(a))) }), nil)
	case "Sqrt":
		return one(unary(in[0], func(a float32) float32 { return float32(math.Sqrt(float64(a))) }), nil)
	case "Exp":
		return one(unary(in[0], func(a float32) float32 { return float32(math.Exp(float64(a))) }), nil)
	case "Relu":
		return one(unary(in[0], func(a float32) float32 { return max(a, 0) }), nil)
	case "Sigmoid":
		return one(unary(in[0], func(a float32) float32 { return float32(1 / (1 + math.Exp(-float64(a)))) }), nil)
	case "Tanh":
		return one(unary(in[0], func(a float32) float32 { return float32(math.Tanh(float64(a))) }), nil)
	case "LeakyRelu":
		alpha := n.float("alpha", 0.01)
		return one(unary(in[0], func(a float32) float32 {
			if a < 0 {
				return a * alpha
			}
			return a
		}), nil)
	case "Cast":
		if n.int("to", 1) == 9 {
			return one(unary(in[0], func(a float32) float32 { return b2f(a != 0) }), nil)
		}
		return []*Tensor{in[0]}, nil
	case "Clip":
		lo, hi := float32(math.Inf(-1)), float32(math.Inf(1))
		if len(in) > 1 && in[1] != nil {
			lo = in[1].Data[0]
		}
		if len(in) > 2 && in[2] != nil {
			hi = in[2].Data[0]
		}
		return one(unary(in[0], func(a float32) float32 { return min(max(a, lo), hi) }), nil)
	case "Unsqueeze":
		axes := n.ints("axes")
		if len(in) > 1 {
			axes = toInts(in[1])
		}
		rank := len(in[0].Shape) + len(axes)
		shape := make([]int, 0, rank)
		ins := map[int]bool{}
		for _, a := range axes {
			if a < 0 {
				a += rank
			}
			ins[a] = true
		}
		j := 0
		for i := 0; i < rank; i++ {
			if ins[i] {
				shape = append(shape, 1)
			} else {
				shape = append(shape, in[0].Shape[j])
				j++
			}
		}
		return []*Tensor{{Shape: shape, Data: in[0].Data}}, nil
	case "Squeeze":
		axes := n.ints("axes")
		if len(in) > 1 {
			axes = toInts(in[1])
		}
		drop := map[int]bool{}
		for _, a := range axes {
			if a < 0 {
				a += len(in[0].Shape)
			}
			drop[a] = true
		}
		var shape []int
		for i, d := range in[0].Shape {
			if (len(axes) == 0 && d == 1) || drop[i] {
				continue
			}
			shape = append(shape, d)
		}
		return []*Tensor{{Shape: shape, Data: in[0].Data}}, nil
	case "Reshape":
		return one(reshape(in[0], toInts(in[1])))
	case "Flatten":
		axis := int(n.int("axis", 1))
		if axis < 0 {
			axis += len(in[0].Shape)
		}
		a := numel(in[0].Shape[:axis])
		return []*Tensor{{Shape: []int{a, len(in[0].Data) / max(a, 1)}, Data: in[0].Data}}, nil
	case "Transpose":
		return []*Tensor{transpose(in[0], n.ints("perm"))}, nil
	case "MatMul":
		return one(matmul(in[0], in[1]))
	case "Gemm":
		var c *Tensor
		if len(in) > 2 {
			c = in[2]
		}
		return one(gemm(in[0], in[1], c, n.float("alpha", 1), n.float("beta", 1), n.int("transA", 0) != 0, n.int("transB", 0) != 0))
	case "ReduceMax", "ReduceMean":
		axes := n.ints("axes")
		if len(in) > 1 && in[1] != nil {
			axes = toInts(in[1])
		}
		return []*Tensor{reduce(in[0], axes, n.int("keepdims", 1) != 0, n.op == "ReduceMean")}, nil
	case "Conv":
		var b *Tensor
		if len(in) > 2 {
			b = in[2]
		}
		if n.stft != nil && len(in[0].Shape) == 3 && in[0].Shape[0] == 1 && in[0].Shape[1] == 1 {
			return []*Tensor{n.stft.run(in[0])}, nil
		}
		return one(conv(in[0], in[1], b, n))
	case "MaxPool":
		return one(maxpool(in[0], n))
	}
	return nil, fmt.Errorf("operator %s not supported", n.op)
}

func b2f(b bool) float32 {
	if b {
		return 1
	}
	return 0
}

func unary(t *Tensor, f func(float32) float32) *Tensor {
	o := &Tensor{Shape: t.Shape, Data: make([]float32, len(t.Data))}
	for i, v := range t.Data {
		o.Data[i] = f(v)
	}
	return o
}

func strides(shape []int) []int {
	s := make([]int, len(shape))
	acc := 1
	for i := len(shape) - 1; i >= 0; i-- {
		s[i] = acc
		acc *= shape[i]
	}
	return s
}

func broadcast(a, b *Tensor, f func(x, y float32) float32) (*Tensor, error) {
	if len(b.Data) == 1 {
		o := &Tensor{Shape: a.Shape, Data: make([]float32, len(a.Data))}
		y := b.Data[0]
		for i, x := range a.Data {
			o.Data[i] = f(x, y)
		}
		if len(b.Shape) > len(a.Shape) {
			o.Shape = b.Shape
		}
		return o, nil
	}
	if len(a.Data) == 1 && len(b.Shape) >= len(a.Shape) {
		o := &Tensor{Shape: b.Shape, Data: make([]float32, len(b.Data))}
		x := a.Data[0]
		for i, y := range b.Data {
			o.Data[i] = f(x, y)
		}
		return o, nil
	}
	rank := max(len(a.Shape), len(b.Shape))
	pad := func(s []int) []int {
		p := make([]int, rank)
		for i := range p {
			p[i] = 1
		}
		copy(p[rank-len(s):], s)
		return p
	}
	as, bs := pad(a.Shape), pad(b.Shape)
	shape := make([]int, rank)
	for i := range shape {
		switch {
		case as[i] == bs[i] || bs[i] == 1:
			shape[i] = as[i]
		case as[i] == 1:
			shape[i] = bs[i]
		default:
			return nil, fmt.Errorf("cannot broadcast %v and %v", a.Shape, b.Shape)
		}
	}
	ast, bst, ost := strides(as), strides(bs), strides(shape)
	o := newTensor(shape...)
	for i := range o.Data {
		ai, bi, r := 0, 0, i
		for d := 0; d < rank; d++ {
			c := r / ost[d]
			r -= c * ost[d]
			if as[d] != 1 {
				ai += c * ast[d]
			}
			if bs[d] != 1 {
				bi += c * bst[d]
			}
		}
		o.Data[i] = f(a.Data[ai], b.Data[bi])
	}
	return o, nil
}

func reshape(t *Tensor, shape []int) (*Tensor, error) {
	s := make([]int, len(shape))
	infer, known := -1, 1
	for i, d := range shape {
		switch {
		case d == 0:
			s[i] = t.Shape[i]
		case d == -1:
			infer = i
			continue
		default:
			s[i] = d
		}
		known *= s[i]
	}
	if infer >= 0 {
		s[infer] = len(t.Data) / known
	}
	if numel(s) != len(t.Data) {
		return nil, fmt.Errorf("reshape %v to %v", t.Shape, shape)
	}
	return &Tensor{Shape: s, Data: t.Data}, nil
}

func transpose(t *Tensor, perm []int) *Tensor {
	r := len(t.Shape)
	if perm == nil {
		perm = make([]int, r)
		for i := range perm {
			perm[i] = r - 1 - i
		}
	}
	shape := make([]int, r)
	for i, p := range perm {
		shape[i] = t.Shape[p]
	}
	ist, ost := strides(t.Shape), strides(shape)
	o := newTensor(shape...)
	for i := range o.Data {
		src, rem := 0, i
		for d := 0; d < r; d++ {
			c := rem / ost[d]
			rem -= c * ost[d]
			src += c * ist[perm[d]]
		}
		o.Data[i] = t.Data[src]
	}
	return o
}

// matmul handles [..., M, K] x [K, N] and equal-batch [..., M, K] x [..., K, N].
func matmul(a, b *Tensor) (*Tensor, error) {
	if len(a.Shape) < 2 || len(b.Shape) < 2 {
		return nil, fmt.Errorf("matmul %v x %v", a.Shape, b.Shape)
	}
	M, K := a.Shape[len(a.Shape)-2], a.Shape[len(a.Shape)-1]
	K2, N := b.Shape[len(b.Shape)-2], b.Shape[len(b.Shape)-1]
	if K != K2 {
		return nil, fmt.Errorf("matmul %v x %v", a.Shape, b.Shape)
	}
	batch := len(a.Data) / (M * K)
	bBatch := len(b.Data) / (K * N)
	if bBatch != 1 && bBatch != batch {
		return nil, fmt.Errorf("matmul batch %v x %v", a.Shape, b.Shape)
	}
	shape := append(append([]int(nil), a.Shape[:len(a.Shape)-1]...), N)
	o := newTensor(shape...)
	for n := 0; n < batch; n++ {
		A := a.Data[n*M*K:]
		B := b.Data
		if bBatch > 1 {
			B = b.Data[n*K*N:]
		}
		O := o.Data[n*M*N:]
		for i := 0; i < M; i++ {
			row := O[i*N : i*N+N]
			for k := 0; k < K; k++ {
				av := A[i*K+k]
				if av == 0 {
					continue
				}
				br := B[k*N : k*N+N]
				for j := range row {
					row[j] += av * br[j]
				}
			}
		}
	}
	return o, nil
}

func gemm(a, b, c *Tensor, alpha, beta float32, ta, tb bool) (*Tensor, error) {
	if ta {
		a = transpose(a, []int{1, 0})
	}
	M, K := a.Shape[0], a.Shape[1]
	var N int
	if tb {
		N = b.Shape[0]
		if b.Shape[1] != K {
			return nil, fmt.Errorf("gemm %v x %vᵀ", a.Shape, b.Shape)
		}
	} else {
		N = b.Shape[1]
		if b.Shape[0] != K {
			return nil, fmt.Errorf("gemm %v x %v", a.Shape, b.Shape)
		}
	}
	o := newTensor(M, N)
	for i := 0; i < M; i++ {
		ar := a.Data[i*K : i*K+K]
		for j := 0; j < N; j++ {
			var s float32
			if tb {
				s = dot(ar, b.Data[j*K:j*K+K])
			} else {
				for k, av := range ar {
					s += av * b.Data[k*N+j]
				}
			}
			o.Data[i*N+j] = alpha * s
		}
	}
	if c != nil && beta != 0 {
		cb, err := broadcast(o, c, func(x, y float32) float32 { return x + beta*y })
		if err != nil {
			return nil, err
		}
		o = cb
	}
	return o, nil
}

func reduce(t *Tensor, axes []int, keep, mean bool) *Tensor {
	r := len(t.Shape)
	red := make([]bool, r)
	if len(axes) == 0 {
		for i := range red {
			red[i] = true
		}
	}
	for _, a := range axes {
		if a < 0 {
			a += r
		}
		red[a] = true
	}
	oshape := make([]int, r)
	for i, d := range t.Shape {
		oshape[i] = d
		if red[i] {
			oshape[i] = 1
		}
	}
	o := newTensor(oshape...)
	cnt := len(t.Data) / max(len(o.Data), 1)
	if !mean {
		for i := range o.Data {
			o.Data[i] = float32(math.Inf(-1))
		}
	}
	ist, ost := strides(t.Shape), strides(oshape)
	for i, v := range t.Data {
		oi, rem := 0, i
		for d := 0; d < r; d++ {
			c := rem / ist[d]
			rem -= c * ist[d]
			if !red[d] {
				oi += c * ost[d]
			}
		}
		if mean {
			o.Data[oi] += v
		} else if v > o.Data[oi] {
			o.Data[oi] = v
		}
	}
	if mean {
		for i := range o.Data {
			o.Data[i] /= float32(cnt)
		}
	}
	if !keep {
		var s []int
		for i, d := range oshape {
			if !red[i] {
				s = append(s, d)
			}
		}
		o.Shape = s
	}
	return o
}

// conv is a direct 1-D or 2-D convolution (NCL / NCHW), group 1.
func conv(x, w, b *Tensor, n *node) (*Tensor, error) {
	if n.int("group", 1) != 1 {
		return nil, errors.New("grouped convolution")
	}
	if ap := n.attrs["auto_pad"]; ap != nil && len(ap.s) > 0 && string(ap.s) != "NOTSET" {
		return nil, fmt.Errorf("auto_pad %s", ap.s)
	}
	oneD := len(x.Shape) == 3
	xs, ws := x.Shape, w.Shape
	if oneD {
		xs = []int{xs[0], xs[1], 1, xs[2]}
		ws = []int{ws[0], ws[1], 1, ws[2]}
	}
	N, C, H, W := xs[0], xs[1], xs[2], xs[3]
	OC, KH, KW := ws[0], ws[2], ws[3]
	if ws[1] != C {
		return nil, fmt.Errorf("conv channels %v * %v", x.Shape, w.Shape)
	}
	sh, sw, dh, dw := 1, 1, 1, 1
	pt, pl, pb, pr := 0, 0, 0, 0
	st, di, pa := n.ints("strides"), n.ints("dilations"), n.ints("pads")
	if oneD {
		if len(st) == 1 {
			sw = st[0]
		}
		if len(di) == 1 {
			dw = di[0]
		}
		if len(pa) == 2 {
			pl, pr = pa[0], pa[1]
		}
	} else {
		if len(st) == 2 {
			sh, sw = st[0], st[1]
		}
		if len(di) == 2 {
			dh, dw = di[0], di[1]
		}
		if len(pa) == 4 {
			pt, pl, pb, pr = pa[0], pa[1], pa[2], pa[3]
		}
	}
	OH := (H+pt+pb-dh*(KH-1)-1)/sh + 1
	OW := (W+pl+pr-dw*(KW-1)-1)/sw + 1
	o := newTensor(N, OC, OH, OW)
	if oneD && C == 1 && pl == 0 && pr == 0 && dw == 1 {
		// one window per output frame: a dot product per filter
		for nn := 0; nn < N; nn++ {
			sig := x.Data[nn*W : (nn+1)*W]
			for oc := 0; oc < OC; oc++ {
				wk := w.Data[oc*KW : (oc+1)*KW]
				out := o.Data[(nn*OC+oc)*OW : (nn*OC+oc+1)*OW]
				for ow := range out {
					out[ow] = dot(wk, sig[ow*sw:ow*sw+KW])
					if b != nil {
						out[ow] += b.Data[oc]
					}
				}
			}
		}
		o.Shape = []int{N, OC, OW}
		return o, nil
	}
	for nn := 0; nn < N; nn++ {
		for oc := 0; oc < OC; oc++ {
			out := o.Data[(nn*OC+oc)*OH*OW : (nn*OC+oc+1)*OH*OW]
			if b != nil {
				for i := range out {
					out[i] = b.Data[oc]
				}
			}
			for c := 0; c < C; c++ {
				img := x.Data[(nn*C+c)*H*W : (nn*C+c+1)*H*W]
				for kh := 0; kh < KH; kh++ {
					for kw := 0; kw < KW; kw++ {
						wv := w.Data[((oc*C+c)*KH+kh)*KW+kw]
						if wv == 0 {
							continue
						}
						for oh := 0; oh < OH; oh++ {
							ih := oh*sh + kh*dh - pt
							if ih < 0 || ih >= H {
								continue
							}
							row := img[ih*W : ih*W+W]
							orow := out[oh*OW : oh*OW+OW]
							iw := kw*dw - pl
							if sw == 1 {
								lo, hi := 0, OW
								if iw < 0 {
									lo = -iw
								}
								if iw+OW > W {
									hi = W - iw
								}
								src := row[iw+lo : iw+hi]
								dst := orow[lo:hi]
								for i, v := range src {
									dst[i] += wv * v
								}
								continue
							}
							for ow := 0; ow < OW; ow++ {
								p := iw + ow*sw
								if p >= 0 && p < W {
									orow[ow] += wv * row[p]
								}
							}
						}
					}
				}
			}
		}
	}
	if oneD {
		o.Shape = []int{N, OC, OW}
	}
	return o, nil
}

func maxpool(x *Tensor, n *node) (*Tensor, error) {
	if len(x.Shape) != 4 {
		return nil, fmt.Errorf("maxpool %v", x.Shape)
	}
	k := n.ints("kernel_shape")
	s := n.ints("strides")
	if s == nil {
		s = []int{1, 1}
	}
	p := n.ints("pads")
	if p == nil {
		p = []int{0, 0, 0, 0}
	}
	N, C, H, W := x.Shape[0], x.Shape[1], x.Shape[2], x.Shape[3]
	OH := (H+p[0]+p[2]-k[0])/s[0] + 1
	OW := (W+p[1]+p[3]-k[1])/s[1] + 1
	o := newTensor(N, C, OH, OW)
	for nc := 0; nc < N*C; nc++ {
		img := x.Data[nc*H*W:]
		out := o.Data[nc*OH*OW:]
		for oh := 0; oh < OH; oh++ {
			for ow := 0; ow < OW; ow++ {
				m := float32(math.Inf(-1))
				for kh := 0; kh < k[0]; kh++ {
					ih := oh*s[0] + kh - p[0]
					if ih < 0 || ih >= H {
						continue
					}
					for kw := 0; kw < k[1]; kw++ {
						iw := ow*s[1] + kw - p[1]
						if iw >= 0 && iw < W && img[ih*W+iw] > m {
							m = img[ih*W+iw]
						}
					}
				}
				out[oh*OW+ow] = m
			}
		}
	}
	return o, nil
}
