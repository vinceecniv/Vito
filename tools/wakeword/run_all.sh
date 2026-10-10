#!/bin/bash
# Everything after clip generation, in order, each step in a systemd scope with
# a memory cap — the page cache counts towards it, so a big feature file read
# through mmap can't take the machine's memory with it. Waits for gen_clips.py
# (running separately, log in ~/ww/gen_all.log) where it needs its clips.
#   nohup bash run_all.sh > ~/ww/run_all.log 2>&1 &
T=$(cd "$(dirname "$0")" && pwd)
D=/mnt/e/vito-wakeword/data
source ~/ww/venv/bin/activate
export PYTHONPATH=~/ww/piper-sample-generator
cap() { local m=$1; shift; systemd-run --scope -q -p MemoryMax=$m -p MemorySwapMax=0 --setenv=PATH="$PATH" --setenv=PYTHONPATH="$PYTHONPATH" "$@"; }
step() { echo "=== $(date '+%F %T') $*"; }

# --- general negatives ---------------------------------------------------
step "ACAV subset"
[ -f $D/acav_1500k.npy ] || cap 3G python $T/subset.py $D/openwakeword_features_ACAV100M_2000_hrs_16bit.npy $D/acav_1500k.npy 1500000

step "MLS windows"
mkdir -p $D/mls_parts
for f in $D/mls/*.parquet; do
  b=$(basename $f .parquet); h=10; case $b in dutch*) h=20;; esac
  [ -f $D/mls_parts/$b.npy ] || echo "$f $h $b"
done | xargs -P 2 -L 1 bash -c 'cap() { systemd-run --scope -q -p MemoryMax=4G -p MemorySwapMax=0 --setenv=PATH="$PATH" "$@"; }; cap python '"$T"'/prep_mls.py $0 --out '"$D"'/mls_parts/$2.npy --hours-per-shard $1 --ncpu 2'
[ -f $D/mls_windows.npy ] || cap 6G python $T/prep_mls.py --join $D/mls_parts/*.npy --out $D/mls_windows.npy

step "MUSAN windows"
[ -f $D/musan_nm_stream.npy ] || cap 4G python $T/eval_features.py stream $D/musan_nm $D/musan_nm_stream.npy --ncpu 2
[ -f $D/musan_windows.npy ] || cap 4G python $T/to_windows.py $D/musan_nm_stream.npy $D/musan_windows.npy --stride 8

# --- per wake word -------------------------------------------------------
n=0
mkdir -p /mnt/e/vito-wakeword/work_clean
for W in hey_vito oke_vito; do
  n=$((n+1))
  step "waiting for $W clips"
  until [ "$(grep -c 'heldout_negative ' ~/ww/gen_all.log)" -ge $n ]; do sleep 60; done
  step "training $W"
  cap 12G bash $T/train.sh $W > ~/ww/train_$W.log 2>&1 || echo "train $W failed"
  step "evaluating $W"
  cap 8G bash $T/eval_word.sh $W > ~/ww/eval_$W.log 2>&1 || echo "eval $W failed"
  step "training $W without ACAV"
  [ -e /mnt/e/vito-wakeword/work_clean/$W ] || ln -s ../work/$W /mnt/e/vito-wakeword/work_clean/$W
  cap 12G bash $T/train.sh $W config_clean.yml > ~/ww/train_clean_$W.log 2>&1 || echo "train clean $W failed"
  step "evaluating $W without ACAV"
  cap 8G bash $T/eval_word.sh $W /mnt/e/vito-wakeword/work_clean/$W.onnx > ~/ww/eval_clean_$W.log 2>&1 || echo "eval clean $W failed"
done
step ALL-DONE
