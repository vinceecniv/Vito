package audio

import (
	"encoding/binary"
	"testing"

	"vito/assets"
)

// TestEmbeddedSoundsParse guards the feedback sounds themselves, not the code
// around them. achievement.wav shipped broken twice over: its RIFF and data
// lengths were left at 0, and behind that header sat 0.85 s of pure silence —
// the generator had written the container and never the audio. Playback
// "succeeded" both times. Nothing failed, nothing logged; the sound was simply
// absent.
//
// Hence the amplitude check. A test that only parses the header would have
// passed on a file full of zeroes, which is exactly the trap this fell into.
func TestEmbeddedSoundsParse(t *testing.T) {
	sounds := map[string][]byte{
		"start":       assets.SoundStart,
		"done":        assets.SoundDone,
		"cancel":      assets.SoundCancel,
		"command":     assets.SoundCommand,
		"achievement": assets.SoundAchievement,
		"warn":        assets.SoundWarn,
	}

	for name, wav := range sounds {
		t.Run(name, func(t *testing.T) {
			pcm, rate, channels, err := parseWAV(wav)
			if err != nil {
				t.Fatalf("parseWAV: %v", err)
			}
			if len(pcm) == 0 {
				t.Fatal("no samples: the header claims an empty data chunk")
			}
			// The declared length must account for the whole file, or the
			// header and the payload disagree again.
			if got, want := len(pcm), len(wav)-44; got != want {
				t.Errorf("data chunk covers %d bytes, file holds %d after the header", got, want)
			}
			if rate == 0 || channels == 0 {
				t.Errorf("rate=%d channels=%d, both must be non-zero", rate, channels)
			}

			// Audible signal, not just bytes. The threshold is far below the
			// ~35% peak these clips are mastered at, so it only catches a clip
			// that is silent or all but inaudible.
			var peak int16
			for i := 0; i+1 < len(pcm); i += 2 {
				v := int16(binary.LittleEndian.Uint16(pcm[i:]))
				if v < 0 {
					v = -v
				}
				if v > peak {
					peak = v
				}
			}
			if peak < 1000 {
				t.Errorf("peak amplitude %d of 32767 — this clip is silent", peak)
			}
		})
	}
}

// TestParseWAVEmptyDataChunk pins the recovery: a data length of 0 with samples
// behind it means a writer that never patched its header, and the remainder of
// the file is the audio.
func TestParseWAVEmptyDataChunk(t *testing.T) {
	full := WAVFromPCM(make([]byte, 960)) // 10 ms of silence at 48 kHz mono

	broken := append([]byte(nil), full...)
	broken[4], broken[5], broken[6], broken[7] = 36, 0, 0, 0    // RIFF size
	broken[40], broken[41], broken[42], broken[43] = 0, 0, 0, 0 // data size

	pcm, _, _, err := parseWAV(broken)
	if err != nil {
		t.Fatalf("parseWAV: %v", err)
	}
	if len(pcm) != 960 {
		t.Errorf("recovered %d bytes, want 960", len(pcm))
	}
}
