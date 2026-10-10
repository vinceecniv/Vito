"""Cut a stream of embeddings (eval_features.py stream) into training windows.

    python to_windows.py <stream.npy> <out.npy> [--stride 8]

The result is (N, 16, 96) float16, the shape train.py's feature_data_files
takes; stride 8 (0.64 s) overlaps windows by half.
"""
import argparse

import numpy as np


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("src")
    ap.add_argument("out")
    ap.add_argument("--stride", type=int, default=8)
    a = ap.parse_args()
    e = np.load(a.src)
    idx = np.arange(16)[None, :] + np.arange(0, len(e) - 15, a.stride)[:, None]
    w = e[idx].astype(np.float16)
    np.random.default_rng(5).shuffle(w)
    np.save(a.out, w)
    print(w.shape, "->", a.out)


if __name__ == "__main__":
    main()
