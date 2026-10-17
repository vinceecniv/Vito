"""Score wake word models on the evaluation features (eval_features.py).

    python evaluate.py --model hey_vito.onnx --feats /mnt/e/vito-wakeword/data/eval/feats \
        --clips heldout_positive_clean.npz heldout_positive_ami10.npz ... --negclips heldout_negative.npz \
        --out report.json

Recall: a held-out clip counts as detected when any 1.28 s window in it
scores at or above the threshold. False accepts: on long streams, every
stretch of windows at or above the threshold counts once, and a new one only
after 2 s below it — the way Vito would react.
"""
import argparse
import json
from pathlib import Path

import numpy as np
import onnxruntime as ort

THRESHOLDS = [round(t, 2) for t in [0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.75, 0.8, 0.85, 0.9, 0.95, 0.97, 0.99]]
STEP_S = 0.08


def lang_of(name):
    n = Path(name).name
    if n.startswith("edge-"):
        n = n[5:]
    return n[:2]


class Scorer:
    def __init__(self, path):
        self.s = ort.InferenceSession(str(path), providers=["CPUExecutionProvider"])
        self.inp = self.s.get_inputs()[0].name
        # some models are exported for one window at a time
        self.batch = 1 if self.s.get_inputs()[0].shape[0] == 1 else 4096

    def __call__(self, windows):
        out = []
        for i in range(0, len(windows), self.batch):
            out.append(self.s.run(None, {self.inp: windows[i:i + self.batch].astype(np.float32)})[0].reshape(-1))
        return np.concatenate(out) if out else np.zeros(0)


def windows_of(emb):
    """All 16-embedding windows of an (N, 96) array, as (N-15, 16, 96)."""
    idx = np.arange(16)[None, :] + np.arange(len(emb) - 15)[:, None]
    return emb[idx]


def stream_scores(score, emb):
    out = []
    for i in range(0, len(emb), 50000):
        part = emb[max(0, i - 15): i + 50000]
        out.append(score(windows_of(part)))
    return np.concatenate(out)


def count_activations(scores, thr, refractory_steps=25):
    n, quiet, active = 0, refractory_steps, False
    for s in scores:
        if s >= thr:
            if not active and quiet >= refractory_steps:
                n += 1
            active, quiet = True, 0
        else:
            active = False
            quiet += 1
    return n


def clip_max(score, emb):
    c, f, d = emb.shape
    w = np.stack([windows_of(e) for e in emb])  # c, f-15, 16, 96
    s = score(w.reshape(-1, 16, d)).reshape(c, -1)
    return s.max(axis=1)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--model", required=True)
    ap.add_argument("--feats", required=True, help="dir with stream .npy files")
    ap.add_argument("--streams", nargs="*", default=["fleurs_nl.npy", "ami.npy", "validation_set_features.npy"])
    ap.add_argument("--clips", nargs="*", default=[], help="positive clip sets (.npz)")
    ap.add_argument("--negclips", nargs="*", default=[], help="confusable clip sets (.npz)")
    ap.add_argument("--out")
    a = ap.parse_args()

    score = Scorer(a.model)
    F = Path(a.feats)
    rep = {"model": Path(a.model).name, "false_accepts_per_hour": {}, "recall": {}, "confusables": {}}

    for s in a.streams:
        p = F / s
        if not p.exists():
            continue
        emb = np.load(p).astype(np.float32)
        if emb.ndim == 3:  # openWakeWord's validation set: already windows
            sc = score(emb)
        else:
            sc = stream_scores(score, emb)
        hours = len(sc) * STEP_S / 3600
        rep["false_accepts_per_hour"][s] = {"hours": round(hours, 2),
                                            **{str(t): round(count_activations(sc, t) / hours, 3) for t in THRESHOLDS}}

    for c in a.clips:
        z = np.load(F / c)
        m = clip_max(score, z["emb"].astype(np.float32))
        names = z["names"]
        r = {"n": int(len(m)), "all": {str(t): round(float((m >= t).mean()), 3) for t in THRESHOLDS}, "by_language": {}}
        for lang in sorted(set(lang_of(n) for n in names)):
            sel = np.array([lang_of(n) == lang for n in names])
            r["by_language"][lang] = {"n": int(sel.sum()), **{str(t): round(float((m[sel] >= t).mean()), 3) for t in THRESHOLDS}}
        rep["recall"][Path(c).stem] = r

    for c in a.negclips:
        z = np.load(F / c)
        m = clip_max(score, z["emb"].astype(np.float32))
        rep["confusables"][Path(c).stem] = {"n": int(len(m)), **{str(t): round(float((m >= t).mean()), 3) for t in THRESHOLDS}}

    js = json.dumps(rep, indent=1)
    print(js)
    if a.out:
        Path(a.out).write_text(js)


if __name__ == "__main__":
    main()
