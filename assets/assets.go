// Package assets embeds the feedback sounds: 48 kHz mono 16-bit PCM WAV, around
// 150 ms for the status blips and up to a second for the celebratory ones.
//
// internal/audio's TestEmbeddedSoundsParse checks every clip here actually
// carries audio. That is not paranoia: achievement.wav once shipped as a valid
// container wrapped around pure silence, and nothing anywhere reported a fault.
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

// SoundWarn is two falling notes for a dictation that finished but not as
// asked — the AI cleanup failed and the raw transcript went in instead.
// Deliberately not SoundCancel: that one means nothing was delivered, while
// here you did get your text, just unpolished. Longer and lower than the status
// blips so it stands out from the "done" chime it follows.
//
//go:embed sounds/warn.wav
var SoundWarn []byte
