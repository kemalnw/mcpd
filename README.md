# mcpd

[![CI](https://github.com/kemalnw/mcpd/actions/workflows/ci.yml/badge.svg)](https://github.com/kemalnw/mcpd/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

**Turn a Linux VM into a remote MCP server for AI agents.**

`mcpd` is a lightweight, self-hosted Model Context Protocol daemon written in
Go. It is designed to expose terminal, process, filesystem, search, and
document capabilities directly from a VM without a third-party relay.

The execution model is intentionally simple: tools run with the permissions of
the Unix user running `mcpd`. Run it as an ordinary user for that user's access,
or run it as `root` when full operating-system access is the intended behavior.

> **Status:** early development. The process/terminal core and native text/filesystem
> facade are implemented. Search, structured document handlers, OAuth,
> TLS/public-IP setup, daemon installation, and CLI lifecycle commands remain on
> the roadmap and are not production-ready yet.

## Design goals

- **Stateless MCP:** native MCP `2026-07-28` over stateless Streamable HTTP.
- **Direct:** AI client → `mcpd` → VM. No cloud relay in the execution path.
- **Lightweight:** one Go binary for the core daemon; optional heavy document
  capabilities remain optional.
- **Capability-first:** target Desktop Commander-class terminal, process,
  filesystem, search, and document tooling.
- **Unix-native:** systemd service and standard Linux process semantics.
- **Observable:** structured tool results plus persistent JSONL audit events.
- **Explicit privilege:** no hidden sandbox; daemon-user permissions are the
  tool permission model.

## Current architecture

```text
MCP client
    |
    | Streamable HTTP / MCP 2026-07-28
    v
+---------------------------+
|           mcpd            |
|  MCP server + tool layer  |
+-------------+-------------+
              |
     +--------+-----------+--------+
     |                    |        |
     v                    v        v
Process manager     File manager  Audit store
     |                    |        |
     |                    |        +--> JSONL
     |                    +--> text/URL reads
     |                    +--> write/edit/move
     |                    +--> directory metadata
     |
     +--> shell commands
     +--> real PTY sessions
     +--> process groups/signals
     +--> bounded output buffers
```

Protocol transport is stateless. Long-running OS resources are represented by
explicit handles such as PIDs; they are application state, not MCP transport
sessions.

## Implemented process tools

| Tool | Status | Notes |
| --- | --- | --- |
| `start_process` | ✅ | wait timeout does not kill long-running processes; PTY `auto/always/never` extension |
| `read_process_output` | ✅ | cursor, absolute-offset, and tail pagination |
| `interact_with_process` | ✅ | interactive stdin with prompt-aware waiting |
| `force_terminate` | ✅ | SIGINT followed by SIGKILL escalation |
| `list_sessions` | ✅ | active and retained completed sessions |
| `list_processes` | ✅ | Linux process inventory via `ps` |
| `kill_process` | ✅ | terminate arbitrary visible OS process by PID |

## Implemented filesystem tools

| Tool | Status | Notes |
| --- | --- | --- |
| `read_file` | ✅ text + URL | streaming line pagination, negative tail offsets; structured formats reserved |
| `read_multiple_files` | ✅ text | per-file errors do not abort the batch |
| `write_file` | ✅ text | rewrite/append modes |
| `create_directory` | ✅ | recursive parent creation |
| `list_directory` | ✅ | recursive depth with nested-entry context protection |
| `move_file` | ✅ | native rename/move semantics |
| `get_file_info` | ✅ | Linux statx timestamps, permissions, type and text line metadata |
| `edit_block` | ✅ text | exact occurrence checks, 70% fuzzy suggestion, symlink/hard-link-aware writes |

Image, Excel, PDF, and DOCX support will plug into the same stable file facade.

The target tool catalog is documented in [`docs/tool-contracts.md`](docs/tool-contracts.md).

## Development

Requirements:

- Linux
- Go version declared by `go.mod`

```bash
git clone https://github.com/kemalnw/mcpd.git
cd mcpd
make check
make build
./bin/mcpd serve --listen 127.0.0.1:8787
```

Health check:

```bash
curl http://127.0.0.1:8787/healthz
```

Default MCP endpoint:

```text
http://127.0.0.1:8787/mcp
```

The development endpoint is intentionally unauthenticated for now. Public
remote deployment will be enabled only together with the OAuth/TLS milestone.

## Configuration

`mcpd` reads TOML from `$MCPD_CONFIG` when set, otherwise from the platform user
config directory (normally `~/.config/mcpd/config.toml` on Linux).

See [`configs/mcpd.example.toml`](configs/mcpd.example.toml).

## Roadmap

1. ✅ Stateless MCP server and process/PTY core.
2. ✅ Native text/filesystem facade, URL reads, metadata, directory operations, and text `edit_block`.
3. ⏳ Progressive search with explicit search handles and ripgrep integration.
4. ⏳ Image, Excel, DOCX, and PDF handlers behind the existing file facade.
5. ⏳ OAuth 2.1/CIMD and automatic TLS for public-IP endpoints.
6. ⏳ `mcpd install/start/stop/restart/status/logs/doctor` with systemd.
7. ⏳ Release automation, signed artifacts, checksums, and install script.

See [`docs/architecture.md`](docs/architecture.md) for the package boundaries
and engineering decisions behind the project.

## Inspiration and compatibility

The capability target is heavily inspired by
[Desktop Commander MCP](https://github.com/wonderwhy-er/DesktopCommanderMCP).
`mcpd` is an independent Go implementation rather than a fork. Familiar tool
names and behavior are retained where they improve model compatibility, while
implementation details are redesigned for a Linux VM and the latest stateless
MCP architecture.

## License

MIT © Abdul Kemal.
