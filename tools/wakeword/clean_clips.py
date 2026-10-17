"""Drop clips that would poison training: unreadable or empty files, and TTS
output that ran away (a two-word phrase lasting seconds is babble, not the
phrase).

    python clean_clips.py <word dir> [--max-positive 3.0]
"""
import argparse
import wave
from concurrent.futures import ProcessPoolExecutor
from pathlib import Path


def check(args):
    path, max_s = args
    try:
        with wave.open(str(path)) as w:
            n, sr = w.getnframes(), w.getframerate()
        if sr != 16000 or n < sr * 0.25 or (max_s and n > sr * max_s):
            raise ValueError
    except Exception:
        Path(path).unlink(missing_ok=True)
        return 1
    return 0


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("root")
    ap.add_argument("--max-positive", type=float, default=3.0)
    ap.add_argument("--max-negative", type=float, default=5.0)
    a = ap.parse_args()
    for d in sorted(Path(a.root).iterdir()):
        if not d.is_dir():
            continue
        max_s = a.max_positive if "positive" in d.name else a.max_negative
        files = list(d.glob("*.wav"))
        with ProcessPoolExecutor(max_workers=8) as ex:
            dropped = sum(ex.map(check, [(f, max_s) for f in files], chunksize=128))
        print(f"{d.name}: dropped {dropped} of {len(files)}", flush=True)


if __name__ == "__main__":
    main()
