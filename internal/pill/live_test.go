package pill

import (
	"strings"
	"testing"

	"golang.org/x/image/font"
)

// The words never jump: per frame the text moves by a few pixels at most,
// while the strip grows, once it slides, and across the moment old words are
// dropped, also when a partial result brings several words at once.
func TestTickerMovesSmoothly(t *testing.T) {
	if err := faces(); err != nil {
		t.Fatal(err)
	}
	words := strings.Fields("Kun je de tekst die bij de pil verschijnt vloeiender laten aanvullen zodat de rest vloeiend opschuift naar links in plaats van dat het springerig gaat zoals het nu doet en dan nog wat meer woorden om ver voorbij de rand te komen zodat er ook oude woorden wegvallen uit het beeld")
	var f Frame
	var tk ticker
	maxStep, maxJerk, prev, prevD := 0.0, 0.0, 0.0, 0.0
	first, second := true, true
	// Partial results come a few words at a time, about as fast as speech:
	// three words a second.
	for i := 0; i < len(words); i += 3 {
		tk.set(strings.Join(words[:min(i+3, len(words))], " "))
		for k := 0; k < 60; k++ {
			tk.step(&f)
			// Where the first word stands, dropped or not.
			left := -f.LiveW*scale/2 + livePad*scale - f.LiveOff - dropped(tk.drawn, tk.drop)
			if tk.drawn == "" {
				continue
			}
			if !first {
				d := left - prev
				maxStep = max(maxStep, d, -d)
				if !second {
					maxJerk = max(maxJerk, d-prevD, prevD-d)
				}
				prevD, second = d, false
			}
			prev, first = left, false
		}
	}
	if tk.drop == 0 {
		t.Error("the test never got old words dropped")
	}
	t.Logf("largest move in one frame: %.1f device px, largest change of speed: %.1f", maxStep, maxJerk)
	if maxStep > 16 || maxJerk > 3 {
		t.Errorf("the words move %.1f px in one frame, and change speed by %.1f", maxStep, maxJerk)
	}
}

// Letters come in a few at a time; a revised ending is typed again from where
// it changed.
func TestTickerTypesOut(t *testing.T) {
	if err := faces(); err != nil {
		t.Fatal(err)
	}
	var f Frame
	var tk ticker
	tk.set("hallo wereld")
	tk.step(&f)
	tk.step(&f)
	if tk.drawn != "h" && tk.drawn != "ha" {
		t.Errorf("first frame shows %q, want one letter", tk.drawn)
	}
	for k := 0; k < 40; k++ {
		tk.step(&f)
	}
	if tk.drawn != "hallo wereld" {
		t.Fatalf("typed out %q", tk.drawn)
	}
	tk.set("hallo werelden, ja")
	if tk.shown != len("hallo wereld") {
		t.Errorf("kept %d bytes, want the unchanged %d", tk.shown, len("hallo wereld"))
	}
	tk.set("hallo wérk")
	if tk.text[:tk.shown] != "hallo w" {
		t.Errorf("after a revision kept %q", tk.text[:tk.shown])
	}
}

func dropped(s string, drop int) float64 {
	words := strings.Fields(s)
	if drop == 0 || drop >= len(words) {
		return 0
	}
	all := font.MeasureString(bodyFace, strings.Join(words, " ")).Ceil()
	rest := font.MeasureString(bodyFace, strings.Join(words[drop:], " ")).Ceil()
	return float64(all - rest)
}

func TestTickerBlockKeepsTwoLines(t *testing.T) {
	if err := faces(); err != nil {
		t.Skip(err)
	}
	var f Frame
	tk := ticker{room: 120, block: true}
	tk.set("een twee drie vier vijf zes zeven acht negen tien elf twaalf dertien veertien")
	risen := false
	for i := 0; i < 400; i++ {
		tk.step(&f)
		if f.LiveOff > 0 {
			risen = true
		}
	}
	if tk.drop == 0 || !risen {
		t.Fatalf("no line went: drop %d, rose %v", tk.drop, risen)
	}
	if f.Live == nil || f.Live2 == nil || f.LiveOff != 0 || f.Live0 != nil {
		t.Fatalf("not settled on two lines: off %v", f.LiveOff)
	}
}
