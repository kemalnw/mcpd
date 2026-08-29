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
> search, OAuth 2.1/CIMD, HTTPS/TLS, systemd lifecycle management, and signed
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
+---------------------------+
|           mcpd            |
| TLS + OAuth + MCP server  |
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

For local development, `auth.enabled = false` and `tls.mode = "off"` keep the
endpoint on plain HTTP. Public deployments should enable OAuth and HTTPS together.

## Install from a release

Install the latest Linux `amd64`/`arm64` binary from the signed GitHub Release:

```bash
curl -fsSL https://github.com/kemalnw/mcpd/releases/latest/download/install.sh | sh
```

The installer verifies SHA-256 before extraction. If `cosign` is installed it also
verifies the Sigstore identity of the release checksum manifest; set
`MCPD_REQUIRE_SIGNATURE=1` to require that verification. Then install the systemd
service with `sudo mcpd install`. See [`docs/releases.md`](docs/releases.md) for
artifact names, manual verification, provenance, and the maintainer release flow.

## OAuth and HTTPS

`mcpd` contains both the OAuth authorization server and MCP resource server. It
implements Authorization Code + PKCE S256 and prefers Client ID Metadata
Documents (CIMD), the registration model used by MCP `2026-07-28`.

Create the owner credential without putting it in shell arguments:

```bash
mcpd auth set-password
# non-interactive automation:
printf '%s\n' "$MCPD_OWNER_PASSWORD" | mcpd auth set-password --password-stdin
```

A public-IP deployment uses a canonical origin and an MCP resource beneath it:

```text
OAuth issuer: https://203.0.113.10
MCP resource: https://203.0.113.10/mcp
```

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

TLS modes:

- `off`: local development only.
- `files`: load an existing certificate/key pair and hot-serve it through Go TLS.
- `acme`: obtain and renew certificates with ACME HTTP-01. The default
  `shortlived` profile supports Let's Encrypt public IPv4/IPv6 certificates.

ACME mode never accepts CA terms implicitly: set `acme_accept_tos = true`
explicitly. HTTP-01 must be reachable on the configured challenge listener
(default `:80`). The HTTPS listener is configured separately, normally `:443`.


## Install and service lifecycle

On a systemd-based Linux VM, build or download `mcpd`, then install it once:

```bash
sudo ./mcpd install
```

The installer copies the current binary to `/usr/local/bin/mcpd`, writes a
validated system config to `/etc/mcpd/config.toml` when one does not already
exist, stores durable state beneath `/var/lib/mcpd`, and installs systemd units.
Existing config is preserved by default. Use `--force-config` only when replacing
it intentionally.

When invoked through `sudo`, the daemon user defaults to `SUDO_USER`, so a normal
install does not silently turn AI access into root access. Full-OS mode remains
explicit:

```bash
sudo mcpd install --user root
```

Lifecycle commands use systemd and journald directly:

```bash
sudo mcpd start
sudo mcpd restart
mcpd status
mcpd logs --lines 100
mcpd logs --follow
mcpd doctor
sudo mcpd stop
```

If an unprivileged service user needs port 80 or 443, the installer creates
`mcpd.socket`. systemd owns the privileged listener and passes it to `mcpd` using
socket activation. The service is deliberately not granted `CAP_NET_BIND_SERVICE`,
so commands spawned by MCP do not inherit extra networking privilege.

If OAuth is enabled but no owner password exists yet, installation succeeds but
service start is deferred. Configure the credential first, then start the daemon:

```bash
sudo mcpd auth set-password --config /etc/mcpd/config.toml
sudo mcpd start
```

`mcpd doctor` validates systemd availability, installed binary/config/unit files,
service user, systemd unit syntax, state permissions, socket-activation needs,
OAuth password state, TLS file prerequisites, and current service activity.

## Configuration

`mcpd` reads TOML from `$MCPD_CONFIG` when set, otherwise from the platform user
config directory (normally `~/.config/mcpd/config.toml` on Linux).

See [`configs/mcpd.example.toml`](configs/mcpd.example.toml).

## Roadmap

1. ✅ Stateless MCP server and process/PTY core.
2. ✅ Native text/filesystem facade, URL reads, metadata, directory operations, and text `edit_block`.
3. ✅ Progressive search with explicit search handles, ripgrep acceleration, and native fallback.
4. ⏸️ Image, Excel, DOCX, and PDF handlers — deferred until after the MVP.
5. ✅ OAuth 2.1/CIMD, scoped tool authorization, and TLS/ACME for public-IP endpoints.
6. ✅ `mcpd install/start/stop/restart/status/logs/doctor` with systemd and privileged-port socket activation.
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
