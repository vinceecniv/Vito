"""Turn evaluation audio into openWakeWord embeddings once, so every model
can be scored on it in seconds.

    python eval_features.py fleurs  <dir with .wav files>  <out.npy>   # hours of speech, one stream
    python eval_features.py clips   <dir with .wav clips>  <out.npz> [--noise <dir> --snr 10]

`fleurs`/`ami` style input is concatenated and cut into 10-minute pieces; the
result is an (N, 96) array of embeddings, one per 80 ms. Clip input gets 1 s
of room tone before and 0.5 s after each clip (optionally mixed with noise at
a given SNR) and becomes an (clips, frames, 96) array plus the file names.
"""
import argparse
import random
from pathlib import Path

import numpy as np
import soundfile as sf
from openwakeword.utils import AudioFeatures
from scipy.signal import resample_poly


def read_wav(p):
    """Any WAV/FLAC as 16 kHz mono int16."""
    a, sr = sf.read(str(p), dtype="float32", always_2d=True)
    a = a[:, 0]
    if sr != 16000:
        g = np.gcd(sr, 16000)
        a = resample_poly(a, 16000 // g, sr // g)
    return np.clip(a * 32767, -32768, 32767).astype(np.int16)


def stream(args, F):
    files = sorted(Path(args.src).rglob("*.wav"))
    out, total = [], 0
    piece = []
    plen = 0
    size = 16000 * 120  # two minutes at a time keeps memory small

    def flush(final=False):
        # Always exactly `size` samples: onnxruntime keeps a memory arena per
        # input shape, and pieces of varying length made it grow by gigabytes.
        nonlocal piece, plen, total
        while plen >= size or (final and plen > 0):
            x = np.concatenate(piece)
            take = min(size, len(x))
            chunk = np.pad(x[:take], (0, size - take))
            out.append(F._get_embeddings(chunk)[: max(1, (take - 12400) // 1280 + 1)])
            total += take
            rest = x[take:]
            piece, plen = ([rest] if len(rest) else []), len(rest)

    for f in files:
        try:
            a = read_wav(f)
        except Exception as e:
            print("skip", f, e)
            continue
        # long recordings are cut; short ones are joined
        piece.append(a)
        plen += len(a)
        flush()
    flush(final=True)
    emb = np.concatenate(out).astype(np.float16)
    np.save(args.out, emb)
    print(f"{len(files)} files, {total/16000/3600:.2f} h, {emb.shape} embeddings -> {args.out}")


def clips(args, F):
    files = sorted(Path(args.src).glob("*.wav"))
    noise = []
    if args.noise:
        for f in sorted(Path(args.noise).rglob("*.wav")):
            noise.append(read_wav(f).astype(np.float32))
    rng = random.Random(7)
    L = None
    batch = []
    for f in files:
        a = read_wav(f).astype(np.float32)
        a = a[: 16000 * 3]
        x = np.concatenate([np.zeros(16000, np.float32), a, np.zeros(8000, np.float32)])
        n = 16000 * 5  # every clip the same length: 5 s
        x = np.pad(x, (0, max(0, n - len(x))))[:n]
        if noise:
            src = rng.choice(noise)
            if len(src) < n:
                src = np.tile(src, n // len(src) + 1)
            o = rng.randrange(0, len(src) - n + 1)
            nz = src[o:o + n]
            ps = np.mean(a ** 2) + 1e-9
            pn = np.mean(nz ** 2) + 1e-9
            nz = nz * np.sqrt(ps / (pn * 10 ** (args.snr / 10)))
            x = x + nz
        else:
            # a little room tone, as a real microphone always has
            x = x + np.random.default_rng(len(batch)).normal(0, 30, n)
        peak = np.max(np.abs(x))
        if peak > 32767:
            x = x * (32767 / peak)
        batch.append(x.astype(np.int16))
    X = np.stack(batch)
    emb = F.embed_clips(X, batch_size=64)
    np.savez_compressed(args.out, emb=emb.astype(np.float16), names=np.array([f.name for f in files]))
    print(f"{len(files)} clips -> {emb.shape} {args.out}")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("kind", choices=["stream", "clips"])
    ap.add_argument("src")
    ap.add_argument("out")
    ap.add_argument("--noise")
    ap.add_argument("--snr", type=float, default=10)
    ap.add_argument("--ncpu", type=int, default=8)
    a = ap.parse_args()
    F = AudioFeatures(inference_framework="onnx", ncpu=a.ncpu)
    (stream if a.kind == "stream" else clips)(a, F)


if __name__ == "__main__":
    main()
