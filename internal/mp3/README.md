# internal/mp3 — MP3 encoder (LGPL)

A copy of [braheezy/shine-mp3](https://github.com/braheezy/shine-mp3) v0.2.0
(`pkg/mp3`), the Go port of the [shine](https://github.com/toots/shine)
fixed-point MP3 encoder. It is used to store long recordings (internal/longrec)
as 48 kbps mono MP3 once they are finished.

**License: GNU Library General Public License v2** (see `LICENSE` here). This
directory is the only part of Vito under the LGPL; the rest is MIT. Vito's whole
source is public, so the encoder can be changed or replaced and Vito rebuilt
with it, as the LGPL asks.

## Changes from upstream

Measured on a 16 kHz mono recording: upstream's output failed to decode in
ffmpeg on nearly every frame. Three fixes, each needed:

1. `NewEncoderBitrate` sets the bitrate before the frame size is derived from it
   (upstream fixes 128 kbps, and changing it afterwards leaves stale values).
2. The side information of an MPEG-1 **mono** frame is 17 bytes, not 9 (that is
   MPEG-2's); shine's C code has 17.
3. The slots per frame are rounded before use: 48 kbps at 16 kHz came out as
   215.999… instead of 216, which set the padding bit on frames that carry no
   padding byte, and decoders lost the frame sync.

After these, 32, 48 and 128 kbps at 16 and 32 kHz decode without a single error
(ffmpeg), and the length matches the source to the frame.
