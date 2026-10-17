package wakeword

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// ScoreWAV runs a recording through the detector for phrase and returns one
// score per 80 ms step, for testing a model on one's own voice
// (`vito wake-test`). The WAV must be 16-bit PCM; other rates are resampled
// linearly, extra channels dropped. A second of silence goes before and after,
// so a phrase at the very start or end still gets its full window.
func ScoreWAV(phrase string, wav []byte) ([]float32, error) {
	cls, err := classifierFor(phrase)
	if err != nil {
		return nil, err
	}
	pcm, err := decodeWAV(wav)
	if err != nil {
		return nil, err
	}
	d, err := NewDetector(cls)
	if err != nil {
		return nil, err
	}
	pad := make([]int16, 16000)
	var out []float32
	for _, part := range [][]int16{pad, pcm, pad} {
		s, err := d.Write(part)
		if err != nil {
			return nil, err
		}
		out = append(out, s...)
	}
	return out, nil
}

func decodeWAV(b []byte) ([]int16, error) {
	if len(b) < 12 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return nil, errors.New("not a WAV file")
	}
	var (
		rate     int
		channels int
		bits     int
		data     []byte
	)
	for p := 12; p+8 <= len(b); {
		id, size := string(b[p:p+4]), int(binary.LittleEndian.Uint32(b[p+4:]))
		body := b[p+8:]
		if size > len(body) {
			size = len(body)
		}
		switch id {
		case "fmt ":
			if size < 16 {
				return nil, errors.New("bad fmt chunk")
			}
			if f := binary.LittleEndian.Uint16(body); f != 1 && f != 0xFFFE {
				return nil, fmt.Errorf("WAV format %d: need 16-bit PCM", f)
			}
			channels = int(binary.LittleEndian.Uint16(body[2:]))
			rate = int(binary.LittleEndian.Uint32(body[4:]))
			bits = int(binary.LittleEndian.Uint16(body[14:]))
		case "data":
			data = body[:size]
		}
		p += 8 + size + size%2
	}
	if bits != 16 || channels < 1 || rate <= 0 || data == nil {
		return nil, fmt.Errorf("need 16-bit PCM (got %d-bit, %d channels)", bits, channels)
	}
	n := len(data) / (2 * channels)
	mono := make([]int16, n)
	for i := range mono {
		mono[i] = int16(binary.LittleEndian.Uint16(data[i*2*channels:]))
	}
	if rate == 16000 {
		return mono, nil
	}
	out := make([]int16, int(int64(n)*16000/int64(rate)))
	for i := range out {
		x := float64(i) * float64(rate) / 16000
		j := int(x)
		f := x - float64(j)
		a, c := float64(mono[min(j, n-1)]), float64(mono[min(j+1, n-1)])
		out[i] = int16(a + (c-a)*f)
	}
	return out, nil
}
