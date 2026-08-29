# Observability and audit logs

`mcpd` exposes two complementary log streams.

## Operational logs

`mcpd logs` reads the systemd journal for `mcpd.service`:

```bash
sudo mcpd logs --follow
sudo mcpd logs --lines 200
sudo mcpd logs --since "10 minutes ago"
```

The operational stream includes MCP tool activity in addition to HTTP/session events. Each executed tool emits a structured `mcp tool call` event followed by an `mcp tool result` event with the same `event_id`.

The HTTP access log records the request method/path, Host, remote address, HTTP status, response bytes, and duration. This makes reverse-proxy failures such as `401`/`403` visible without logging request bodies or bearer credentials.

MCP OAuth decisions are logged separately with the resolved JSON-RPC method, whether it came from the body/header, whether authentication was present and valid, and the resulting decision (`allow`, `challenge`, `allow_discovery`, or deferred tool-scope checking). The raw `Authorization` header is never logged.


For `start_process`, the call event includes the shell command and requested wait settings. The result contains the PID and the process state observed when the MCP call returned. The tool-call duration is not a process lifetime limit: a command can remain `running` after `start_process` returns.

Managed processes also emit separate lifecycle events such as `process started` and `process exited`, keyed by PID. This makes long-running commands traceable independently from the MCP request that created them.

Operational arguments are intentionally summarized. File contents, returned file data, and raw `interact_with_process` input are not written to the normal service log. Large commands, paths, and patterns are truncated with an explicit `*_truncated` marker.

## Durable audit log

When `[audit].enabled = true`, tool invocations are also appended to the configured JSONL audit path. A normal system installation uses:

```text
/var/lib/mcpd/audit.jsonl
```

The durable audit event and operational tool logs share the same `event_id`, so an operator can correlate a live journal entry with its audit record.

The audit trail is intended for durable/security-oriented history, while journald is intended for live operations and debugging. Do not remove or replace the audit trail with service logs.

OAuth authorization and refresh events are summarized in the operational journal. Authorization logs include client/resource/scope metadata and explicitly show whether `offline_access` was requested and whether a refresh token was issued. Refresh logs include success/rejection reasons and refresh-family identifiers. Raw authorization codes, PKCE verifiers, access tokens, and refresh tokens are never logged.

## Sensitive data

Treat both logging surfaces as sensitive operational data. In particular, `start_process.command` is logged intentionally so an operator can see what the AI executed. Shell commands can themselves contain user-provided secrets, tokens, or credentials; avoid embedding secrets directly in command strings when possible and protect access to journald and the audit file accordingly.
