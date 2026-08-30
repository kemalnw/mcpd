# Architecture

MCPD is a self-hosted MCP server for Linux machines. It keeps the protocol layer
small and delegates operating-system behavior to focused internal packages.

## Request path

A normal remote deployment looks like:

```text
MCP client
    |
    | HTTPS + OAuth
    v
HTTPS frontend (Caddy, reverse proxy, or tunnel)
    |
    | HTTP
    v
mcpd
    |
    +-- process / PTY
    +-- filesystem
    +-- search
    +-- durable execution supervisor
    +-- workflow state
    +-- audit log
```

MCPD itself is an HTTP origin server. TLS is intentionally terminated outside the
daemon so certificate lifecycle and public ingress remain standard infrastructure
concerns.

## State model

MCP transport requests are stateless. Long-lived work is represented by explicit
application handles instead of transport sessions:

- PIDs and batch IDs for normal process execution;
- search IDs for progressive searches;
- durable job IDs for restart-surviving commands;
- run IDs for persistent workflow/checkpoint state.

This separation lets clients reconnect without pretending an HTTP/MCP connection is
the lifetime of the underlying Linux resource.

## Package boundaries

```text
cmd/mcpd           CLI entry point
internal/app       dependency wiring, HTTP/MCP lifecycle
internal/tools     MCP schemas and adapters
internal/process   process groups, PTY, output retention, batches
internal/filesystem native filesystem and text/URL operations
internal/search    progressive search sessions and backends
internal/durableexec restart-surviving command supervisor/state
internal/workflow  durable runs, checkpoints, handoffs, retention
internal/oauth     OAuth authorization/resource server
internal/service   systemd installation and lifecycle operations
internal/audit     structured durable tool-call audit events
internal/config    defaults, TOML loading, validation
```

Protocol-specific types stay near `internal/tools` and `internal/app`. Domain
packages own operating-system and persistence behavior so core logic remains
testable without an MCP transport.

## Execution model

Normal process tools are optimized for live interaction. `start_process` manages a
process group, optional PTY, bounded output retention, and a handle that can be
continued with later calls. Batch execution adds bounded concurrency for multiple
independent non-interactive jobs.

Durable jobs use a separate supervisor service. That service owns the durable
runner lifecycle so restarting `mcpd.service` does not terminate an already-running
durable command. State and logs are persisted on disk and reconciled when the main
daemon reconnects.

Workflow state is separate again: it stores objectives, revisions, checkpoints,
handoffs, and references to execution evidence. It is deliberately not a copy of
chat history or full command logs.

## Authorization model

For remote use, MCPD combines an OAuth authorization server with the MCP protected
resource. Authorization Code + PKCE is used for interactive authorization, and
refresh tokens are available when the client requests offline access.

Read-only tools require `mcp:read`; filesystem mutation and process-control tools
require `mcp:write`.

The public OAuth issuer is the canonical HTTPS origin while the MCP resource is the
MCP endpoint beneath it, for example:

```text
issuer   https://mcp.example.com
resource https://mcp.example.com/mcp
```

The backend can remain bound to loopback because the HTTPS frontend owns public
network exposure.

## Unix permission model

MCPD deliberately does not invent a second filesystem/process sandbox. Tool access
is bounded by the Unix account running the daemon. A dedicated unprivileged service
user therefore limits what connected clients can access; running the daemon as
`root` intentionally removes that boundary.

## Observability

Operational events are written to journald and durable tool-call audit events can
be written to JSONL. Tool calls and results use correlation identifiers so operators
can trace an action without storing entire file bodies or bearer credentials in the
normal service log.

See [operations.md](operations.md) for runtime state and logging details and
[tools.md](tools.md) for tool-selection and retry-safety guidance.
