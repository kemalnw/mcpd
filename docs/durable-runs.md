# Durable engineering runs

MCPD keeps long-horizon engineering workflow state separate from MCP transport sessions and from ephemeral process PIDs. The `internal/workflow` store is the persistence foundation for resumable agent work.

## Storage layout

`workflow.state_dir` defaults beneath the MCPD state directory at `runs/`. Each run receives an opaque `run_<hex>` identifier and owns a private directory:

```text
<workflow.state_dir>/
  run_<id>/
    run.json
    logs/
      <job-id>.log
```

Run metadata is versioned with `schema_version` and revisioned with a monotonically increasing `revision`. State updates require the expected current revision so reconnecting agents cannot silently overwrite a newer checkpoint.

`run.json` is written through a private temporary file, synced, and atomically renamed. Job logs are append-only, mode `0600`, and can be tailed independently from run metadata. This allows future MCP tools to return compact summaries while retaining full execution evidence on the VM.

## Lifecycle boundaries

This foundation persists workflow intent and evidence; it does **not** yet claim that an OS process survives an MCPD daemon restart. Restart reconciliation belongs to issue #56. MCP-facing create/checkpoint/resume operations are added by issue #48, while idempotent creation is issue #47.

Automatic long-horizon retention/garbage collection is intentionally deferred to issue #58 so deletion policy is designed after the durable lifecycle is stable. Until then, durable run directories are preserved rather than removed implicitly.

## Safety properties

- run/job handles are restricted to safe path characters before filesystem use;
- state/log directories use restrictive permissions;
- corrupted JSON fails explicitly instead of being treated as an empty run;
- state writes are atomic and revision checked;
- full logs are not embedded in run metadata;
- the stored schema version provides an explicit future migration boundary.
