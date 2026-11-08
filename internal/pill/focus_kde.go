package pill

import (
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// kdeFocus knows only the screens' sizes, from kscreen-doctor: KWin tells a
// client neither the screen in use nor the focused window without a script of
// its own. The size is what the pinned island needs to stay on screen; without
// it a drag would push the layer surface out of bounds, which KWin answers by
// closing it ("invalid geometry").
var kdeFocus = focusSource{
	available: func() bool {
		return strings.Contains(strings.ToLower(os.Getenv("XDG_CURRENT_DESKTOP")), "kde")
	},
	watch:  func(stop <-chan struct{}, changed func(output string)) { <-stop },
	screen: kdeScreen,
}

func init() { focusSources = append(focusSources, kdeFocus) }

var (
	kdeMu   sync.Mutex
	kdeAt   time.Time
	kdeList []kdeOutput
)

type kdeOutput struct {
	Name     string
	Enabled  bool
	Scale    float64
	Rotation int
	Size     struct{ Width, Height int }
}

func kdeScreen(connector string) (w, h int, ok bool) {
	kdeMu.Lock()
	defer kdeMu.Unlock()
	if time.Since(kdeAt) > 2*time.Second {
		out, err := exec.Command("kscreen-doctor", "-j").Output()
		if err != nil {
			return 0, 0, false
		}
		var d struct{ Outputs []kdeOutput }
		if json.Unmarshal(out, &d) != nil {
			return 0, 0, false
		}
		kdeList, kdeAt = d.Outputs, time.Now()
	}
	for _, o := range kdeList {
		// A window that was not told its screen ("") is on the only one.
		if connector == "" && len(kdeList) == 1 {
			connector = o.Name
		}
		if o.Name != connector || !o.Enabled || o.Scale <= 0 {
			continue
		}
		w, h = int(math.Round(float64(o.Size.Width)/o.Scale)), int(math.Round(float64(o.Size.Height)/o.Scale))
		if o.Rotation == 2 || o.Rotation == 8 { // a quarter turn either way
			w, h = h, w
		}
		return w, h, true
	}
	return 0, 0, false
}
