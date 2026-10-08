package whistle

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode"
)

// TestEval measures Whistle on kept recordings against a reference transcript,
// whole (when a clip fits in one pass) and cut at pauses as the live path
// would. Manual: needs the engine and model and a reference file.
//
//	VITO_WHISTLE_EVAL=1 WHISTLE_BIN=…/needle.exe WHISTLE_MODEL=…/whistle.cact \
//	WHISTLE_REF=ref.json WHISTLE_REC=…/recordings go test ./internal/whistle -run TestEval -v
func TestEval(t *testing.T) {
	if os.Getenv("VITO_WHISTLE_EVAL") == "" {
		t.Skip("set VITO_WHISTLE_EVAL=1")
	}
	e := Engine{Bin: os.Getenv("WHISTLE_BIN"), Model: os.Getenv("WHISTLE_MODEL")}
	var ref map[string]struct {
		Raw      string `json:"raw"`
		Language string `json:"language"`
	}
	b, err := os.ReadFile(os.Getenv("WHISTLE_REF"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &ref); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var totW, totErrWhole, totWWhole, totErrSeg, totAudio float64
	var totTimeSeg time.Duration
	for id, r := range ref {
		wav, err := os.ReadFile(filepath.Join(os.Getenv("WHISTLE_REC"), id+".wav"))
		if err != nil {
			continue
		}
		pcm := wav[44:]
		secs := float64(len(pcm)) / (SampleRate * 2)
		refW := words(r.Raw)
		// Cut at pauses, fed in 100 ms chunks as the microphone would.
		seg := NewSegmenter()
		var cuts []int
		for off := 0; off < len(pcm); off += 3200 {
			end := min(off+3200, len(pcm))
			cuts = append(cuts, seg.Add(pcm[off:end])...)
		}
		cuts = append(cuts, len(pcm))
		var parts []string
		prev, t0 := 0, time.Now()
		for _, c := range cuts {
			if c-prev < frameBytes {
				continue
			}
			res, err := e.Transcribe(ctx, pcm[prev:c], "nl", nil)
			if err != nil {
				t.Fatalf("%s: %v", id, err)
			}
			parts = append(parts, res.Text)
			prev = c
		}
		segTime := time.Since(t0)
		segText := strings.Join(parts, " ")
		segErr := wer(refW, words(segText))
		line := fmt.Sprintf("%s %5.1fs  %2d segs  seg WER %4.1f%% in %4.1fs", id, secs, len(cuts), 100*segErr/float64(max(1, len(refW))), segTime.Seconds())
		if secs <= MaxSeconds {
			t1 := time.Now()
			res, err := e.Transcribe(ctx, pcm, "nl", nil)
			if err != nil {
				t.Fatalf("%s whole: %v", id, err)
			}
			we := wer(refW, words(res.Text))
			line += fmt.Sprintf("  | whole WER %4.1f%% in %4.1fs", 100*we/float64(max(1, len(refW))), time.Since(t1).Seconds())
			totErrWhole += we
			totWWhole += float64(len(refW))
		}
		t.Log(line)
		if os.Getenv("WHISTLE_SHOW") != "" {
			t.Logf("  ref: %s\n  seg: %s", r.Raw, segText)
		}
		totW += float64(len(refW))
		totErrSeg += segErr
		totAudio += secs
		totTimeSeg += segTime
	}
	t.Logf("TOTAL %.0fs audio: seg WER %.1f%% (%.1fs, %.2fx realtime)  whole WER %.1f%% (clips ≤30s)",
		totAudio, 100*totErrSeg/totW, totTimeSeg.Seconds(), totTimeSeg.Seconds()/totAudio, 100*totErrWhole/max(1, totWWhole))
}

// words lowercases and strips punctuation, so the score is about the words.
func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

// wer is the word-level edit distance.
func wer(a, b []string) float64 {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			c := 1
			if a[i-1] == b[j-1] {
				c = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+c)
		}
		prev = cur
	}
	return float64(prev[len(b)])
}
