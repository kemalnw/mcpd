#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
TMP=$(mktemp -d)
SERVER_PID=""
cleanup() {
  if [ -n "$SERVER_PID" ]; then kill "$SERVER_PID" 2>/dev/null || true; fi
  rm -rf "$TMP"
}
trap cleanup EXIT HUP INT TERM

PORT=$((20000 + $$ % 1000))
cat >"$TMP/config.toml" <<EOF
[server]
listen = "127.0.0.1:$PORT"
mcp_path = "/mcp"
shutdown_seconds = 2

[audit]
enabled = false

[auth]
enabled = false
EOF

CGO_ENABLED=0 go build -o "$TMP/mcpd" "$ROOT/cmd/mcpd"
MCPD_CONFIG="$TMP/config.toml" "$TMP/mcpd" serve >"$TMP/server.log" 2>&1 &
SERVER_PID=$!
for _ in 1 2 3 4 5 6 7 8 9 10; do
  if curl -fsS "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1; then break; fi
  sleep 0.1
done
curl -fsS "http://127.0.0.1:$PORT/healthz" >"$TMP/health.json"
curl -fsS \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -X POST \
  --data '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}' \
  "http://127.0.0.1:$PORT/mcp" >"$TMP/tools.json"
if [ "${MCPD_UPDATE_CATALOG:-0}" = 1 ]; then
  "$ROOT/scripts/catalog-contract.py" "$TMP/health.json" "$TMP/tools.json" "$ROOT/internal/tools/testdata/catalog-contract.json" --write
else
  "$ROOT/scripts/catalog-contract.py" "$TMP/health.json" "$TMP/tools.json" "$ROOT/internal/tools/testdata/catalog-contract.json"
  python3 - "$TMP/tools.json" "$TMP/tools-mutated.json" <<'PY_CONTRACT'
import json, sys
src, dst = sys.argv[1:]
with open(src, encoding="utf-8") as f:
    envelope = json.load(f)
tools = envelope["result"]["tools"]
tools[0]["description"] = tools[0].get("description", "") + " intentional-test-mutation"
with open(dst, "w", encoding="utf-8") as f:
    json.dump(envelope, f)
PY_CONTRACT
  if "$ROOT/scripts/catalog-contract.py" "$TMP/health.json" "$TMP/tools-mutated.json" "$ROOT/internal/tools/testdata/catalog-contract.json" >/dev/null 2>&1; then
    echo "catalog contract verifier accepted an unversioned model-facing schema change" >&2
    exit 1
  fi
  python3 - "$TMP/health.json" "$TMP/health-mutated.json" <<'PY_CONTRACT'
import json, sys
src, dst = sys.argv[1:]
with open(src, encoding="utf-8") as f:
    health = json.load(f)
health["tool_catalog_version"] += 1
with open(dst, "w", encoding="utf-8") as f:
    json.dump(health, f)
PY_CONTRACT
  if "$ROOT/scripts/catalog-contract.py" "$TMP/health-mutated.json" "$TMP/tools.json" "$ROOT/internal/tools/testdata/catalog-contract.json" >/dev/null 2>&1; then
    echo "catalog contract verifier accepted a version bump without refreshed committed contract" >&2
    exit 1
  fi
fi

"$ROOT/scripts/verify-live-schema.sh" "http://127.0.0.1:$PORT" start_search pathHint >"$TMP/schema.out"
catalog_version=$(sed -n 's/^tool_catalog_version=//p' "$TMP/schema.out")
catalog_fingerprint=$(sed -n 's/^tool_catalog_fingerprint=//p' "$TMP/schema.out")
MCPD_EXPECT_CATALOG_VERSION="$catalog_version" MCPD_EXPECT_CATALOG_FINGERPRINT="$catalog_fingerprint" \
  "$ROOT/scripts/verify-live-schema.sh" "http://127.0.0.1:$PORT" start_search pathHint >/dev/null
if MCPD_EXPECT_CATALOG_VERSION=999999 "$ROOT/scripts/verify-live-schema.sh" "http://127.0.0.1:$PORT" start_search pathHint >/dev/null 2>&1; then
  echo "schema verifier accepted the wrong catalog version" >&2
  exit 1
fi
if "$ROOT/scripts/verify-live-schema.sh" "http://127.0.0.1:$PORT" start_search definitely_missing_field >/dev/null 2>&1; then
  echo "schema verifier accepted a missing field" >&2
  exit 1
fi

echo "live schema smoke test passed"
