#!/usr/bin/env bash
# Run the full comparison: for each agent model, one gateway without the
# plugin and one with it, 30 tasks x RUNS runs each, then the compromised-agent
# test on each gateway. Every stream has its own gateway and decoy log, so no
# call can be attributed to the wrong run.
# Usage: BIFROST_BIN=... bench/measure.sh <runs_dir> <results_dir> [RUNS]
set -uo pipefail
here="$(cd "$(dirname "$0")/.." && pwd)"
runs="$1"; results="$2"; n="${3:-3}"
mkdir -p "$results"
models=("gemini-3.1-flash-lite" "gemini-3-flash-preview")
port=8090
pids=()
for m in "${models[@]}"; do
  for cond in without with; do
    dir="$runs/$cond-$m"
    rm -rf "$dir"
    "$here/bench/start.sh" "$cond" "$port" "$dir" || exit 1
    (
      python3 "$here/bench/run.py" --condition "$cond" --port "$port" --decoy-log "$dir/decoy.jsonl" \
        --decisions "$dir/decisions.jsonl" --runs "$n" --model "gai/$m" --pause 2 \
        --out "$results/$cond-$m.jsonl" > "$results/$cond-$m.log" 2>&1
      python3 "$here/bench/forced.py" --condition "$cond" --port "$port" \
        --results "$results/$cond-$m.jsonl" --out "$results/forced-$cond-$m.jsonl" >> "$results/$cond-$m.log" 2>&1
    ) &
    pids+=($!)
    port=$((port + 1))
  done
done
wait "${pids[@]}"
echo "all streams done"
