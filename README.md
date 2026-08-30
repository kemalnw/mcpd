# mcpd

[![CI](https://github.com/kemalnw/mcpd/actions/workflows/ci.yml/badge.svg)](https://github.com/kemalnw/mcpd/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

**A self-hosted MCP server for giving AI clients direct access to a Linux machine.**

`mcpd` is a lightweight Go daemon that exposes terminal, process, filesystem,
search, and durable execution capabilities over Model Context Protocol (MCP).
It is designed for Linux VMs you control and does not require a third-party relay
in the execution path.

> [!IMPORTANT]
> `mcpd` is not a sandbox. Tools run with the permissions of the Unix user running
> the daemon. Use a dedicated, least-privileged service user unless full host access
> is intentional.

## Features

- Process execution with PTY support, interactive stdin, output pagination, and safe termination.
- Parallel command batches with bounded, resource-aware concurrency.
- Native file read/write/edit, directory operations, metadata, and URL text reads.
- Progressive filename/content search with ripgrep acceleration and a native fallback.
- Durable jobs and resumable engineering workflows for long-running agent work.
- OAuth 2.1 authorization for remote MCP clients, including PKCE and refresh tokens.
- systemd lifecycle commands, structured operational logs, and a durable JSONL audit trail.
- Single Go binary; Caddy can be managed by the setup wizard for public HTTPS.

## Install

Install the latest Linux `amd64` or `arm64` release:

```bash
curl -fsSL https://github.com/kemalnw/mcpd/releases/latest/download/install.sh | sh
```

The installer verifies the release checksum before installation. If `cosign` is
available it also verifies the Sigstore bundle; set `MCPD_REQUIRE_SIGNATURE=1` to
require signature verification.

With an interactive terminal, the installer starts `sudo mcpd setup`. In a
non-interactive environment it installs the binary and prints the setup command
instead. Set `MCPD_SETUP=0` to install only the binary.

## Setup

For a normal remote deployment:

```bash
sudo mcpd setup
```

The wizard configures the service, OAuth owner credential, and HTTPS frontend.
Managed Caddy is the default. If another reverse proxy or tunnel already owns
public HTTPS, choose that option in the wizard or use `--https-ready` for
non-interactive setup.

The normal request path is:

```text
MCP client
  -> HTTPS https://mcp.example.com/mcp
  -> Caddy / existing HTTPS frontend
  -> HTTP 127.0.0.1:31354
  -> mcpd
```

`mcpd` itself remains an HTTP origin server; public TLS terminates in the frontend.

See [docs/setup.md](docs/setup.md) for DNS, Caddy, automation, and client connection details.

## Tools

MCPD exposes tools in five groups:

| Area | Examples |
| --- | --- |
| Process | `start_process`, `start_process_batch`, `read_process_output`, `interact_with_process`, `force_terminate` |
| Filesystem | `read_file`, `read_multiple_files`, `write_file`, `edit_block`, `list_directory`, `move_file` |
| Search | `start_search`, `get_more_search_results`, `list_searches`, `stop_search` |
| Durable execution | `start_durable_job`, `get_durable_job`, `read_durable_job_log`, `cancel_durable_job` |
| Workflow | `create_run`, `checkpoint_run`, `handoff_run`, `resume_run`, `collect_workflow_garbage` |

The MCP `tools/list` response is the source of truth for the current tool schemas.
See [docs/tools.md](docs/tools.md) for usage guidance and retry-safety rules.

## Security model

Remote deployments should enable OAuth and expose `mcpd` only through HTTPS.
Read-only tools require `mcp:read`; mutating and process-control tools require
`mcp:write`.

The daemon deliberately uses normal Unix permissions instead of hiding access
behind an application sandbox. Running it as `root` therefore gives connected
clients root-level operating-system capabilities.

See [SECURITY.md](SECURITY.md) for vulnerability reporting and security guidance.

## Configuration

`mcpd setup` writes the system service configuration to `/etc/mcpd/config.toml`.
For user/local execution, `mcpd` reads `$MCPD_CONFIG` when set, otherwise the
platform user config path.

A complete example is available at [configs/mcpd.example.toml](configs/mcpd.example.toml).

## Operations

Common lifecycle commands:

```bash
sudo mcpd start
sudo mcpd restart
mcpd status
mcpd logs --lines 100
mcpd logs --follow
mcpd doctor
sudo mcpd stop
```

See [docs/operations.md](docs/operations.md) for logs, audit data, durable state,
and operational behavior.

## Documentation

- [Setup](docs/setup.md) — installation, HTTPS, OAuth, and client connection.
- [Tools](docs/tools.md) — tool groups, execution modes, and retry safety.
- [Operations](docs/operations.md) — service lifecycle, logs, audit, and durable state.
- [Architecture](docs/architecture.md) — protocol, package boundaries, and security design.
- [Releases](docs/releases.md) — artifact verification and maintainer release flow.

## Development

Requirements: Linux and the Go version declared in `go.mod`.

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

## License

MIT © Abdul Kemal.
