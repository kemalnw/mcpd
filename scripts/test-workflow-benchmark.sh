#!/bin/sh
set -eu
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
TMP=$(mktemp -d); PID=""
cleanup(){ [ -z "$PID" ] || kill "$PID" 2>/dev/null || true; rm -rf "$TMP"; }
trap cleanup EXIT HUP INT TERM
PORT=$((22000 + $$ % 1000))
cat >"$TMP/config.toml" <<EOF
[server]
listen = "127.0.0.1:$PORT"
mcp_path = "/mcp"
shutdown_seconds = 2
[process]
batch_max_parallel = 8
batch_global_parallel = 8

[audit]
enabled = false
[auth]
enabled = false
EOF
CGO_ENABLED=0 go build -o "$TMP/mcpd" "$ROOT/cmd/mcpd"
MCPD_CONFIG="$TMP/config.toml" "$TMP/mcpd" serve >"$TMP/server.log" 2>&1 & PID=$!
for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do curl -fsS "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1 && break; sleep 0.1; done
curl -fsS "http://127.0.0.1:$PORT/healthz" >/dev/null
"$ROOT/scripts/benchmark-workflow.py" "http://127.0.0.1:$PORT" --assert-ci --json-out "$TMP/results.json"
python3 - "$TMP/results.json" <<'PY'
import json,sys
x=json.load(open(sys.argv[1]))
assert x['short']['batch']['peak_concurrency'] >= 2
assert x['resume']['batch']['resume_independent'] is True
print('workflow benchmark smoke passed')
PY
