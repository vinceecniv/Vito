package audio

import (
	"fmt"
	"os"
	"sync"

	"github.com/gen2brain/malgo"
)

// Player plays a kept recording through the configured output device, with
// pause and seek. Playback lives in the daemon rather than in the browser on
// purpose: only the daemon knows which output device the user picked, and a web
// page can't select one without asking for microphone permission first.
//
// One recording plays at a time — starting another replaces it.
type Player struct {
	mu     sync.Mutex
	dev    *malgo.Device
	pcm    []byte
	pos    int // byte offset into pcm
	rate   int // bytes per second
	dur    float64
	id     string // which recording is loaded ("" = nothing)
	paused bool
	onEnd  func(id string)
}

// NewPlayer returns a player that calls onEnd (from the audio callback's
// goroutine, so it must not block) when a recording finishes on its own.
func NewPlayer(onEnd func(id string)) *Player { return &Player{onEnd: onEnd} }

type PlaybackState struct {
	ID       string  `json:"id,omitempty"`
	Playing  bool    `json:"playing"`
	Position float64 `json:"position"` // seconds
	Duration float64 `json:"duration"` // seconds
}

func (p *Player) State() PlaybackState {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.id == "" {
		return PlaybackState{}
	}
	return PlaybackState{ID: p.id, Playing: !p.paused, Position: p.position(), Duration: p.dur}
}

// position reports the play head in seconds. Caller holds p.mu.
func (p *Player) position() float64 {
	if p.rate == 0 {
		return 0
	}
	return float64(p.pos) / float64(p.rate)
}

// Play loads and starts a WAV file. Any recording already playing is stopped.
func (p *Player) Play(c *Context, deviceName, id, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	pcm, sampleRate, channels, err := parseWAV(data)
	if err != nil {
		return err
	}
	p.Stop()

	p.mu.Lock()
	p.pcm, p.pos, p.id, p.paused = pcm, 0, id, false
	p.rate = int(sampleRate) * int(channels) * BytesPerSample
	p.dur = float64(len(pcm)) / float64(p.rate)
	p.mu.Unlock()

	cfg := malgo.DefaultDeviceConfig(malgo.Playback)
	cfg.Playback.Format = malgo.FormatS16
	cfg.Playback.Channels = uint32(channels)
	cfg.SampleRate = sampleRate
	cfg.Alsa.NoMMap = 1
	if devID := c.deviceIDByName(malgo.Playback, deviceName); devID != nil {
		cfg.Playback.DeviceID = devID.Pointer()
	}

	callbacks := malgo.DeviceCallbacks{Data: func(out, _ []byte, _ uint32) {
		p.mu.Lock()
		if p.paused || p.pos >= len(p.pcm) {
			p.mu.Unlock()
			for i := range out { // silence, so the device keeps running while paused
				out[i] = 0
			}
			return
		}
		n := copy(out, p.pcm[p.pos:])
		p.pos += n
		for i := n; i < len(out); i++ {
			out[i] = 0
		}
		ended := p.pos >= len(p.pcm)
		id := p.id
		p.mu.Unlock()
		if ended && p.onEnd != nil {
			go p.onEnd(id)
		}
	}}

	dev, err := malgo.InitDevice(c.mc.Context, cfg, callbacks)
	if err != nil {
		return fmt.Errorf("init playback device: %w", err)
	}
	if err := dev.Start(); err != nil {
		dev.Uninit()
		return fmt.Errorf("start playback: %w", err)
	}
	p.mu.Lock()
	p.dev = dev
	p.mu.Unlock()
	return nil
}

// Pause and Resume keep the device open, so resuming is instant.
func (p *Player) Pause() {
	p.mu.Lock()
	p.paused = true
	p.mu.Unlock()
}

func (p *Player) Resume() {
	p.mu.Lock()
	// Resuming after the clip ran out starts it over, which is what a play button
	// is expected to do once it has reached the end.
	if p.pos >= len(p.pcm) {
		p.pos = 0
	}
	p.paused = false
	p.mu.Unlock()
}

// Seek moves the play head to the given offset in seconds.
func (p *Player) Seek(sec float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.rate == 0 {
		return
	}
	pos := int(sec * float64(p.rate))
	pos -= pos % (BytesPerSample) // keep sample alignment
	if pos < 0 {
		pos = 0
	}
	if pos > len(p.pcm) {
		pos = len(p.pcm)
	}
	p.pos = pos
}

// Stop ends playback and releases the device.
func (p *Player) Stop() {
	p.mu.Lock()
	dev := p.dev
	p.dev, p.id, p.pcm, p.pos, p.paused = nil, "", nil, 0, false
	p.mu.Unlock()
	if dev != nil {
		dev.Uninit()
	}
}
