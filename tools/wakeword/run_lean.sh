#!/bin/bash
# Train and evaluate one wake word on the CPU with tight memory caps — for a
# machine that shares its memory with other work. WSL keeps host memory a
# CUDA context touched, so the GPU is left alone here.
#   nohup bash run_lean.sh hey_vito > ~/ww/run_lean.log 2>&1 &
W=${1:-hey_vito}
T=$(cd "$(dirname "$0")" && pwd)
D=/mnt/e/vito-wakeword/data
source ~/ww/venv/bin/activate
export PYTHONPATH=~/ww/piper-sample-generator
export CUDA_VISIBLE_DEVICES=""
cap() { local m=$1; shift; systemd-run --scope -q -p MemoryMax=$m -p MemorySwapMax=0 --setenv=PATH="$PATH" --setenv=PYTHONPATH="$PYTHONPATH" --setenv=CUDA_VISIBLE_DEVICES= "$@"; }
step() { echo "=== $(date '+%F %T') $*"; }

step "MLS join"
[ -f $D/mls_windows.npy ] || cap 6G python $T/prep_mls.py --join $D/mls_parts/*.npy --out $D/mls_windows.npy
step "waiting for $W clips"
[ "$W" != hey_vito ] || until grep -q "GEN-HEY-OK" ~/ww/gen_all.log; do sleep 30; done
step "cleaning $W clips"
cap 4G python $T/clean_clips.py /mnt/e/vito-wakeword/work/$W
step "training $W"
cap 10G bash $T/train.sh $W > ~/ww/train_$W.log 2>&1 || echo "train $W failed"
step "evaluating $W"
cap 6G bash $T/eval_word.sh $W > ~/ww/eval_$W.log 2>&1 || echo "eval $W failed"
step "MUSAN windows"
[ -f $D/musan_nm_stream.npy ] || cap 3G python $T/eval_features.py stream $D/musan_nm $D/musan_nm_stream.npy --ncpu 2
[ -f $D/musan_windows.npy ] || cap 3G python $T/to_windows.py $D/musan_nm_stream.npy $D/musan_windows.npy --stride 8
step "training $W without ACAV"
mkdir -p /mnt/e/vito-wakeword/work_clean
[ -e /mnt/e/vito-wakeword/work_clean/$W ] || ln -s ../work/$W /mnt/e/vito-wakeword/work_clean/$W
cap 10G bash $T/train.sh $W config_clean.yml > ~/ww/train_clean_$W.log 2>&1 || echo "train clean $W failed"
step "evaluating $W without ACAV"
cap 6G bash $T/eval_word.sh $W /mnt/e/vito-wakeword/work_clean/$W.onnx > ~/ww/eval_clean_$W.log 2>&1 || echo "eval clean $W failed"
step LEAN-DONE
