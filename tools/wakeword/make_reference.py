"""Write reference outputs that internal/wakeword's tests compare against.

For each network: a fixed input and onnxruntime's output. For the whole
pipeline: a clip streamed through openWakeWord in 80 ms chunks, with the
score after every chunk. Files are little-endian float32 with a small header
(rank, dims...), read by testdata_test.go.

    python make_reference.py <models dir> <wake word .onnx> <clip.wav> <out dir>
"""
import struct
import sys
import wave
from pathlib import Path

import numpy as np
import onnxruntime as ort


def save(path, a):
    a = np.asarray(a, dtype=np.float32)
    with open(path, "wb") as f:
        f.write(struct.pack("<i", a.ndim))
        f.write(struct.pack(f"<{a.ndim}i", *a.shape))
        f.write(a.astype("<f4").tobytes())


def main():
    models, ww, clip, out = map(Path, sys.argv[1:5])
    out.mkdir(parents=True, exist_ok=True)
    rng = np.random.default_rng(1)

    mel = ort.InferenceSession(str(models / "melspectrogram.onnx"))
    x = rng.integers(-3000, 3000, size=(1, 1760)).astype(np.float32)
    save(out / "mel_in.f32", x)
    save(out / "mel_out.f32", mel.run(None, {"input": x})[0])

    emb = ort.InferenceSession(str(models / "embedding_model.onnx"))
    x = rng.normal(1.0, 1.0, size=(1, 76, 32, 1)).astype(np.float32)
    save(out / "emb_in.f32", x)
    save(out / "emb_out.f32", emb.run(None, {"input_1": x})[0])

    cls = ort.InferenceSession(str(ww))
    name = cls.get_inputs()[0].name
    x = rng.normal(0, 1, size=(1, 16, 96)).astype(np.float32)
    save(out / "ww_in.f32", x)
    save(out / "ww_out.f32", cls.run(None, {name: x})[0])

    # The whole pipeline, as openWakeWord streams it.
    from openwakeword.model import Model
    m = Model(wakeword_models=[str(ww)], inference_framework="onnx")
    # Start from a known feature buffer instead of openWakeWord's random one.
    m.preprocessor.feature_buffer = np.zeros_like(m.preprocessor.feature_buffer)
    with wave.open(str(clip)) as w:
        audio = np.frombuffer(w.readframes(w.getnframes()), dtype=np.int16)
    audio = np.concatenate([np.zeros(16000, np.int16), audio, np.zeros(16000, np.int16)])
    audio = audio[: len(audio) // 1280 * 1280]
    key = list(m.models.keys())[0]
    scores = []
    for i in range(0, len(audio), 1280):
        scores.append(m.predict(audio[i:i + 1280])[key])
    save(out / "stream_in.f32", audio.astype(np.float32))
    save(out / "stream_out.f32", np.array(scores))
    print("max score", max(scores))


if __name__ == "__main__":
    main()
