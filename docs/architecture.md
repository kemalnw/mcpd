# Architecture

## Scope

`mcpd` is a self-hosted MCP daemon for Linux VMs. The daemon exposes direct OS
capabilities to remote AI clients and executes those capabilities using the
permissions of the user running the daemon.

The project deliberately separates three kinds of state:

1. **MCP transport state:** none. HTTP uses stateless MCP `2026-07-28`.
2. **Runtime resource state:** explicit process/search handles such as PIDs and
   search IDs.
3. **Durable daemon state:** configuration, OAuth identity, TLS material, and
   audit history.

This keeps protocol requests independent while still supporting REPLs,
long-running commands, and progressive search.

## Package boundaries

```text
cmd/mcpd
  CLI entry point only

internal/app
  dependency wiring, HTTP lifecycle, MCP transport

internal/tools
  MCP contracts and adapters; no OS-specific process logic

internal/process
  process lifecycle, PTY, signals, output buffering/pagination

internal/files          (planned)
  file facade and format-specific handlers

internal/search         (planned)
  ripgrep + Office document search sessions

internal/documents      (planned)
  Excel, DOCX, PDF capability adapters

internal/auth           (planned)
  OAuth 2.1 authorization/resource-server responsibilities

internal/tls            (planned)
  public-IP ACME certificate lifecycle

internal/daemon         (planned)
  systemd install and service lifecycle

internal/audit
  normalized tool-call events + persistent JSONL

internal/config
  defaults, TOML loading, validation
```

The tool layer depends on domain interfaces. Domain packages must not depend on
MCP request/response types. This prevents protocol concerns from leaking into
OS logic and keeps core behavior independently testable.

## Process model

`start_process` creates an OS process immediately and registers a managed
session keyed by its real PID. `timeout_ms` is a response wait timeout, not a
process lifetime timeout.

```text
start_process
     |
     +--> spawn shell process
     +--> register PID/session
     +--> capture stdout/stderr or PTY stream
     +--> wait until:
           - process exits, or
           - prompt is detected, or
           - timeout_ms elapses
     |
     +--> return PID + current structured state
```

Long-running processes remain alive after the tool call returns. Subsequent MCP
requests operate on the PID explicitly.

### PTY

Desktop Commander compatibility does not require copying its pipe-based
terminal implementation. `mcpd` uses a real pseudo-terminal for interactive
workloads.

`start_process` adds an optional `pty` extension:

- `auto` — default; detect common interactive commands.
- `always` — force PTY.
- `never` — use stdin/stdout/stderr pipes.

Requests that omit `pty` remain compatible with the Desktop Commander input
shape.

### Output retention

Each process has a bounded in-memory output buffer. Current defaults:

- 50 MiB per process.
- 1 MiB maximum individual line.
- 100 completed process sessions retained.

Oldest lines are evicted when the buffer exceeds its limit. Absolute line
numbers remain monotonic through eviction.

`read_process_output` preserves three offset modes:

- `offset = 0`: read from the process cursor and advance it.
- `offset > 0`: absolute read without changing the cursor.
- `offset < 0`: tail-relative read without changing the cursor.

## Structured tool outputs

Every `mcpd` tool uses the typed MCP Go SDK API. Go input/output types generate
JSON Schema 2020-12 contracts and tool results expose structured content.

Human-readable text remains available through the SDK fallback, but agents are
not required to parse prose to recover PIDs, states, line counts, or exit codes.

## Audit model

Every tool invocation passes through a generic audit wrapper. The normalized
event contains:

```json
{
  "id": "evt_...",
  "timestamp": "...",
  "tool": "start_process",
  "arguments": {},
  "duration_ms": 12,
  "status": "success",
  "error": ""
}
```

The store maintains a small in-memory recent ring for MCP diagnostics and an
append-only JSONL stream for durable CLI log access.

## Protocol baseline

The first implementation pins the official
`github.com/modelcontextprotocol/go-sdk` release that introduced complete MCP
`2026-07-28` support. Streamable HTTP is configured with `Stateless: true`.

Legacy MCP sessions are not part of the target architecture.
