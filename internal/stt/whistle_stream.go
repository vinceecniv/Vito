package stt

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"vito/internal/config"
	"vito/internal/whistle"
)

// WhistleEngine hands the stt package the installed Whistle engine; the daemon
// sets it to its whistle.Manager. Nil, or false, means not installed.
var WhistleEngine func() (whistle.Engine, bool)

var errWhistleMissing = errors.New("the Whistle model isn't downloaded yet — download it under Settings → Speech recognition")

func whistleEngine() (whistle.Engine, error) {
	if WhistleEngine == nil {
		return whistle.Engine{}, errWhistleMissing
	}
	e, ok := WhistleEngine()
	if !ok {
		return whistle.Engine{}, errWhistleMissing
	}
	return e, nil
}

// whistleLang is the configured language when Whistle knows it, else "" for
// it to detect.
func whistleLang(cfg config.STT) string {
	if whistle.Languages[cfg.Language] {
		return cfg.Language
	}
	return ""
}

// whistleStream transcribes while you speak with a model that only takes
// whole clips of up to 30 s: the recording is cut at its pauses
// (whistle.Segmenter), each finished segment is transcribed once, and the
// segment still being spoken is transcribed again every so often for the live
// text. One worker does the transcribing, finished segments first, so the
// final text is ready moments after you stop.
type whistleStream struct {
	eng       whistle.Engine
	engErr    error
	lang      string
	keywords  []string
	log       *slog.Logger
	onPartial func(string)

	mu       sync.Mutex
	wake     *sync.Cond
	pcm      []byte
	seg      *whistle.Segmenter
	cutAt    int      // end of the last finished segment queued
	queue    [][2]int // finished segments waiting, as byte ranges
	texts    []string // transcripts of finished segments, in order
	openText string   // the latest look at the open segment
	lastLook time.Time
	looking  bool // a look at the open segment is due or running
	busy     bool
	finished bool
	aborted  bool
	langSeen string
	err      error
	idle     chan struct{}
}

// A look at the open segment waits until it holds 1.5 s: on a sliver of sound
// the model tends to make up a polite phrase ("Dank u wel.") that the next look
// takes back, which flickers.
const whistleLookEvery = 1200 * time.Millisecond

func newWhistleStream(cfg config.STT, keyterms []string, log *slog.Logger, onPartial func(string)) *whistleStream {
	s := &whistleStream{lang: whistleLang(cfg), keywords: keyterms, log: log, onPartial: onPartial, seg: whistle.NewSegmenter()}
	s.eng, s.engErr = whistleEngine()
	s.wake = sync.NewCond(&s.mu)
	if s.engErr == nil {
		go s.work()
	}
	return s
}

func (s *whistleStream) Send(pcm []byte) {
	if s.engErr != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished || s.aborted {
		return
	}
	s.pcm = append(s.pcm, pcm...)
	for _, c := range s.seg.Add(pcm) {
		if c > s.cutAt {
			s.queue = append(s.queue, [2]int{s.cutAt, c})
			s.cutAt = c
		}
	}
	open := len(s.pcm) - s.seg.Start()
	if !s.looking && s.seg.HasSpeech() && open >= whistle.SampleRate*2*3/2 && time.Since(s.lastLook) >= whistleLookEvery {
		s.looking = true
	}
	s.wake.Signal()
}

// work runs one transcription at a time: finished segments in order, and in
// between, when nothing is waiting, a look at the open segment.
func (s *whistleStream) work() {
	ctx := context.Background()
	for {
		s.mu.Lock()
		for !s.aborted && len(s.queue) == 0 && !(s.looking && !s.finished) && !s.finished {
			s.wake.Wait()
		}
		if s.aborted || (s.finished && len(s.queue) == 0) {
			s.busy = false
			if s.idle != nil {
				close(s.idle)
				s.idle = nil
			}
			s.mu.Unlock()
			return
		}
		var clip []byte
		final := len(s.queue) > 0
		if final {
			r := s.queue[0]
			s.queue = s.queue[1:]
			clip = append([]byte(nil), s.pcm[r[0]:r[1]]...)
		} else {
			clip = append([]byte(nil), s.pcm[s.seg.Start():]...)
			s.lastLook = time.Now()
		}
		s.looking = false
		s.busy = true
		s.mu.Unlock()

		res, err := s.eng.Transcribe(ctx, clip, s.lang, s.keywords)

		s.mu.Lock()
		s.busy = false
		switch {
		case err != nil:
			if final && s.err == nil {
				s.err = err
			}
			s.log.Warn("whistle transcription failed", "err", err)
		case final:
			if res.Text != "" {
				s.texts = append(s.texts, res.Text)
			}
			s.openText = ""
		default:
			s.openText = res.Text
		}
		if res.Language != "" {
			s.langSeen = res.Language
		}
		live := s.joined()
		s.mu.Unlock()
		if s.onPartial != nil && live != "" {
			s.onPartial(live)
		}
	}
}

func (s *whistleStream) joined() string {
	parts := append([]string(nil), s.texts...)
	if s.openText != "" {
		parts = append(parts, s.openText)
	}
	return strings.Join(parts, " ")
}

func (s *whistleStream) Finish(ctx context.Context) (string, error) {
	if s.engErr != nil {
		return "", s.engErr
	}
	s.mu.Lock()
	// What is left after the last pause is the final segment.
	if len(s.pcm)-s.cutAt >= whistle.SampleRate*2/5 {
		s.queue = append(s.queue, [2]int{s.cutAt, len(s.pcm)})
		s.cutAt = len(s.pcm)
	}
	s.finished = true
	s.openText = ""
	idle := make(chan struct{})
	s.idle = idle
	s.wake.Signal()
	s.mu.Unlock()
	select {
	case <-idle:
	case <-ctx.Done():
		s.Abort()
		return "", ctx.Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil && len(s.texts) == 0 {
		return "", s.err
	}
	return strings.Join(s.texts, " "), nil
}

func (s *whistleStream) Language() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.langSeen
}

func (s *whistleStream) Abort() {
	s.mu.Lock()
	s.aborted = true
	s.wake.Broadcast()
	s.mu.Unlock()
}

// whistleFile transcribes a WAV file the same way, segment by segment: the
// spool file after a failed session, or an uploaded recording.
type whistleFile struct {
	lang     string
	keywords []string
}

func (w whistleFile) TranscribeFile(ctx context.Context, path string) (string, error) {
	e, err := whistleEngine()
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	pcm, err := pcmFromWAV(b)
	if err != nil {
		return "", err
	}
	seg := whistle.NewSegmenter()
	var cuts []int
	for off := 0; off < len(pcm); off += 3200 {
		cuts = append(cuts, seg.Add(pcm[off:min(off+3200, len(pcm))])...)
	}
	cuts = append(cuts, len(pcm))
	var parts []string
	prev := 0
	for _, c := range cuts {
		if c-prev < whistle.SampleRate*2/5 {
			continue
		}
		res, err := e.Transcribe(ctx, pcm[prev:c], w.lang, w.keywords)
		if err != nil {
			return "", err
		}
		if res.Text != "" {
			parts = append(parts, res.Text)
		}
		prev = c
	}
	return strings.Join(parts, " "), nil
}

// pcmFromWAV returns the samples of a 16 kHz mono 16-bit WAV, the format Vito
// records in, walking the chunks rather than assuming a 44-byte header.
func pcmFromWAV(b []byte) ([]byte, error) {
	if len(b) < 12 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return nil, errors.New("not a WAV file")
	}
	for off := 12; off+8 <= len(b); {
		id := string(b[off : off+4])
		size := int(uint32(b[off+4]) | uint32(b[off+5])<<8 | uint32(b[off+6])<<16 | uint32(b[off+7])<<24)
		body := off + 8
		if id == "fmt " && body+16 <= len(b) {
			ch := int(b[body+2]) | int(b[body+3])<<8
			rate := int(uint32(b[body+4]) | uint32(b[body+5])<<8 | uint32(b[body+6])<<16 | uint32(b[body+7])<<24)
			bits := int(b[body+14]) | int(b[body+15])<<8
			if ch != 1 || rate != whistle.SampleRate || bits != 16 {
				return nil, errors.New("Whistle needs 16 kHz mono 16-bit audio")
			}
		}
		if id == "data" {
			end := min(body+size, len(b))
			return b[body:end], nil
		}
		off = body + size + size%2
	}
	return nil, errors.New("WAV file has no data")
}
