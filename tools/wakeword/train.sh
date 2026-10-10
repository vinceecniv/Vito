#!/bin/bash
# Train one wake word from the clips gen_clips.py made:
#   bash train.sh hey_vito [config_clean.yml]
# Augments the clips into openWakeWord features (once), then trains; the
# model lands in the config's output_dir as <word>.onnx.
set -e
W=$1
HERE=$(cd "$(dirname "$0")" && pwd)
CFG=${2:-config.yml}
source ~/ww/venv/bin/activate
export PYTHONPATH=~/ww/piper-sample-generator
sed "s/MODEL/$W/g" "$HERE/$CFG" > ~/ww/cfg_$W.yml
cd ~/ww/openWakeWord
python openwakeword/train.py --training_config ~/ww/cfg_$W.yml --augment_clips
python openwakeword/train.py --training_config ~/ww/cfg_$W.yml --train_model
