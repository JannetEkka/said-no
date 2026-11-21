#!/usr/bin/env bash
# Start one Bifrost for a test condition.
# Usage: BIFROST_BIN=/path/to/bifrost-http bench/start.sh <with|without> <port> <run_dir>
# Needs GEMINI_API_KEY (and TAVILY_API_KEY, unless Tavily runs keyless) in the environment.
set -euo pipefail
cond="$1"; port="$2"; dir="$3"
here="$(cd "$(dirname "$0")/.." && pwd)"
mkdir -p "$dir"
rm -f "$dir"/*.db "$dir"/*.db-*
python3 "$here/bench/make_config.py" "$dir" "$cond" "$here/decoy/build/decoy" "$here/plugin/build/said-no.so" "$port"
cd "$dir"
DECOY_LOG="$dir/decoy.jsonl" nohup "${BIFROST_BIN:?set BIFROST_BIN}" -app-dir "$dir" -port "$port" > "$dir/bifrost.log" 2>&1 &
for _ in $(seq 1 90); do
  if curl -s -o /dev/null "http://127.0.0.1:$port/health"; then echo "bifrost ($cond) up on :$port"; exit 0; fi
  sleep 2
done
echo "bifrost ($cond) did not start; see $dir/bifrost.log" >&2; exit 1
