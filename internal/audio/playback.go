package audio

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/gen2brain/malgo"
)

// PlayWAV plays an embedded PCM16 WAV on the selected output device and
// returns when playback finishes (or after a safety timeout). Feedback
// sounds are < 150 ms, so callers may run this synchronously or in a
// goroutine as they prefer.
func PlayWAV(c *Context, deviceName string, wavData []byte, volume float64) error {
	pcm, sampleRate, channels, err := parseWAV(wavData)
	if err != nil {
		return err
	}
	// Apply gain whenever it isn't unity — below 1.0 attenuates, above 1.0
	// amplifies (applyGain clamps to the int16 range, so loud sources just
	// saturate rather than wrap). Skip the copy when volume is exactly 1.0.
	if volume != 1.0 {
		if volume < 0 {
			volume = 0
		}
		scaled := make([]byte, len(pcm))
		copy(scaled, pcm)
		applyGain(scaled, volume)
		pcm = scaled
	}

	cfg := malgo.DefaultDeviceConfig(malgo.Playback)
	cfg.Playback.Format = malgo.FormatS16
	cfg.Playback.Channels = uint32(channels)
	cfg.SampleRate = sampleRate
	cfg.Alsa.NoMMap = 1
	if id := c.deviceIDByName(malgo.Playback, deviceName); id != nil {
		cfg.Playback.DeviceID = id.Pointer()
	}

	pos := 0
	done := make(chan struct{})
	var closeOnce func()
	callbacks := malgo.DeviceCallbacks{
		Data: func(out, _ []byte, frameCount uint32) {
			n := copy(out, pcm[pos:])
			pos += n
			if pos >= len(pcm) {
				closeOnce()
			}
		},
	}
	closed := false
	closeOnce = func() {
		if !closed {
			closed = true
			close(done)
		}
	}

	dev, err := malgo.InitDevice(c.mc.Context, cfg, callbacks)
	if err != nil {
		return fmt.Errorf("init playback device: %w", err)
	}
	defer dev.Uninit()
	if err := dev.Start(); err != nil {
		return fmt.Errorf("start playback: %w", err)
	}

	// Safety timeout scaled to the clip length (feedback sounds are tiny, but the
	// mic-test plays back several seconds) plus a grace margin.
	durSec := float64(len(pcm)) / float64(sampleRate*uint32(channels)*2)
	timeout := time.Duration(durSec*float64(time.Second)) + 2*time.Second
	select {
	case <-done:
		// small grace so the device drains its last buffer
		time.Sleep(30 * time.Millisecond)
	case <-time.After(timeout):
	}
	return nil
}

// WAVFromPCM wraps raw capture-format PCM (16 kHz mono s16le) in a minimal WAV
// container so it can be played back through PlayWAV (used by the mic test).
func WAVFromPCM(pcm []byte) []byte {
	var buf bytes.Buffer
	buf.WriteString("RIFF")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(36+len(pcm)))
	buf.WriteString("WAVE")
	buf.WriteString("fmt ")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(16))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(1)) // PCM
	_ = binary.Write(&buf, binary.LittleEndian, uint16(CaptureChannels))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(CaptureSampleRate))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(CaptureSampleRate*CaptureChannels*BytesPerSample))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(CaptureChannels*BytesPerSample))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(8*BytesPerSample))
	buf.WriteString("data")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(len(pcm)))
	buf.Write(pcm)
	return buf.Bytes()
}

// parseWAV handles the simple PCM16 files we embed ourselves.
func parseWAV(data []byte) (pcm []byte, sampleRate uint32, channels uint16, err error) {
	if len(data) < 12 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return nil, 0, 0, errors.New("not a RIFF/WAVE file")
	}
	off := 12
	for off+8 <= len(data) {
		id := string(data[off : off+4])
		size := int(binary.LittleEndian.Uint32(data[off+4 : off+8]))
		body := off + 8
		if body+size > len(data) {
			size = len(data) - body
		}
		switch id {
		case "fmt ":
			if size < 16 {
				return nil, 0, 0, errors.New("short fmt chunk")
			}
			if binary.LittleEndian.Uint16(data[body:]) != 1 || binary.LittleEndian.Uint16(data[body+14:]) != 16 {
				return nil, 0, 0, errors.New("only PCM16 WAV supported")
			}
			channels = binary.LittleEndian.Uint16(data[body+2:])
			sampleRate = binary.LittleEndian.Uint32(data[body+4:])
		case "data":
			// A writer that streams the audio and never seeks back to patch the
			// header leaves this at 0 while the samples are right there. Taking
			// the declared length at face value plays silence and reports
			// success, which is the hardest kind of bug to notice.
			if size == 0 && body < len(data) {
				size = len(data) - body
			}
			pcm = data[body : body+size]
		}
		off = body + size + size%2
	}
	if sampleRate == 0 {
		return nil, 0, 0, errors.New("missing fmt chunk")
	}
	if len(pcm) == 0 {
		return nil, 0, 0, errors.New("missing or empty data chunk")
	}
	return pcm, sampleRate, channels, nil
}
