# Wake word models

- `melspectrogram.onnx`, `embedding_model.onnx` — openWakeWord's feature
  networks (https://github.com/dscripka/openWakeWord, Apache License 2.0):
  the melspectrogram and a port of Google's `speech_embedding`
  (https://tfhub.dev/google/speech_embedding/1, Apache License 2.0). Shared by
  every wake word.
- `hey_vito.onnx`, `oke_vito.onnx` — Vito's own classifiers, trained with
  `tools/wakeword` (see its README for the data and how to reproduce them).

A classifier is any openWakeWord model: input `(1, 16, 96)` embeddings,
output one score. Dropping another `<name>.onnx` here adds a phrase.
