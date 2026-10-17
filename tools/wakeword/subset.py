"""Copy the first N windows of a large (N, 16, 96) feature file.

    python subset.py <in.npy> <out.npy> <n>

openWakeWord's ACAV100M file is 17 GB; training reads it through the page
cache, which on a shared machine is memory taken from everything else. Its
windows are already in random order, so a leading slice is a fair sample.
"""
import sys

import numpy as np


def main():
    src, dst, n = sys.argv[1], sys.argv[2], int(sys.argv[3])
    a = np.load(src, mmap_mode="r")
    n = min(n, len(a))
    out = np.lib.format.open_memmap(dst, mode="w+", dtype=a.dtype, shape=(n,) + a.shape[1:])
    step = 50000
    for i in range(0, n, step):
        out[i:i + step] = a[i:min(i + step, n)]
    out.flush()
    print(out.shape, "->", dst)


if __name__ == "__main__":
    main()
