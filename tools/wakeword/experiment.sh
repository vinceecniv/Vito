#!/bin/bash
# Train variants of one word on its existing features and evaluate each:
#   bash experiment.sh hey_vito "w300:max_negative_weight=300,target_false_positives_per_hour=1.0" ...
# Each variant is name:key=value,...; results in /mnt/e/vito-wakeword/work_exp/<name>/.
W=$1; shift
T=$(cd "$(dirname "$0")" && pwd)
source ~/ww/venv/bin/activate
export PYTHONPATH=~/ww/piper-sample-generator CUDA_VISIBLE_DEVICES=
for spec in "$@"; do
  name=${spec%%:*}; sets=${spec#*:}
  O=/mnt/e/vito-wakeword/work_exp/$name
  mkdir -p $O; [ -e $O/$W ] || ln -s ../../work/$W $O/$W
  python - "$T/config.yml" "$O" "$W" "$sets" > ~/ww/cfg_exp_$W.yml <<'PY'
import sys, yaml
cfg = yaml.safe_load(open(sys.argv[1]).read().replace("MODEL", sys.argv[3]))
cfg["output_dir"] = sys.argv[2]
for kv in filter(None, sys.argv[4].split(",")):
    k, v = kv.split("=")
    cfg[k] = yaml.safe_load(v)
print(yaml.safe_dump(cfg))
PY
  echo "=== $(date '+%T') $name: $sets"
  (cd ~/ww/openWakeWord && python -W ignore openwakeword/train.py --training_config ~/ww/cfg_exp_$W.yml --train_model 2>&1 \
     | tr '\r' '\n' | grep -E "^Final Model") || true
  if [ -s $O/$W.onnx ]; then
    bash $T/eval_word.sh $W $O/$W.onnx > $O/eval.log 2>&1
    python $T/summarize.py $O/report_$W.json
  else
    echo "  no model"
  fi
done
echo EXP-DONE
