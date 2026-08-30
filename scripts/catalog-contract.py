#!/usr/bin/env python3
import argparse
import hashlib
import json
from pathlib import Path


def load(path):
    with open(path, encoding="utf-8") as f:
        return json.load(f)


def semantic_tools(envelope):
    tools = envelope.get("result", {}).get("tools")
    if not isinstance(tools, list) or not tools:
        raise SystemExit("tools/list response has no tools")
    # Server enumeration order is not a model-facing contract. Everything inside
    # each tool object is: name/title/descriptions/schemas/annotations/security.
    return sorted(tools, key=lambda item: item.get("name", ""))


def schema_hash(envelope):
    payload = json.dumps(semantic_tools(envelope), sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode()
    return "sha256:" + hashlib.sha256(payload).hexdigest()


def main():
    parser = argparse.ArgumentParser(description="verify MCPD model-facing tools/list against a versioned committed contract")
    parser.add_argument("health")
    parser.add_argument("tools")
    parser.add_argument("baseline")
    parser.add_argument("--write", action="store_true")
    args = parser.parse_args()

    health = load(args.health)
    envelope = load(args.tools)
    version = health.get("tool_catalog_version")
    if not isinstance(version, int) or version <= 0:
        raise SystemExit("health response has no valid tool_catalog_version")
    fingerprint = schema_hash(envelope)
    contract = {
        "catalog_version": version,
        "tools_list_sha256": fingerprint,
        "tool_count": len(semantic_tools(envelope)),
    }
    baseline_path = Path(args.baseline)
    if args.write:
        baseline_path.parent.mkdir(parents=True, exist_ok=True)
        baseline_path.write_text(json.dumps(contract, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        print(f"updated catalog contract: version={version} hash={fingerprint}")
        return

    expected = load(baseline_path)
    if expected != contract:
        raise SystemExit(
            "model-facing MCP tool contract changed without matching committed catalog contract\n"
            f"expected={json.dumps(expected, sort_keys=True)}\n"
            f"actual={json.dumps(contract, sort_keys=True)}\n"
            "If the schema/description/annotations change is intentional: increment internal/tools.CatalogVersion, "
            "then run MCPD_UPDATE_CATALOG=1 ./scripts/test-schema-smoke.sh and review the contract diff."
        )
    print(f"catalog_contract_ok version={version} hash={fingerprint} tools={contract['tool_count']}")


if __name__ == "__main__":
    main()
