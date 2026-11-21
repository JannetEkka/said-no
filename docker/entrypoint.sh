#!/usr/bin/env bash
# Write the config for SAIDNO_CONDITION (with|without) and run Bifrost in the foreground.
set -euo pipefail
mkdir -p /app/data
python3 /app/bench/make_config.py /app/data "${SAIDNO_CONDITION:-with}" /app/bin/decoy /app/bin/said-no.so 8080
export DECOY_LOG=/app/data/decoy.jsonl
exec /app/bin/bifrost-http -app-dir /app/data -host 0.0.0.0 -port 8080
