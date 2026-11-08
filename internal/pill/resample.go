package pill

import "math"

// resample scales a premultiplied BGRA picture sw×sh to dw×dh: going down it
// averages, going up it blends. The
// pictures are drawn at twice their logical size; a desktop that cannot scale a
// window itself (Windows) gets them at the screen's own DPI this way.
func resample(dst []byte, dw, dh int, src []byte, sw, sh int) {
	if dw == sw && dh == sh {
		copy(dst, src)
		return
	}
	xs, ys := taps(sw, dw), taps(sh, dh)
	mid := make([]float32, sh*dw*4)
	for y := 0; y < sh; y++ {
		row := src[y*sw*4:]
		for x, ts := range xs {
			var b, g, r, a float32
			for _, t := range ts {
				p := row[t.i*4:]
				b += float32(p[0]) * t.w
				g += float32(p[1]) * t.w
				r += float32(p[2]) * t.w
				a += float32(p[3]) * t.w
			}
			o := (y*dw + x) * 4
			mid[o], mid[o+1], mid[o+2], mid[o+3] = b, g, r, a
		}
	}
	for y, ts := range ys {
		for x := 0; x < dw; x++ {
			var c [4]float32
			for _, t := range ts {
				p := mid[(t.i*dw+x)*4:]
				for k := range c {
					c[k] += p[k] * t.w
				}
			}
			o := (y*dw + x) * 4
			for k := range c {
				dst[o+k] = byte(min(255, max(0, c[k]+0.5)))
			}
		}
	}
}

type tap struct {
	i int
	w float32
}

// taps gives each of n output pixels the source pixels it takes from: going
// down, each by how much of the output pixel it covers; going up, a blend of
// the two nearest.
func taps(from, n int) [][]tap {
	ratio := float64(from) / float64(n)
	out := make([][]tap, n)
	for i := range out {
		var ts []tap
		if ratio >= 1 {
			a, b := float64(i)*ratio, float64(i+1)*ratio
			for j := int(a); j < from && float64(j) < b; j++ {
				if w := math.Min(b, float64(j+1)) - math.Max(a, float64(j)); w > 1e-6 {
					ts = append(ts, tap{j, float32(w / ratio)})
				}
			}
		} else {
			c := (float64(i)+0.5)*ratio - 0.5
			j := int(math.Floor(c))
			f := float32(c - float64(j))
			lo, hi := min(max(j, 0), from-1), min(max(j+1, 0), from-1)
			ts = []tap{{lo, 1 - f}, {hi, f}}
		}
		out[i] = ts
	}
	return out
}
