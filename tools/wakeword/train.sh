#!/bin/bash
# Train one wake word from the clips gen_clips.py made:
#   bash train.sh hey_vito
# Augments the clips into openWakeWord features (once), then trains; the
# model lands in the output_dir of config.yml as <word>.onnx.
set -e
W=$1
HERE=$(cd "$(dirname "$0")" && pwd)
source ~/ww/venv/bin/activate
export PYTHONPATH=~/ww/piper-sample-generator
sed "s/MODEL/$W/g" "$HERE/config.yml" > ~/ww/cfg_$W.yml
cd ~/ww/openWakeWord
python openwakeword/train.py --training_config ~/ww/cfg_$W.yml --augment_clips
python openwakeword/train.py --training_config ~/ww/cfg_$W.yml --train_model
