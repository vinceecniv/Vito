package stt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"

	"vito/internal/apierr"
	"vito/internal/audio"
	"vito/internal/config"
)

// streamChunkBytes is ~50 ms of 16 kHz mono s16le, the chunk size the
// AssemblyAI docs recommend.
const streamChunkBytes = 1600

// Stream is one AssemblyAI v3 streaming session. It dials asynchronously so
// the caller can start buffering audio immediately (prewarm); buffered audio
// is flushed once the socket is up. On any failure the stream marks itself
// failed and the caller falls back to the async spool path.
type Stream struct {
	log       *slog.Logger
	onPartial func(string) // may be nil; called from the read loop

	audioCh   chan []byte
	closeSend sync.Once
	cancel    context.CancelFunc
	done      chan struct{} // run() finished

	mu      sync.Mutex
	pending []byte // accumulates towards streamChunkBytes
	turns   map[int]string
	failed  error
}

func newAssemblyAIStream(cfg config.STT, keyterms []string, log *slog.Logger, onPartial func(string)) *Stream {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Stream{
		log:       log,
		onPartial: onPartial,
		audioCh:   make(chan []byte, 1024),
		cancel:    cancel,
		done:      make(chan struct{}),
		turns:     make(map[int]string),
	}
	go s.run(ctx, cfg, keyterms)
	return s
}

// Send buffers a PCM chunk for the socket. Never blocks: when the buffer is
// full or the stream failed the chunk is dropped — the spool file has it.
func (s *Stream) Send(pcm []byte) {
	s.mu.Lock()
	if s.failed != nil {
		s.mu.Unlock()
		return
	}
	s.pending = append(s.pending, pcm...)
	var out [][]byte
	for len(s.pending) >= streamChunkBytes {
		out = append(out, s.pending[:streamChunkBytes:streamChunkBytes])
		s.pending = s.pending[streamChunkBytes:]
	}
	s.mu.Unlock()

	for _, chunk := range out {
		select {
		case s.audioCh <- chunk:
		default:
			s.log.Warn("stt stream buffer full, dropping chunk")
			return
		}
	}
}

// Finish flushes remaining audio, terminates the session and returns the
// final transcript. Call after capture has stopped.
func (s *Stream) Finish(ctx context.Context) (string, error) {
	s.mu.Lock()
	tail := s.pending
	s.pending = nil
	failed := s.failed
	s.mu.Unlock()
	if failed != nil {
		return "", failed
	}
	if len(tail) > 0 {
		select {
		case s.audioCh <- tail:
		default:
		}
	}
	s.closeSend.Do(func() { close(s.audioCh) })

	select {
	case <-s.done:
	case <-ctx.Done():
		s.cancel()
		<-s.done
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed != nil {
		return "", s.failed
	}
	return s.assemble(), nil
}

// Abort tears the session down without waiting for results.
func (s *Stream) Abort() {
	s.cancel()
	s.closeSend.Do(func() { close(s.audioCh) })
}

// Language returns "" — AssemblyAI's streaming turns don't carry a per-turn
// language in the responses we parse, so callers fall back to the configured one.
func (s *Stream) Language() string { return "" }

func (s *Stream) fail(err error) {
	s.mu.Lock()
	if s.failed == nil {
		s.failed = err
	}
	s.mu.Unlock()
	s.log.Warn("stt stream failed", "err", err)
}

func (s *Stream) run(ctx context.Context, cfg config.STT, keyterms []string) {
	defer close(s.done)

	q := url.Values{}
	q.Set("sample_rate", strconv.Itoa(audio.CaptureSampleRate))
	q.Set("encoding", "pcm_s16le")
	q.Set("format_turns", "true")
	if cfg.Language == "auto" {
		q.Set("language_detection", "true")
	} else {
		q.Set("language_codes", cfg.Language)
	}
	if cfg.Model != "" {
		q.Set("speech_model", cfg.Model)
	}
	if len(keyterms) > 0 {
		if data, err := json.Marshal(keyterms); err == nil {
			q.Set("keyterms_prompt", string(data))
		}
	}
	wsURL := streamURL(cfg) + "?" + q.Encode()

	dialCtx, dialDone := context.WithTimeout(ctx, 5*time.Second)
	conn, resp, err := websocket.Dial(dialCtx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": []string{cfg.APIKey}},
	})
	dialDone()
	if err != nil {
		// A depleted balance is rejected on the handshake (402); flag it as such
		// so the caller skips the async fallback that would only fail the same way.
		if resp != nil {
			if e := apierr.FromHTTP("AssemblyAI", resp.StatusCode, ""); e != nil {
				s.fail(e)
				for range s.audioCh {
				}
				return
			}
		}
		s.fail(fmt.Errorf("dial streaming API: %w", err))
		for range s.audioCh { // drain until Finish/Abort closes it
		}
		return
	}
	conn.SetReadLimit(1 << 20)
	defer conn.Close(websocket.StatusNormalClosure, "")

	readDone := make(chan error, 1)
	go func() { readDone <- s.readLoop(ctx, conn) }()

	for chunk := range s.audioCh {
		if err := conn.Write(ctx, websocket.MessageBinary, chunk); err != nil {
			if ctx.Err() == nil { // silent on Abort
				s.fail(fmt.Errorf("send audio: %w", err))
			}
			for range s.audioCh {
			}
			return
		}
	}
	if ctx.Err() != nil { // aborted; no result wanted
		return
	}

	// Audio channel closed: end the turn and ask for the final transcripts.
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"Terminate"}`)); err != nil {
		s.fail(fmt.Errorf("send Terminate: %w", err))
		return
	}
	select {
	case err := <-readDone:
		var ce websocket.CloseError
		if err != nil && !errors.As(err, &ce) {
			s.fail(err)
		}
	case <-time.After(5 * time.Second):
		s.fail(errors.New("timeout waiting for final transcript"))
	case <-ctx.Done():
	}
}

type serverMessage struct {
	Type            string `json:"type"`
	TurnOrder       int    `json:"turn_order"`
	Transcript      string `json:"transcript"`
	EndOfTurn       bool   `json:"end_of_turn"`
	TurnIsFormatted bool   `json:"turn_is_formatted"`
	Error           string `json:"error"`
}

func (s *Stream) readLoop(ctx context.Context, conn *websocket.Conn) error {
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		var msg serverMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			s.log.Debug("unparseable stt message", "data", string(data))
			continue
		}
		switch msg.Type {
		case "Begin":
			s.log.Debug("stt session began")
		case "Turn":
			s.mu.Lock()
			// Each Turn message carries the full transcript-so-far for that
			// turn; the formatted version arrives last, so overwriting is
			// exactly right.
			s.turns[msg.TurnOrder] = msg.Transcript
			partial := s.assemble()
			s.mu.Unlock()
			if s.onPartial != nil {
				s.onPartial(partial)
			}
		case "Termination":
			return nil
		default:
			if msg.Error != "" {
				return fmt.Errorf("streaming API error: %s", msg.Error)
			}
		}
	}
}

// assemble joins turn transcripts in order. Caller must hold s.mu.
func (s *Stream) assemble() string {
	orders := make([]int, 0, len(s.turns))
	for o := range s.turns {
		orders = append(orders, o)
	}
	sort.Ints(orders)
	text := ""
	for _, o := range orders {
		t := s.turns[o]
		if t == "" {
			continue
		}
		if text != "" {
			text += " "
		}
		text += t
	}
	return text
}
