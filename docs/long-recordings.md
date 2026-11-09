# Long recordings

A separate mode for lectures, meetings and long dictation sessions, beside
ordinary dictation. Agreed on 8 November 2026; phase 1 started 9 November 2026.

Why separate: dictation is about getting text to the cursor fast. A long
recording is about losing nothing, giving the text structure and working with
it afterwards. It is still "voice in, text out", and it fills the gap Wispr
Flow's Notetaker covers.

## Where it lives in the interface

- A menu item of its own, **Recordings**, between History and Achievements.
  Not a dashboard card (the dashboard is for a glance) and not part of History
  (one long document with times, speakers and audio is another kind of thing
  than a list of short dictations).
- **Overview**: the sessions (title, date, length, how far each is: raw,
  cleaned, speakers, summary), the button *New recording* and *Audio file*. A
  recording that was not closed (a crash) shows a banner here: restore or
  throw away.
- **Session screen**, one screen that grows with the phases: while recording a
  quiet focus view (large live text, timer, pause, Mark, Header, stop);
  afterwards the same screen is the document, with *Cleaned / Raw*, re-clean and
  export. Phase 2 adds speaker labels and an audio bar, phase 3 a summary panel.
- **Starting** also from the island's quick menu and the tray ("Start long
  recording"). While one runs the pill shows a red dot with a timer, also when
  Vito's window is closed; the island offers pause, mark and stop. The
  dictation key keeps dictating; it never silently changes mode.
- A long uploaded audio file becomes a session in the same list (label
  "From a file").
- Recordings do not count as dictations: not in "words today", saved typing
  time or History.
- Settings: one card "Long recordings" in an existing tab (clean per block,
  how long audio is kept, the consent note for meetings), not a tab of its own.

Sketch: the design canvas "Vito Opnames" (four screens: overview, while
recording, afterwards, pill and island).

## Header and Mark

Keys follow English: **H** = header, **M** = mark (in the session screen, when
no text field has the focus; also buttons there and in the island).

- **Header (H)** marks *where* a new subject starts; nothing has to be typed or
  said at that moment (from the island or a meeting you can't). The cleanup
  pass proposes a short title from what follows, shown as a grey divider with
  the time until then. A typed title (in the window, or afterwards) always wins
  and is never overwritten. Later: a short phrase spoken right after H becomes
  the title; re-cleaning may propose headers where none was set (an option,
  not the default).
- **Mark (M)** is a bookmark: "this mattered". It is meant for finding back,
  not for filling in later. Because you press it after hearing something, it
  marks the sentence(s) of roughly the last 15 seconds, highlighted afterwards
  and listed in the session's side column to jump to. Phase 2: a mark plays
  from there. Phase 3: summary and action points weigh marked passages more.
  An optional short note can be added to a mark.

## Audio storage

Record as **WAV** (16 kHz mono, ≈ 115 MB per hour): simplest to recover after
a crash (patch the header), what local Parakeet wants, trivially seekable.
When the session is finished — after the last cleaned block, so recovery still
has the WAV — convert to **MP3, 48 kbps mono** (≈ 21 MB per hour):

| Format | Per hour | Note |
|---|---|---|
| WAV | ~115 MB | only while recording |
| FLAC (lossless) | ~55 MB | little gain |
| MP3 48 kbps mono | ~21 MB | plays everywhere (Safari, any player after export); pure-Go encoder exists, no extra C library in three builds |
| Opus 24 kbps | ~11 MB | best for speech, but needs libopus through cgo |

Speaker recognition (phase 2) works fine on MP3 (Soniox and AssemblyAI take
it; no measurable difference at 48 kbps). Local re-transcription decodes it
back to WAV first. A setting "Keep audio: always / 30 days / text only"; the
text is always kept.

Built (`internal/longrec/compress.go`): stopping a session compresses it right
after the last block, restoring or recording on turns it back into a WAV
first (`EnsureWAV`, the codec delay of 1057 samples dropped so block times
still match) and compresses it again afterwards; at start-up every finished
session still holding a WAV is compressed (`CompressAll`). The encoder is a
fixed copy of shine-mp3 in `internal/mp3` (LGPL-2, the only non-MIT part —
see its README for the three fixes); every frame is exactly 216 bytes, which
`Compress` checks before the WAV goes. Decoding is go-mp3 (Apache-2.0).
Measured on a 4:21 recording: 8.4 MB → 1.57 MB in ~0.65 s.

## The recording's own folder

Every session is also kept as ordinary files under `long.folder` (default
`<Documents>/Vito/<"Recordings" in the UI language>`), one folder per session
named `yyyy-mm-dd hh.mm <title>`: `.docx`, `.md`, `.txt`, `.srt`, `.vtt` and,
once done, the `.mp3` (`internal/longrec/files.go`, `docx.go`). The daemon
rewrites them 2 s after every `long_doc` event, at start-up and when the
setting changes (folders move along; across drives they are written anew).
`files.json` in the session remembers the sha256 of what Vito wrote; a file
that no longer matches is the user's and is never overwritten — Vito's update
goes beside it as `<name> (Vito).<ext>` and the doc reports it in
`files.edited`. Deleting a session moves its folder to the trash
(`internal/osfiles`: SHFileOperation, ~/.Trash, the freedesktop trash); where
there is none it stays. The Export menu became "Open folder".

## An audio file as a recording

"Audio file" on the Recordings page: the browser decodes the file (anything
it plays) with `OfflineAudioContext` at 16 kHz, mixes it to mono 16-bit and
posts that WAV to `POST /api/long/import?title=`. `longrec.NewImport` writes
it as the session's audio (state `importing`, source `upload`); `CutPoints`
cuts it where a recording would be cut (first 0.7 s pause after 60 s, at the
latest after 150 s) and `Import` transcribes block after block from the file
(`transcribeSlice`), each one queued for cleanup as it lands. `meta.planned`
is the number of blocks, for "Transcribing n/N". A daemon stopped mid-import
carries on 20 s after the next start with the blocks still missing. When done
the audio is compressed like any recording. Uploads do not count for the
"longest recording" achievements.

## Recording settings

Settings → Output → Recordings: clean each block (`long.clean`), how long the
audio is kept (`long.keep_audio`: "" always, `30d`, `text` = not at all), the
folder, and a note to ask people's consent before recording them. Audio is
removed only from finished sessions that are not busy (`pruneLongAudio`: at
start-up, every 6 h, after each compression and when the setting changes);
the text and the folder's files stay, Vito's MP3 copy goes unless the user
changed it. A session without audio cannot be continued (the server refuses;
the page hides the button and shows "Text only" in the list).

## Phase 1: the mode itself

- **Nothing lost on a crash**: audio goes to disk as it comes; after a crash
  the WAV header is repaired; final speech-to-text pieces go straight into a
  log per session; at the next start Vito offers to restore.
- **Longer than the service allows**: the session is cut into blocks at
  natural pauses (about every 1–2 minutes, at the latest after ~2.5 minutes),
  each block its own streaming session, the next one opened before the
  previous one is finished, so no audio falls between them. That also removes
  every provider's session limit (Soniox, AssemblyAI) without depending on
  their reconnect behaviour. A block whose live session failed is transcribed
  from its slice of the WAV with the provider's file API.
- **Cleanup per block**, with the end of the previous block as context; a
  queue that respects Groq's free tier (one request at a time, backing off on a
  rate limit); the raw text is always kept; afterwards the whole text can be
  re-cleaned.
- **Own screen**: large live text, timer, pause/resume, Mark, Header.
- **Export**: Markdown, txt, SRT/VTT (times per sentence, spread over a block
  by length — blocks carry exact times, sentences an estimate).

## Phase 2: speakers and audio afterwards

Started 9 November 2026.

- **Listen back** (built): a player under the title (play, 15 s back and
  forward, position, speed, space bar). A paragraph's time plays from there;
  while it plays a click on a sentence jumps to it and the sentence being
  spoken lights up. The page fetches the audio once as a blob (an `<audio>`
  element cannot send the token).
- **Speakers** (built): "Recognise speakers" sends the whole audio once more
  to Soniox (`enable_speaker_diarization`) or AssemblyAI (`speaker_labels`) —
  the speech service in use when it can, else whichever has a key
  (`stt.SpeakerProvider`); a local engine or Whisper cannot. The turns go to
  `speakers.json` (speakers numbered in order of first speaking, a turn over
  1500 characters split at sentences) and replace the blocks in the document
  up to where the audio ended then; anything recorded on afterwards follows
  from the blocks. Turns are cleaned in chunks of about 2000 characters, one
  request each with the turns as `[n] text` lines (`speakers_clean.jsonl`); a
  reply that merges, drops or relabels a turn is refused and the raw turns
  stand. Names are given once in the card beside the text
  (`PUT /api/long/{id}/speakers/{n}`) and apply to the page, the copy and
  every file ("Name: …"); "Remove speakers" goes back to the blocks. The
  recognition is counted in the session's cost (`speaker_seconds`).
  Headers and marks are times, so they stay where they were.
- A cost estimate first: left for later (the user's call, 9 Nov 2026).
- Test audio: `E:ito-testaudioergadering-3-sprekers.wav` (+ `.txt`), a
  Dutch meeting of three Soniox TTS voices (`tts-rt-v2`, voices Maya, Daniel,
  Noah). Soniox's TTS endpoint refuses Python's default User-Agent
  (Cloudflare 1010); send one of your own.

## Phase 3: online meetings and summaries

Postponed (9 November 2026): less essential than phases 1 and 2; picked up later.

- Record the computer's own sound for online meetings (different per OS).
- Summary and action points through Vito Assist.
- A note that the others must be asked for consent, because a meeting records
  them too.

## Phase 1 storage layout

`<UserConfigDir>/vito/sessions/<id>/`:

- `audio.wav` — the whole recording; header patched every few seconds and on
  close, repaired at start-up when the daemon died mid-session. Becomes
  `audio.mp3` once the session is done.
- `session.json` — title, started, state (`recording`, `paused`,
  `interrupted`, `done`), duration, provider.
- `blocks.jsonl` — one line per finished block: index, start and end in
  seconds of audio, raw text; appended the moment a block is transcribed.
- `clean.jsonl` — one line per cleaned block: index, cleaned text, and a
  proposed header title where the block starts a section.
- `events.jsonl` — marks, headers (with a typed title when given), pauses and
  resumes, each at a time in seconds of audio.

Append-only logs, so a crash loses at most the block in progress, and that one
is recovered from the audio.
