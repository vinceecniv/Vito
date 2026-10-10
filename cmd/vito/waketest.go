package main

import (
	"fmt"
	"os"
	"strings"

	"vito/internal/wakeword"
)

// wakeTest scores a recording with a wake word model and prints where it
// would have woken Vito — for trying a model on one's own voice.
func wakeTest(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: vito wake-test <file.wav> [%s]", strings.Join(wakeword.Phrases(), "|"))
	}
	phrase := "hey_vito"
	if len(args) > 1 {
		phrase = args[1]
	}
	b, err := os.ReadFile(args[0])
	if err != nil {
		return err
	}
	scores, err := wakeword.ScoreWAV(phrase, b)
	if err != nil {
		return err
	}
	var peak float32
	at := 0
	for i, s := range scores {
		if s > peak {
			peak, at = s, i
		}
		if s >= 0.1 {
			// steps are 80 ms; the recording starts after 1 s of padding
			fmt.Printf("%6.2f s  %4.2f %s\n", float64(i+1)*0.08-1, s, strings.Repeat("█", int(s*40)))
		}
	}
	fmt.Printf("peak %.2f at %.2f s — %s at the default sensitivity\n", peak, float64(at+1)*0.08-1,
		map[bool]string{true: "would wake Vito", false: "would not wake Vito"}[peak >= wakeword.Threshold(phrase, 0.5)])
	return nil
}
