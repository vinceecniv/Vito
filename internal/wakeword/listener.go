package wakeword

import (
	"embed"
	"encoding/binary"
	"fmt"
	"log/slog"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"vito/internal/audio"
)

//go:embed models/*.onnx
var modelFS embed.FS

// Phrases lists the wake phrases Vito ships a classifier for, by model name
// ("hey_vito", "oke_vito").
func Phrases() []string {
	ents, _ := modelFS.ReadDir("models")
	var out []string
	for _, e := range ents {
		n := strings.TrimSuffix(e.Name(), ".onnx")
		if n != "melspectrogram" && n != "embedding_model" {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

func classifierFor(phrase string) ([]byte, error) {
	b, err := modelFS.ReadFile(path.Join("models", phrase+".onnx"))
	if err != nil {
		return nil, fmt.Errorf("wakeword: no model for %q", phrase)
	}
	return b, nil
}

// DefaultThreshold is the score at which a phrase counts as heard.
const DefaultThreshold = 0.5

// Threshold maps the user's sensitivity (0–1, 0.5 the default; higher
// reacts sooner) to a score threshold.
func Threshold(sensitivity float64) float32 {
	if sensitivity <= 0 || sensitivity >= 1 {
		return DefaultThreshold
	}
	// 0.5 → 0.5; 1 → 0.1 (eager); 0 → 0.9 (strict)
	return float32(0.9 - 0.8*sensitivity)
}

// Listener keeps a microphone open and calls OnWake when the phrase is
// heard. Everything stays on this machine: the audio goes nowhere but the
// detector, and none of it is kept.
type Listener struct {
	log      *slog.Logger
	audioCtx *audio.Context
	// OnWake is called (on its own goroutine) when the phrase is heard.
	OnWake func(score float32)
	// OnScore, when set, gets the highest score of every ~200 ms, for a live
	// meter in the settings.
	OnScore func(score float32)

	mu      sync.Mutex
	cap     *audio.Capture
	chunks  chan []byte
	done    chan struct{}
	running string // the phrase being listened for; "" when stopped
}

func NewListener(log *slog.Logger, audioCtx *audio.Context) *Listener {
	return &Listener{log: log, audioCtx: audioCtx}
}

// Running reports the phrase being listened for, or "".
func (l *Listener) Running() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.running
}

// Start opens the microphone and listens for phrase. A running listener is
// restarted.
func (l *Listener) Start(device string, gain float64, phrase string, threshold float32) error {
	cls, err := classifierFor(phrase)
	if err != nil {
		return err
	}
	det, err := NewDetector(cls)
	if err != nil {
		return err
	}
	l.Stop()
	chunks := make(chan []byte, 64)
	done := make(chan struct{})
	c, err := audio.StartCapture(l.audioCtx, device, gain, func(b []byte) {
		select {
		case chunks <- b:
		default: // the detector fell behind: drop rather than block the audio thread
		}
	})
	if err != nil {
		return fmt.Errorf("wakeword: open microphone: %w", err)
	}
	l.mu.Lock()
	l.cap, l.chunks, l.done, l.running = c, chunks, done, phrase
	l.mu.Unlock()
	go l.loop(det, chunks, done, threshold)
	l.log.Info("wake word listening", "phrase", phrase, "threshold", threshold)
	return nil
}

// Stop closes the microphone. Safe to call when not running.
func (l *Listener) Stop() {
	l.mu.Lock()
	c, chunks, done := l.cap, l.chunks, l.done
	l.cap, l.chunks, l.done, l.running = nil, nil, nil, ""
	l.mu.Unlock()
	if c == nil {
		return
	}
	c.Stop() // no more callbacks after this returns
	close(chunks)
	<-done
	l.log.Info("wake word stopped")
}

// refractory keeps one utterance from waking Vito twice.
const refractory = 2 * time.Second

func (l *Listener) loop(det *Detector, chunks <-chan []byte, done chan<- struct{}, threshold float32) {
	defer close(done)
	var (
		pcm      []int16
		last     time.Time
		peak     float32
		lastMeas = time.Now()
	)
	for b := range chunks {
		pcm = pcm[:0]
		for i := 0; i+1 < len(b); i += 2 {
			pcm = append(pcm, int16(binary.LittleEndian.Uint16(b[i:])))
		}
		scores, err := det.Write(pcm)
		if err != nil {
			l.log.Error("wake word detector", "err", err)
			continue
		}
		for _, s := range scores {
			peak = max(peak, s)
			if s >= threshold && time.Since(last) >= refractory {
				last = time.Now()
				l.log.Info("wake word heard", "score", s)
				if l.OnWake != nil {
					go l.OnWake(s)
				}
			}
		}
		if l.OnScore != nil && time.Since(lastMeas) >= 200*time.Millisecond {
			l.OnScore(peak)
			peak, lastMeas = 0, time.Now()
		}
	}
}
