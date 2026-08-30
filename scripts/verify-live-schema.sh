#!/bin/sh
set -eu

if [ "$#" -lt 1 ]; then
  echo "usage: $0 <public-origin> [tool] [input-field]" >&2
  echo "example: $0 https://mcp.example.com start_search pathHint" >&2
  exit 2
fi

ORIGIN=${1%/}
TOOL=${2:-${MCPD_EXPECT_TOOL:-start_search}}
FIELD=${3:-${MCPD_EXPECT_FIELD:-pathHint}}
MCP_PATH=${MCPD_MCP_PATH:-/mcp}
EXPECT_VERSION=${MCPD_EXPECT_VERSION:-}
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT HUP INT TERM

HEALTH_URL="$ORIGIN/healthz"
MCP_URL="$ORIGIN$MCP_PATH"

curl -fsS "$HEALTH_URL" >"$TMP/health.json"
curl -fsS \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -X POST \
  --data '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}' \
  "$MCP_URL" >"$TMP/tools.json"

python3 - "$TMP/health.json" "$TMP/tools.json" "$TOOL" "$FIELD" "$EXPECT_VERSION" <<'PY'
import json, sys
health_path, tools_path, tool_name, field_name, expected_version = sys.argv[1:]
with open(health_path, encoding="utf-8") as f:
    health = json.load(f)
with open(tools_path, encoding="utf-8") as f:
    envelope = json.load(f)
version = health.get("version", "")
if health.get("status") != "ok":
    raise SystemExit(f"health check is not ok: {health!r}")
if expected_version and version != expected_version:
    raise SystemExit(f"server version mismatch: got {version!r}, want {expected_version!r}")
tools = envelope.get("result", {}).get("tools", [])
tool = next((item for item in tools if item.get("name") == tool_name), None)
if tool is None:
    raise SystemExit(f"tool {tool_name!r} missing from fresh tools/list")
props = tool.get("inputSchema", {}).get("properties", {})
if field_name not in props:
    raise SystemExit(f"field {field_name!r} missing from {tool_name!r} input schema; server version={version!r}")
print(f"server_version={version}")
print(f"schema_ok tool={tool_name} field={field_name}")
PY

echo "fresh live tools/list verified; reconnect clients separately if they still show an older cached schema"
