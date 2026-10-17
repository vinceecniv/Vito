"""Turn Multilingual LibriSpeech shards into negative training windows.

Each shard's audio becomes openWakeWord embeddings, cut into windows of 16
(1.28 s, the classifier's input) that are saved as one (N, 16, 96) float16
array — the format train.py's feature_data_files expects.

    python prep_mls.py <shard.parquet ...> --out <out.npy> [--hours-per-shard 20]
    python prep_mls.py --join <part.npy ...> --out <out.npy>
"""
import argparse
import io
from pathlib import Path

import numpy as np
import pyarrow.parquet as pq
import soundfile as sf
from openwakeword.utils import AudioFeatures
from scipy.signal import resample_poly


def decode(b):
    a, sr = sf.read(io.BytesIO(b), dtype="float32", always_2d=True)
    a = a[:, 0]
    if sr != 16000:
        g = np.gcd(sr, 16000)
        a = resample_poly(a, 16000 // g, sr // g)
    return np.clip(a * 32767, -32768, 32767).astype(np.int16)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("src", nargs="+")
    ap.add_argument("--out", required=True)
    ap.add_argument("--join", action="store_true", help="concatenate and shuffle .npy parts")
    ap.add_argument("--hours-per-shard", type=float, default=20)
    ap.add_argument("--ncpu", type=int, default=2)
    a = ap.parse_args()
    if a.join:
        W = np.concatenate([np.load(p) for p in a.src])
        np.random.default_rng(3).shuffle(W)
        np.save(a.out, W)
        print("windows", W.shape, "->", a.out)
        return
    F = AudioFeatures(inference_framework="onnx", ncpu=a.ncpu)
    windows = []
    for shard in map(Path, a.src):
        pf = pq.ParquetFile(shard)
        budget = a.hours_per_shard * 3600 * 16000
        got = 0
        piece, plen = [], 0
        for batch in pf.iter_batches(batch_size=64, columns=["audio"]):
            for row in batch.column("audio").to_pylist():
                try:
                    x = decode(row["bytes"])
                except Exception as e:
                    print("skip", e)
                    continue
                piece.append(x)
                plen += len(x)
                # Always exactly two minutes: onnxruntime keeps a memory arena
                # per input shape, and varying lengths made it grow by gigabytes.
                size = 16000 * 120
                while plen >= size:
                    y = np.concatenate(piece)
                    emb = F._get_embeddings(y[:size])
                    n = len(emb) // 16 * 16
                    windows.append(emb[:n].reshape(-1, 16, 96).astype(np.float16))
                    got += size
                    piece, plen = [y[size:]], len(y) - size
            if got >= budget:
                break
        print(f"{shard.name}: {got/16000/3600:.1f} h", flush=True)
    W = np.concatenate(windows)
    np.random.default_rng(3).shuffle(W)
    np.save(a.out, W)
    print("windows", W.shape, "->", a.out)


if __name__ == "__main__":
    main()
