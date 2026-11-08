//go:build linux

package pill

import (
	"math"
	"os"
	"testing"
	"time"
)

// Opens the real window on the running compositor for a few seconds:
//
//	VITO_PILL_TEST=1 go test ./internal/pill/ -run TestWindow -v
func TestWindow(t *testing.T) {
	if os.Getenv("VITO_PILL_TEST") == "" {
		t.Skip("set VITO_PILL_TEST=1 to open the pill on this desktop")
	}
	start := time.Now()
	win, err := openWindow(0, [4]int{})
	if err != nil {
		t.Fatal(err)
	}
	t.Log("open took", time.Since(start))
	defer win.close()
	buf := make([]byte, surfaceW*scale*surfaceH*scale*4)
	var f Frame
	for n := 0; time.Since(start) < 4*time.Second; n++ {
		s := time.Since(start).Seconds()
		f.T, f.Show = s, min(1, s*6)
		if n%barStep == 0 {
			pushLevel(&f.Levels, 0.5+0.4*math.Sin(s*9))
		}
		new(painter).Draw(buf, surfaceW*scale, surfaceH*scale, f)
		if err := win.present(buf); err != nil {
			t.Fatal(err)
		}
		time.Sleep(frameTime)
	}
}
