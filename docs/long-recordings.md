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
  "Upload").
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

- Re-transcribe the whole audio with speaker recognition (Soniox or
  AssemblyAI), clean per turn, name speakers once ("Speaker 1" → a name),
  applied everywhere.
- Listen back: click a word or time and the audio plays from there.
- A cost estimate first, as with uploads.
- Local speech recognition has no speaker recognition.

## Phase 3: online meetings and summaries

- Record the computer's own sound for online meetings (different per OS).
- Summary and action points through Vito Assist.
- A note that the others must be asked for consent, because a meeting records
  them too.

## Phase 1 storage layout

`<UserConfigDir>/vito/sessions/<id>/`:

- `audio.wav` — the whole recording; header patched every few seconds and on
  close, repaired at start-up when the daemon died mid-session.
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
