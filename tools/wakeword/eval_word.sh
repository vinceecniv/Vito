#!/bin/bash
# Evaluate a trained wake word on everything it never trained on:
#   bash eval_word.sh hey_vito [model.onnx]
# Held-out voices (clean, and mixed with meeting babble at 10 and 5 dB),
# confusable phrases, and hours of Dutch speech, meetings and openWakeWord's
# validation set for false accepts. Writes report_<word>.json beside the model.
set -e
W=$1
WORK=/mnt/e/vito-wakeword/work
FEATS=/mnt/e/vito-wakeword/data/eval/feats
MODEL=${2:-$WORK/$W.onnx}
HERE=$(cd "$(dirname "$0")" && pwd)
source ~/ww/venv/bin/activate
cd $FEATS
[ -e validation_set_features.npy ] || ln -s /mnt/e/vito-wakeword/data/validation_set_features.npy .
NOISE=/mnt/e/vito-wakeword/data/eval/ami
[ -f ${W}_pos_clean.npz ] || python $HERE/eval_features.py clips $WORK/$W/heldout_positive ${W}_pos_clean.npz
[ -f ${W}_pos_babble10.npz ] || python $HERE/eval_features.py clips $WORK/$W/heldout_positive ${W}_pos_babble10.npz --noise $NOISE --snr 10
[ -f ${W}_pos_babble5.npz ] || python $HERE/eval_features.py clips $WORK/$W/heldout_positive ${W}_pos_babble5.npz --noise $NOISE --snr 5
[ -f ${W}_neg.npz ] || python $HERE/eval_features.py clips $WORK/$W/heldout_negative ${W}_neg.npz
python $HERE/evaluate.py --model $MODEL --feats $FEATS \
  --clips ${W}_pos_clean.npz ${W}_pos_babble10.npz ${W}_pos_babble5.npz \
  --negclips ${W}_neg.npz --out $(dirname $MODEL)/report_$(basename $MODEL .onnx).json
