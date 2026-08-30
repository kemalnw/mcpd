# Operations

This page covers the runtime behavior of an installed MCPD service. For first-time
installation and public HTTPS setup, see [setup.md](setup.md).

## Service lifecycle

A system installation uses two systemd units:

- `mcpd.service` — MCP/OAuth server and normal interactive tool execution;
- `mcpd-durable.service` — supervisor for restart-surviving durable jobs.

The durable supervisor is intentionally separate so restarting or upgrading the
main MCPD daemon does not kill already-running durable jobs.

Use the CLI instead of calling systemd directly for normal administration:

```bash
sudo mcpd start
sudo mcpd restart
mcpd update --check
sudo mcpd update
mcpd status
mcpd logs --lines 200
mcpd logs --follow
mcpd doctor
sudo mcpd stop
```

`mcpd doctor` checks the installed configuration and service-facing dependencies.

`mcpd update --check` compares the installed version with the latest stable GitHub release without changing the host. `sudo mcpd update` downloads the matching Linux release archive, verifies its SHA-256 checksum, atomically replaces the installed binary, restarts only the main daemon, and waits for the local `/healthz` endpoint. The durable supervisor is kept running. If the new daemon fails to restart or become healthy, MCPD restores the previous binary and restarts the main service again. Use `--force` only when intentionally reinstalling the latest release or replacing a locally newer build.

## Operational logs

`mcpd logs` reads journald for the MCPD services. The normal daemon emits structured
HTTP, OAuth, tool-call, tool-result, and managed-process lifecycle events.

Tool-call and tool-result events share an `event_id`, allowing operators to follow
one invocation without logging entire tool payloads. HTTP logs include request
method/path, remote address, status, response size, and duration.

Sensitive bearer credentials, authorization codes, PKCE verifiers, refresh tokens,
raw file contents, and raw interactive stdin are not intentionally written to the
normal service log. Commands are logged for operational traceability, so do not put
secrets directly in shell command strings when avoidable.

## Audit log

When `[audit].enabled = true`, MCPD also writes a persistent JSONL audit stream.
A normal system installation stores it at:

```text
/var/lib/mcpd/audit.jsonl
```

The audit stream is for durable security/operational history; journald is for live
service diagnosis. Protect access to both because command paths and other
operational metadata can still be sensitive.

## Durable state

A normal system installation stores persistent state under `/var/lib/mcpd`.
Important areas include:

```text
/var/lib/mcpd/
  auth/       OAuth signing, owner, and refresh-token state
  audit.jsonl
  runs/       durable workflow/checkpoint state and run logs
  durable/    restart-surviving durable job state and logs
```

Durable jobs are reconciled when the main daemon reconnects to the supervisor.
Their disk-backed state is authoritative; a client disconnect or daemon restart
should not be treated as evidence that the job must be started again.

Workflow runs persist higher-level objectives, checkpoints, handoffs, and bounded
log references. Automatic garbage collection removes eligible old terminal runs
according to the `[workflow]` retention settings while protecting active/leased
work. `collect_workflow_garbage` can preview or explicitly request bounded cleanup
when an operator needs to inspect or accelerate that process.

## Process and search retention

Normal process sessions and progressive searches are runtime resources rather than
MCP transport sessions. Their handles can remain readable after completion for the
configured retention window, but they are not the right mechanism for work that
must survive daemon restarts. Use durable jobs for that case.

Large process/search responses are intentionally bounded. Continue from returned
handles and cursors instead of assuming one MCP response contains the entire log or
result set.

## Configuration changes

`mcpd setup` is the preferred human-facing reconfiguration path. It preserves a
compatible existing configuration by default and requires an explicit reconfigure
choice before replacing it.

For automation that already owns a configuration file, `mcpd install --config ...`
is the lower-level deterministic path. See [../configs/mcpd.example.toml](../configs/mcpd.example.toml)
for the available configuration groups.
