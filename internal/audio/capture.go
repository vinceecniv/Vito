package audio

import (
	"fmt"

	"github.com/gen2brain/malgo"
)

// Capture records 16 kHz mono s16le audio from the selected input device.
// onData receives gain-adjusted PCM chunks and is called from the audio
// thread: it must not block (hand off to a buffered channel).
type Capture struct {
	dev *malgo.Device
}

func StartCapture(c *Context, deviceName string, gain float64, onData func([]byte)) (*Capture, error) {
	cfg := malgo.DefaultDeviceConfig(malgo.Capture)
	cfg.Capture.Format = malgo.FormatS16
	cfg.Capture.Channels = CaptureChannels
	cfg.SampleRate = CaptureSampleRate
	cfg.Alsa.NoMMap = 1
	if id := c.deviceIDByName(malgo.Capture, deviceName); id != nil {
		cfg.Capture.DeviceID = id.Pointer()
	}

	callbacks := malgo.DeviceCallbacks{
		Data: func(_, in []byte, frameCount uint32) {
			n := int(frameCount) * CaptureChannels * BytesPerSample
			if n > len(in) {
				n = len(in)
			}
			chunk := make([]byte, n)
			copy(chunk, in[:n])
			if gain != 1.0 {
				applyGain(chunk, gain)
			}
			onData(chunk)
		},
	}

	dev, err := malgo.InitDevice(c.mc.Context, cfg, callbacks)
	if err != nil {
		return nil, fmt.Errorf("init capture device: %w", err)
	}
	if err := dev.Start(); err != nil {
		dev.Uninit()
		return nil, fmt.Errorf("start capture: %w", err)
	}
	return &Capture{dev: dev}, nil
}

func (c *Capture) Stop() {
	_ = c.dev.Stop()
	c.dev.Uninit()
}

func applyGain(pcm []byte, gain float64) {
	for i := 0; i+1 < len(pcm); i += 2 {
		s := int16(uint16(pcm[i]) | uint16(pcm[i+1])<<8)
		v := int32(float64(s) * gain)
		if v > 32767 {
			v = 32767
		} else if v < -32768 {
			v = -32768
		}
		pcm[i] = byte(uint16(v))
		pcm[i+1] = byte(uint16(v) >> 8)
	}
}
