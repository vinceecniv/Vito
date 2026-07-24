// Package audio wraps malgo for capture, playback and the spool file.
package audio

import (
	"fmt"

	"github.com/gen2brain/malgo"
)

const (
	CaptureSampleRate = 16000 // AssemblyAI streaming expects 16 kHz mono s16le
	CaptureChannels   = 1
	BytesPerSample    = 2
)

type Context struct {
	mc *malgo.AllocatedContext
}

func NewContext() (*Context, error) {
	mc, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
	if err != nil {
		return nil, fmt.Errorf("init audio context: %w", err)
	}
	return &Context{mc: mc}, nil
}

func (c *Context) Close() {
	_ = c.mc.Uninit()
	c.mc.Free()
}

type DeviceInfo struct {
	Name      string `json:"name"`
	IsDefault bool   `json:"is_default"`
}

func (c *Context) CaptureDevices() ([]DeviceInfo, error)  { return c.devices(malgo.Capture) }
func (c *Context) PlaybackDevices() ([]DeviceInfo, error) { return c.devices(malgo.Playback) }

func (c *Context) devices(kind malgo.DeviceType) ([]DeviceInfo, error) {
	infos, err := c.mc.Devices(kind)
	if err != nil {
		return nil, err
	}
	out := make([]DeviceInfo, 0, len(infos))
	for _, info := range infos {
		out = append(out, DeviceInfo{Name: info.Name(), IsDefault: info.IsDefault != 0})
	}
	return out, nil
}

// deviceIDByName resolves a configured device name to a malgo ID.
// Returns nil (system default) when name is empty or not found.
func (c *Context) deviceIDByName(kind malgo.DeviceType, name string) *malgo.DeviceID {
	if name == "" {
		return nil
	}
	infos, err := c.mc.Devices(kind)
	if err != nil {
		return nil
	}
	for _, info := range infos {
		if info.Name() == name {
			id := info.ID
			return &id
		}
	}
	return nil
}
