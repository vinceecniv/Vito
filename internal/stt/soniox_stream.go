package stt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"vito/internal/audio"
	"vito/internal/config"
)

// SonioxModel is the realtime model. Soniox runs one unified realtime model
// across all its languages, so there is nothing to choose here.
const SonioxModel = "stt-rt-v5"

const sonioxURL = "wss://stt-rt.soniox.com/transcribe-websocket"

// sonioxStream is one Soniox realtime session.
//
// The protocol differs from AssemblyAI's in two ways that shape this code: the
// API key travels in a JSON config message rather than a header (so the socket
// is useless until that message lands), and results arrive as a stream of
// tokens rather than a whole transcript per turn. Tokens marked is_final are
// settled and are appended once; the rest form a tail that is replaced on every
// message.
type sonioxStream struct {
	log       *slog.Logger
	onPartial func(string)

	audioCh   chan []byte
	closeSend sync.Once
	cancel    context.CancelFunc
	done      chan struct{}

	mu        sync.Mutex
	pending   []byte // accumulates towards streamChunkBytes
	final     strings.Builder
	tail      string
	failed    error
	langCount map[string]int // detected language of final tokens, for the flag
}

func newSonioxStream(cfg config.STT, keyterms []string, log *slog.Logger, onPartial func(string)) *sonioxStream {
	ctx, cancel := context.WithCancel(context.Background())
	s := &sonioxStream{
		log:       log,
		onPartial: onPartial,
		audioCh:   make(chan []byte, 1024),
		cancel:    cancel,
		done:      make(chan struct{}),
		langCount: map[string]int{},
	}
	go s.run(ctx, cfg)
	return s
}

func (s *sonioxStream) Send(pcm []byte) {
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

func (s *sonioxStream) Finish(ctx context.Context) (string, error) {
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
	return strings.TrimSpace(s.final.String() + s.tail), nil
}

func (s *sonioxStream) Abort() {
	s.cancel()
	s.closeSend.Do(func() { close(s.audioCh) })
}

// Language returns the most-spoken detected language (ISO code) of the session,
// or "" if Soniox reported none.
func (s *sonioxStream) Language() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	best, bestN := "", 0
	for lang, n := range s.langCount {
		if n > bestN {
			best, bestN = lang, n
		}
	}
	return best
}

func (s *sonioxStream) fail(err error) {
	s.mu.Lock()
	if s.failed == nil {
		s.failed = err
	}
	s.mu.Unlock()
	s.log.Warn("stt stream failed", "err", err)
}

// sonioxConfig is the first message; without it the server transcribes nothing.
type sonioxConfig struct {
	APIKey                       string   `json:"api_key"`
	Model                        string   `json:"model"`
	AudioFormat                  string   `json:"audio_format"`
	SampleRate                   int      `json:"sample_rate"`
	NumChannels                  int      `json:"num_channels"`
	LanguageHints                []string `json:"language_hints,omitempty"`
	EnableLanguageIdentification bool     `json:"enable_language_identification,omitempty"`
}

func (s *sonioxStream) run(ctx context.Context, cfg config.STT) {
	defer close(s.done)

	dialCtx, dialDone := context.WithTimeout(ctx, 5*time.Second)
	conn, _, err := websocket.Dial(dialCtx, sonioxURL, nil)
	dialDone()
	if err != nil {
		s.fail(fmt.Errorf("dial soniox: %w", err))
		for range s.audioCh {
		}
		return
	}
	conn.SetReadLimit(1 << 20)
	defer conn.Close(websocket.StatusNormalClosure, "")

	start := sonioxConfig{
		APIKey:      cfg.SonioxAPIKey,
		Model:       SonioxModel,
		AudioFormat: "pcm_s16le",
		SampleRate:  audio.CaptureSampleRate,
		NumChannels: 1,
	}
	// Always identify the language so the history flag reflects what was actually
	// spoken (Soniox transcribes the real language even against a hint); keep the
	// configured language as a hint to bias recognition when one is set.
	start.EnableLanguageIdentification = true
	if cfg.Language != "auto" && cfg.Language != "" {
		start.LanguageHints = []string{cfg.Language}
	}
	payload, err := json.Marshal(start)
	if err != nil {
		s.fail(err)
		return
	}
	if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
		s.fail(fmt.Errorf("send soniox config: %w", err))
		for range s.audioCh {
		}
		return
	}

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

	// An empty frame ends the stream; the server flushes what is left and
	// answers with finished:true. It has to be a TEXT frame: the docs say
	// "binary or text", but an empty binary frame is silently ignored — the
	// session then just runs on until it times out. Verified against the live
	// API: empty text → finished within ~170 ms, empty binary → nothing at all.
	if err := conn.Write(ctx, websocket.MessageText, []byte{}); err != nil {
		s.fail(fmt.Errorf("send end-of-stream: %w", err))
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

type sonioxToken struct {
	Text     string `json:"text"`
	IsFinal  bool   `json:"is_final"`
	Language string `json:"language"` // ISO code, present when language identification is on
}

type sonioxMessage struct {
	Tokens       []sonioxToken `json:"tokens"`
	Finished     bool          `json:"finished"`
	ErrorCode    int           `json:"error_code"`
	ErrorMessage string        `json:"error_message"`
}

func (s *sonioxStream) readLoop(ctx context.Context, conn *websocket.Conn) error {
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		var msg sonioxMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			s.log.Debug("unparseable soniox message", "data", string(data))
			continue
		}
		if msg.ErrorMessage != "" {
			return fmt.Errorf("soniox error %d: %s", msg.ErrorCode, msg.ErrorMessage)
		}

		s.mu.Lock()
		// Final tokens are sent once and never change, so they are appended and
		// forgotten; everything else is a provisional tail that this message
		// replaces wholesale.
		tail := ""
		for _, tk := range msg.Tokens {
			if tk.IsFinal {
				s.final.WriteString(tk.Text)
				if tk.Language != "" && strings.TrimSpace(tk.Text) != "" {
					s.langCount[tk.Language]++
				}
			} else {
				tail += tk.Text
			}
		}
		s.tail = tail
		partial := strings.TrimSpace(s.final.String() + s.tail)
		s.mu.Unlock()

		if s.onPartial != nil {
			s.onPartial(partial)
		}
		if msg.Finished {
			return nil
		}
	}
}
