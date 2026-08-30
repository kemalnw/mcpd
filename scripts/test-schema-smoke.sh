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
curl -fsS "http://127.0.0.1:$PORT/healthz" >/dev/null

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
