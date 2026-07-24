// Package assets embeds the feedback sounds (16-bit PCM WAV, < 150 ms each).
package assets

import _ "embed"

//go:embed sounds/start.wav
var SoundStart []byte

//go:embed sounds/done.wav
var SoundDone []byte

//go:embed sounds/cancel.wav
var SoundCancel []byte

// SoundAchievement is a short rising arpeggio played when a milestone is
// unlocked. Longer than the feedback blips (~0.85 s) because it's a small
// celebration, not a status cue.
//
//go:embed sounds/achievement.wav
var SoundAchievement []byte
