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

> **Status:** early development. Process/terminal, native text/filesystem, progressive
> search, OAuth 2.1/CIMD, HTTP-origin deployment, systemd lifecycle management, and signed
> release automation are implemented. Structured document handlers remain
> deferred until after the MVP.

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
    | HTTPS + OAuth 2.1 / MCP 2026-07-28
    v
+-----------------------------+
| user-managed HTTPS frontend |
+--------------+--------------+
               | HTTP
               v
+---------------------------+
|           mcpd            |
| OAuth + MCP HTTP origin   |
+-------------+-------------+
              |
     +--------+-----------+-----------+--------+
     |                    |           |        |
     v                    v           v        v
Process manager     File manager  Search manager  Audit store
     |                    |           |        |
     |                    |           |        +--> JSONL
     |                    |           +--> ripgrep when available
     |                    |           +--> native Go fallback
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
| `start_process` | ✅ | wait timeout does not kill long-running processes; PTY `auto/always/never`; optional stdout/stderr separation |
| `start_process_batch` | ✅ | schedule 2+ independent non-interactive jobs with bounded parallelism |
| `read_process_batch` | ✅ | wait-for-any-change and changed-only output deltas |
| `cancel_process_batch` | ✅ | cancel queued/running batch jobs safely |
| `read_process_output` | ✅ | cursor/absolute/tail pagination plus same-line output generations |
| `interact_with_process` | ✅ | interactive stdin with prompt-aware waiting; optional raw input |
| `resize_process_pty` | ✅ | resize running PTY rows/columns |
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

## Implemented search tools

| Tool | Status | Notes |
| --- | --- | --- |
| `start_search` | ✅ | progressive files/content search; ripgrep preferred with native fallback |
| `get_more_search_results` | ✅ | absolute-offset and negative-tail pagination |
| `stop_search` | ✅ | cancellation preserves results already found |
| `list_searches` | ✅ | running and retained completed searches |

Search sessions are explicit application resources, not MCP transport sessions.
Completed sessions remain readable for five minutes after their last read by
default. `maxResults` is a global match cap. File filters are intersected with
the main filename pattern.

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
./bin/mcpd serve --listen 127.0.0.1:31354
```

Health check:

```bash
curl http://127.0.0.1:31354/healthz
```

Default MCP endpoint:

```text
http://127.0.0.1:31354/mcp
```

For local development, `auth.enabled = false` keeps the origin on plain HTTP.
For remote production use, keep `mcpd` behind a user-managed HTTPS terminator and enable OAuth.

## Install from a release

Install the latest Linux `amd64`/`arm64` binary from the signed GitHub Release:

```bash
curl -fsSL https://github.com/kemalnw/mcpd/releases/latest/download/install.sh | sh
```

The installer verifies SHA-256 before extraction. If `cosign` is installed it also
verifies the Sigstore identity of the release checksum manifest; set
`MCPD_REQUIRE_SIGNATURE=1` to require that verification.

When an interactive terminal is available, the installer automatically launches
`sudo mcpd setup`. The wizard asks for the public domain, service user, OAuth owner
password, and HTTPS frontend. Managed **Caddy** is the recommended/default frontend;
an existing reverse proxy or tunnel remains supported. The normal backend and MCP
path default to `127.0.0.1:31354` and `/mcp`.

If no terminal is available, the installer never waits for prompts: it installs the
binary and prints `sudo mcpd setup` as the next step. Set `MCPD_SETUP=0` to suppress
automatic setup explicitly. See [`docs/releases.md`](docs/releases.md) for artifact
verification, provenance, and the maintainer release flow.

## OAuth and public HTTPS

`mcpd` contains both the OAuth authorization server and MCP resource server. It
implements Authorization Code + PKCE S256 and prefers Client ID Metadata
Documents (CIMD), the registration model used by MCP `2026-07-28`.

Create the owner credential without putting it in shell arguments:

```bash
mcpd auth set-password
# non-interactive automation:
printf '%s\n' "$MCPD_OWNER_PASSWORD" | mcpd auth set-password --password-stdin
```

A remote deployment uses a canonical domain origin and an MCP resource beneath it:

```text
OAuth issuer: https://mcp.example.com
MCP resource: https://mcp.example.com/mcp
```

`mcpd` itself serves plain HTTP only. `auth.external_url` describes the public HTTPS
origin seen by MCP clients; it is intentionally independent from the local HTTP listener.

Relevant discovery endpoints are:

```text
/.well-known/oauth-protected-resource
/.well-known/oauth-protected-resource/mcp
/.well-known/oauth-authorization-server
/oauth/authorize
/oauth/token
/oauth/jwks.json
```

Tools advertise `securitySchemes`, and an unauthenticated `tools/call` returns
`_meta["mcp/www_authenticate"]` so clients such as ChatGPT can start account
linking. Read-only tools require `mcp:read`; mutating/process-control tools
require `mcp:write`.

### HTTPS termination and DNS

`mcpd` remains an HTTP-only origin server. The default listener is
`127.0.0.1:31354`; `mcpd setup` manages **Caddy** as the standard public HTTPS/TLS
frontend:

```text
ChatGPT
  -> HTTPS mcp.example.com:443
  -> Caddy (automatic certificate + renewal)
  -> HTTP 127.0.0.1:31354
  -> mcpd
```

For managed Caddy, the public domain must resolve to the VM and public TCP `80` and
`443` must be reachable. Setup installs Caddy through a supported OS package
manager, keeps `mcpd` loopback-only, manages `/etc/caddy/mcpd.caddy`, validates the
complete Caddy configuration, and reloads or starts `caddy.service`. Existing
unrelated Caddy sites are preserved.

Cloudflare **DNS only** is compatible with this default: the A/AAAA record points
directly at the VM and Caddy terminates TLS there. DNS alone does not terminate TLS
or translate ports.

If nginx, HAProxy, Caddy, Cloudflare Tunnel, or another HTTPS frontend already owns
public HTTPS, choose **Existing reverse proxy / tunnel** (or pass `--https-ready`).
In that mode `mcpd` does not install or mutate Caddy. Cloudflare **Proxied**/Tunnel
is treated as this user-managed frontend path.


## Setup and service lifecycle

The primary first-run command is:

```bash
sudo mcpd setup
```

See [`docs/setup.md`](docs/setup.md) for interactive setup, automation, reruns,
Cloudflare/DNS examples, and the ChatGPT connection flow.

`mcpd setup` is the human-facing wizard. It validates all answers before changing
the system, writes `/etc/mcpd/config.toml`, installs/enables `mcpd.service`,
configures the OAuth owner credential, starts the backend, provisions the selected HTTPS frontend,
verifies public HTTPS/OAuth, and runs `mcpd doctor`. Managed Caddy owns certificate
issuance/renewal while `mcpd` stays HTTP-only.

Owner passwords have a minimum of 8 characters; a longer passphrase is recommended.
The password is read without echo and is never passed in process arguments.

OAuth access tokens remain short-lived (1 hour by default). Clients that request
`offline_access` receive rotating refresh tokens with a 30-day sliding idle timeout,
so active ChatGPT connections can renew authorization without repeated owner login.
After upgrading an existing ChatGPT app to a release with refresh-token support,
disconnect/reconnect it once so ChatGPT fetches the new OAuth metadata and obtains
its first refresh token.

Rerunning `sudo mcpd setup` preserves an existing configuration by default and
offers an explicit reconfigure choice. Compatible OAuth signing/password state is
preserved unless the user explicitly replaces it.

For automation, use explicit setup flags and password stdin:

```bash
printf '%s\n' "$MCPD_OWNER_PASSWORD" | sudo mcpd setup \
  --domain mcp.example.com --yes --password-stdin
```

Pass `--https-ready` when an existing reverse proxy or tunnel already owns public
HTTPS and managed Caddy should not be installed or changed.

`mcpd install` remains the deterministic lower-level primitive for scripts/tests
that already provide a config. It is intentionally non-interactive:

```bash
sudo mcpd install --config /path/to/config.toml
```

Lifecycle commands are thin systemd/journald wrappers:

```bash
sudo mcpd start
sudo mcpd restart
mcpd status
mcpd logs --lines 100
mcpd logs --follow
mcpd doctor
sudo mcpd stop
```

For v0.1.x upgrades, see [`docs/migration-v0.2.md`](docs/migration-v0.2.md).

## Configuration

`mcpd` reads TOML from `$MCPD_CONFIG` when set, otherwise from the platform user
config directory (normally `~/.config/mcpd/config.toml` on Linux).

See [`configs/mcpd.example.toml`](configs/mcpd.example.toml).

## Roadmap

1. ✅ Stateless MCP server and process/PTY core.
2. ✅ Native text/filesystem facade, URL reads, metadata, directory operations, and text `edit_block`.
3. ✅ Progressive search with explicit search handles, ripgrep acceleration, and native fallback.
4. ⏸️ Image, Excel, DOCX, and PDF handlers — deferred until after the MVP.
5. ✅ OAuth 2.1/CIMD and scoped tool authorization for domain-based public endpoints.
6. ✅ `mcpd install/start/stop/restart/status/logs/doctor` with a minimal single systemd service.
7. ✅ Reproducible release automation, Sigstore-signed artifacts, provenance, checksums, and install script.

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
