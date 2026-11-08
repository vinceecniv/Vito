//go:build linux

package inject

import (
	"fmt"
	"time"

	"vito/internal/config"
	"vito/internal/x11"
)

// x11Inject delivers through XTEST (internal/x11): the same paste or typing as
// the other backends, with the clipboard served by Vito itself.
func x11Inject(cfg config.Injection, mode Mode, text string) error {
	c, err := x11.Keys()
	if err != nil {
		return err
	}
	defer c.Close()
	switch mode {
	case ModeType:
		return c.Type(text)
	case ModePaste:
		prev, prevOK := readClipboardText()
		if err := copyText(text); err != nil {
			return err
		}
		time.Sleep(time.Duration(cfg.PasteDelayMS) * time.Millisecond)
		if err := c.Paste(); err != nil {
			return err
		}
		if cfg.RestoreClipboard && prevOK {
			time.Sleep(time.Duration(cfg.RestoreDelayMS) * time.Millisecond)
			_ = copyText(prev) // best effort
		}
		return nil
	}
	return fmt.Errorf("unknown injection mode %q", mode)
}

func x11PressEnter() error {
	c, err := x11.Keys()
	if err != nil {
		return err
	}
	defer c.Close()
	time.Sleep(40 * time.Millisecond) // let the paste settle before submitting
	return c.Enter()
}
