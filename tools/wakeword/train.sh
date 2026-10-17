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
# train.py ends by converting to TFLite, which Vito doesn't use (and needs
# onnx_tf); success is the .onnx it writes before that.
OUT=$(python -c "import yaml,sys; print(yaml.safe_load(open(sys.argv[1]))['output_dir'])" ~/ww/cfg_$W.yml)
rm -f "$OUT/$W.onnx"
python openwakeword/train.py --training_config ~/ww/cfg_$W.yml --train_model || true
test -s "$OUT/$W.onnx"
