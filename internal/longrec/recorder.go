package longrec

import (
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"vito/internal/stt"
)

// Cutting into blocks: at the first pause after minBlock, at the latest at
// maxBlock. Each block is a streaming session of its own, so no provider's
// session limit is ever reached, and each is one cleanup request.
var (
	minBlock   = 60.0  // seconds
	maxBlock   = 150.0 // seconds
	pauseLen   = 0.7   // seconds of quiet that count as a pause
	speechGate = 0.03  // peak above which a chunk counts as speech (as in the daemon)
)

// Options wire a recorder to the daemon's speech service and its events.
type Options struct {
	Store *Store
	// NewStream opens a live session for one block.
	NewStream func(onPartial func(string)) stt.Streamer
	// File transcribes a block's audio when its live session failed; may be nil.
	File          stt.Transcriber
	FinishTimeout time.Duration
	Provider      string
	Language      string
	Log           *slog.Logger
	// OnUpdate reports the live state (timer, partial text). Called often.
	OnUpdate func(Status)
	// OnBlock reports a transcribed block, for the cleanup queue.
	OnBlock func(id string, b Block)
}

// Live is a block still being heard or finished.
type Live struct {
	Index int     `json:"i"`
	Start float64 `json:"start"`
	Text  string  `json:"text"`
}

// Status is a running session as the interface shows it.
type Status struct {
	ID      string  `json:"id"`
	Title   string  `json:"title"`
	State   string  `json:"state"`
	Seconds float64 `json:"seconds"`
	Live    []Live  `json:"live"` // unfinished blocks, oldest first
	Blocks  int     `json:"blocks"`
}

type liveBlock struct {
	index   int
	start   float64
	stream  stt.Streamer
	partial string
}

// Recorder is one running session.
type Recorder struct {
	o    Options
	meta Meta
	wav  *wavFile

	mu       sync.Mutex
	cur      *liveBlock
	pending  map[int]*liveBlock // cut, not yet transcribed
	next     int
	quiet    float64 // seconds of quiet at the end of the current block
	paused   bool
	stopped  bool
	blocks   int
	lastTick time.Time
	wg       sync.WaitGroup
}

// Start begins a session: its folder, its audio file, its first block.
func Start(o Options, title string) (*Recorder, error) {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.FinishTimeout <= 0 {
		o.FinishTimeout = 30 * time.Second
	}
	now := time.Now()
	m := Meta{ID: newID(), Title: title, Started: now, State: StateRecording, Provider: o.Provider, Language: o.Language}
	if strings.TrimSpace(m.Title) == "" {
		m.Title = "" // the interface names an untitled session by its date
	}
	if err := o.Store.create(m); err != nil {
		return nil, err
	}
	w, err := createWAV(o.Store.AudioPath(m.ID))
	if err != nil {
		o.Store.Delete(m.ID)
		return nil, err
	}
	r := &Recorder{o: o, meta: m, wav: w, pending: map[int]*liveBlock{}}
	r.mu.Lock()
	r.openBlock(0)
	r.mu.Unlock()
	r.update()
	return r, nil
}

// Continue records on in an existing session: the audio is appended to its
// file and the blocks are numbered on. A session the daemon left unfinished
// first gets its untranscribed tail transcribed from the file, beside the new
// recording.
func Continue(o Options, id string) (*Recorder, error) {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.FinishTimeout <= 0 {
		o.FinishTimeout = 30 * time.Second
	}
	m, err := o.Store.Meta(id)
	if err != nil {
		return nil, err
	}
	if m.State != StateDone && m.State != StateInterrupted {
		return nil, fmt.Errorf("session is %s", m.State)
	}
	blocks, err := o.Store.Blocks(id)
	if err != nil {
		return nil, err
	}
	w, err := openWAV(o.Store.AudioPath(id))
	if err != nil {
		return nil, err
	}
	next, covered := 0, 0.0
	for _, b := range blocks {
		next = max(next, b.Index+1)
		covered = max(covered, b.End)
	}
	r := &Recorder{o: o, meta: m, wav: w, pending: map[int]*liveBlock{}, next: next, blocks: len(blocks)}
	at := w.seconds()
	r.mu.Lock()
	if at-covered > 0.5 { // the tail an interrupted session never transcribed
		gap := &liveBlock{index: r.next, start: covered, stream: noStream{}}
		r.next++
		r.pending[gap.index] = gap
		r.wg.Add(1)
		go r.finish(gap, at)
	}
	r.openBlock(at)
	r.mu.Unlock()
	o.Store.AddEvent(id, Event{Type: "resume", At: at})
	r.meta, err = o.Store.UpdateMeta(id, func(m *Meta) {
		m.State = StateRecording
		m.Seconds = at
		if o.Provider != "" {
			m.Provider = o.Provider
		}
	})
	if err != nil {
		r.Abort()
		return nil, err
	}
	r.update()
	return r, nil
}

// noStream stands in for a live session that never was: Finish fails, so
// finish transcribes the block from the file.
type noStream struct{}

func (noStream) Send([]byte) {}
func (noStream) Finish(context.Context) (string, error) {
	return "", fmt.Errorf("not heard live")
}
func (noStream) Language() string { return "" }
func (noStream) Abort()           {}

// ID is the session's id.
func (r *Recorder) ID() string { return r.meta.ID }

// openBlock starts a live session for the block that begins at start. r.mu held.
func (r *Recorder) openBlock(start float64) {
	b := &liveBlock{index: r.next, start: start}
	r.next++
	b.stream = r.o.NewStream(func(p string) {
		r.mu.Lock()
		b.partial = strings.TrimSpace(p)
		r.mu.Unlock()
		r.update()
	})
	r.cur = b
	r.quiet = 0
}

// cut ends the current block at the present length and finishes it in the
// background. r.mu held.
func (r *Recorder) cut() {
	b := r.cur
	if b == nil {
		return
	}
	r.cur = nil
	end := r.wav.seconds()
	r.pending[b.index] = b
	r.wg.Add(1)
	go r.finish(b, end)
}

// Write takes captured PCM. Called from one goroutine; never blocks on the
// network.
func (r *Recorder) Write(pcm []byte) {
	r.mu.Lock()
	if r.paused || r.stopped || r.cur == nil {
		r.mu.Unlock()
		return
	}
	if err := r.wav.write(pcm); err != nil {
		r.o.Log.Error("long recording: audio write failed", "err", err)
	}
	// Sent outside the lock: a stream may report a partial from within Send.
	// Write runs on one goroutine, so the block cannot change meanwhile.
	s := r.cur.stream
	r.mu.Unlock()
	s.Send(pcm)
	r.mu.Lock()
	if r.cur == nil || r.cur.stream != s {
		r.mu.Unlock()
		return
	}
	if peak(pcm) >= speechGate {
		r.quiet = 0
	} else {
		r.quiet += float64(len(pcm)) / bytesPerSec
	}
	length := r.wav.seconds() - r.cur.start
	if (length >= minBlock && r.quiet >= pauseLen) || length >= maxBlock {
		r.cut()
		r.openBlock(r.wav.seconds())
	}
	tick := time.Since(r.lastTick) >= time.Second
	if tick {
		r.lastTick = time.Now()
	}
	r.mu.Unlock()
	if tick {
		r.update()
	}
}

func peak(pcm []byte) float64 {
	var p int32
	for i := 0; i+1 < len(pcm); i += 2 {
		v := int32(int16(binary.LittleEndian.Uint16(pcm[i:])))
		if v < 0 {
			v = -v
		}
		p = max(p, v)
	}
	return float64(p) / 32767
}

// finish waits for a block's transcript — from its live session, else from its
// slice of the audio — and logs it.
func (r *Recorder) finish(b *liveBlock, end float64) {
	defer r.wg.Done()
	ctx, cancel := context.WithTimeout(context.Background(), r.o.FinishTimeout)
	text, err := b.stream.Finish(ctx)
	cancel()
	if err != nil && end-b.start > 0.5 {
		r.o.Log.Warn("long recording: live block failed, transcribing its audio", "block", b.index, "err", err)
		text, err = r.fromFile(b.start, end)
	}
	blk := Block{Index: b.index, Start: b.start, End: end, Text: strings.TrimSpace(text)}
	if err != nil {
		blk.Error = err.Error()
		r.o.Log.Error("long recording: block not transcribed", "block", b.index, "err", err)
	}
	if err := r.o.Store.AddBlock(r.meta.ID, blk); err != nil {
		r.o.Log.Error("long recording: block not saved", "block", b.index, "err", err)
	}
	r.mu.Lock()
	delete(r.pending, b.index)
	r.blocks++
	r.mu.Unlock()
	r.update()
	if r.o.OnBlock != nil {
		r.o.OnBlock(r.meta.ID, blk)
	}
}

func (r *Recorder) fromFile(from, to float64) (string, error) {
	return transcribeSlice(r.o.File, r.o.Store.AudioPath(r.meta.ID), from, to)
}

func transcribeSlice(t stt.Transcriber, audioPath string, from, to float64) (string, error) {
	if t == nil {
		return "", fmt.Errorf("no file transcription for this speech service")
	}
	tmp, err := os.MkdirTemp("", "vito-block-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	p := filepath.Join(tmp, "block.wav")
	if err := sliceWAV(audioPath, p, from, to); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	return t.TranscribeFile(ctx, p)
}

// Pause stops recording until Resume; the block so far is finished.
func (r *Recorder) Pause() {
	r.mu.Lock()
	if r.paused || r.stopped {
		r.mu.Unlock()
		return
	}
	r.paused = true
	r.cut()
	at := r.wav.seconds()
	_ = r.wav.patch()
	r.mu.Unlock()
	r.o.Store.AddEvent(r.meta.ID, Event{Type: "pause", At: at})
	r.meta, _ = r.o.Store.UpdateMeta(r.meta.ID, func(m *Meta) { m.State = StatePaused; m.Seconds = at })
	r.update()
}

// Resume goes on recording after a pause.
func (r *Recorder) Resume() {
	r.mu.Lock()
	if !r.paused || r.stopped {
		r.mu.Unlock()
		return
	}
	r.paused = false
	at := r.wav.seconds()
	r.openBlock(at)
	r.mu.Unlock()
	r.o.Store.AddEvent(r.meta.ID, Event{Type: "resume", At: at})
	r.meta, _ = r.o.Store.UpdateMeta(r.meta.ID, func(m *Meta) { m.State = StateRecording })
	r.update()
}

// Mark sets a bookmark at the present moment.
func (r *Recorder) Mark() (Event, error) { return r.event("mark", "") }

// Header starts a new section here; title may be empty (the cleanup pass
// proposes one).
func (r *Recorder) Header(title string) (Event, error) {
	return r.event("header", strings.TrimSpace(title))
}

func (r *Recorder) event(kind, title string) (Event, error) {
	r.mu.Lock()
	at := r.wav.seconds()
	stopped := r.stopped
	r.mu.Unlock()
	if stopped {
		return Event{}, fmt.Errorf("session stopped")
	}
	return r.o.Store.AddEvent(r.meta.ID, Event{Type: kind, At: at, Title: title})
}

// Stop ends the session: the last block is finished, the audio closed. It
// waits for every block's transcript.
func (r *Recorder) Stop() (Meta, error) {
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return r.meta, nil
	}
	r.stopped = true
	r.cut()
	r.mu.Unlock()
	r.update()
	r.wg.Wait()
	sec := r.wav.seconds()
	err := r.wav.close()
	words := r.o.Store.Words(r.meta.ID)
	m, e := r.o.Store.UpdateMeta(r.meta.ID, func(m *Meta) {
		m.State = StateDone
		m.Seconds = sec
		m.Words = words
	})
	if err == nil {
		err = e
	}
	r.meta = m
	r.update()
	return m, err
}

// Abort throws the live sessions away without waiting (daemon shutdown). The
// audio and the blocks so far stay; the next start offers to restore.
func (r *Recorder) Abort() {
	r.mu.Lock()
	r.stopped = true
	if r.cur != nil {
		r.cur.stream.Abort()
	}
	for _, b := range r.pending {
		b.stream.Abort()
	}
	_ = r.wav.close() // the state stays "recording": Recover picks it up
	r.mu.Unlock()
}

// Status reports the live state.
func (r *Recorder) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := Status{ID: r.meta.ID, Title: r.meta.Title, Seconds: r.wav.seconds(), Blocks: r.blocks}
	switch {
	case r.stopped:
		st.State = StateDone
	case r.paused:
		st.State = StatePaused
	default:
		st.State = StateRecording
	}
	for _, b := range r.pending {
		st.Live = append(st.Live, Live{Index: b.index, Start: b.start, Text: b.partial})
	}
	if r.cur != nil {
		st.Live = append(st.Live, Live{Index: r.cur.index, Start: r.cur.start, Text: r.cur.partial})
	}
	sortLive(st.Live)
	return st
}

// SetTitle renames the running session.
func (r *Recorder) SetTitle(title string) {
	r.mu.Lock()
	r.meta.Title = strings.TrimSpace(title)
	r.mu.Unlock()
	r.o.Store.UpdateMeta(r.meta.ID, func(m *Meta) { m.Title = strings.TrimSpace(title) })
	r.update()
}

func sortLive(l []Live) {
	for i := 1; i < len(l); i++ {
		for j := i; j > 0 && l[j].Index < l[j-1].Index; j-- {
			l[j], l[j-1] = l[j-1], l[j]
		}
	}
}

func (r *Recorder) update() {
	if r.o.OnUpdate != nil {
		r.o.OnUpdate(r.Status())
	}
}
