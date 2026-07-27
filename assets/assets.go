// Package assets embeds the feedback sounds (16-bit PCM WAV, < 150 ms each).
package assets

import _ "embed"

//go:embed sounds/start.wav
var SoundStart []byte

//go:embed sounds/done.wav
var SoundDone []byte

//go:embed sounds/cancel.wav
var SoundCancel []byte

// SoundCommand is a bright ascending chime confirming a spoken command was
// understood ("Vito, vertaal naar Duits") — a positive "here it comes" cue,
// distinct from the plain start/done blips.
//
//go:embed sounds/command.wav
var SoundCommand []byte

// SoundAchievement is a short rising arpeggio played when a milestone is
// unlocked. Longer than the feedback blips (~0.85 s) because it's a small
// celebration, not a status cue.
//
//go:embed sounds/achievement.wav
var SoundAchievement []byte
