# Training Vito's wake words

Vito's wake words ("Hey Vito", "Okay Vito") are [openWakeWord] classifiers:
a small network that looks at the last 1.28 s of Google's speech embedding
and says how likely it is that the phrase was just said. The app runs them in
pure Go (`internal/wakeword`); this directory makes them.

Everything here runs on a Linux machine (or WSL2) with an NVIDIA GPU; the
scripts assume `~/ww` for the Python side and `/mnt/e/vito-wakeword` for the
data, so adjust the paths for another box.

## Setup

```sh
mkdir ~/ww && cd ~/ww
git clone https://github.com/dscripka/openWakeWord
git clone https://github.com/rhasspy/piper-sample-generator
uv venv -p 3.11 venv && source venv/bin/activate
uv pip install torch==2.5.1 torchaudio==2.5.1 --index-url https://download.pytorch.org/whl/cu121
uv pip install -e ./openWakeWord -e ./piper-sample-generator torchinfo 'torchmetrics<1' \
  audiomentations==0.33.0 torch-audiomentations pronouncing deep-phonemizer mutagen pyyaml \
  onnx onnxruntime acoustics soundfile edge-tts 'speechbrain<1' 'datasets<3' 'numpy<2.1' \
  'scipy==1.14.1' pyarrow
export PYTHONPATH=~/ww/piper-sample-generator   # piper_train lives beside the package
sudo apt install espeak-ng ffmpeg sox
```

## Data

| What | Use | Licence |
|---|---|---|
| Piper speaker-mixing generators: `en_US-libritts_r`, `nl_NL-mls`, `de_DE-mls`, `fr_FR-mls` (piper-sample-generator v2.0.0 release) | positive and confusable clips | MIT / CC BY 4.0 data |
| Piper voices in 12 languages (`voices.json` on rhasspy/piper-voices) | more accents; separate voices held out for testing | per voice, mostly CC BY / CC0 |
| Microsoft neural voices via `edge-tts` | held-out test only | — (not in any model) |
| MUSAN noise, music, speech (openslr 17) | background mixed into training clips | CC BY 4.0 |
| MIT environmental impulse responses | room acoustics | CC BY 4.0 |
| openWakeWord ACAV100M features (2000 h) | general negatives | **CC BY-NC-SA 4.0** |
| Multilingual LibriSpeech: 4 Dutch shards + de, fr, es, it, pt, pl | general negatives in Vito's languages | CC BY 4.0 |
| FLEURS Dutch (9 h), AMI meetings (5.8 h), openWakeWord validation set (11 h) | false-accept test only | CC BY 4.0 |

## Steps

```sh
python gen_clips.py --word hey_vito --out /mnt/e/vito-wakeword/work --edge   # clips, ~2 h
python prep_mls.py mls/*.parquet --out mls_windows.npy                      # MLS negatives
python eval_features.py stream fleurs_nl feats/fleurs_nl.npy                  # test streams
bash train.sh hey_vito                                                       # augment + train
bash eval_word.sh hey_vito                                                   # report_hey_vito.json
python make_reference.py <models> hey_vito.onnx <clip.wav> ../../internal/wakeword/testdata
```

`make_reference.py` writes the onnxruntime outputs the Go tests compare
against; run it again whenever a model changes, and copy the model to
`internal/wakeword/models/`.

## What the numbers mean

- **Recall** — a held-out clip (a voice training never heard) counts as
  detected when any 1.28 s window in it reaches the threshold. Measured clean
  and with meeting babble at 10 and 5 dB SNR.
- **False accepts per hour** — on hours of speech and meetings, every run of
  windows above the threshold counts once, with 2 s of quiet before the next:
  that is how often Vito would start a dictation uninvited.
- **Confusables** — share of near-misses ("hé video", "Vito" alone, the other
  wake phrase) that would wake it.

[openWakeWord]: https://github.com/dscripka/openWakeWord
