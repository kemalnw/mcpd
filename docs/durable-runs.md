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

## Agent-session handoff

The supervising AI session is intentionally treated as ephemeral. MCPD does not attempt to keep a model/chat alive beyond host turn, context, or wall-clock limits. Instead, `handoff_run` stores a compact handoff checkpoint and `resume_run` returns a bounded source of truth for a fresh agent session.

Handoff checkpoints are safe-to-act-on recovery records, not chat summaries. Facts are stored separately from advisory recommendations: evidence references, active side effects, blockers, pending approvals, cleanup state, `do_not_repeat`, and reconnectable handles remain factual. Recommendations carry source/confidence/revalidation metadata. A resumed session never inherits credentials, tokens, approvals, or write authority from the prior session; pending approvals always require authorization in the current session.

For `before_wait`, at least one active handle must record the last observed state, earliest useful next poll time, and a cancellation path (`cancel_tool`, with `cancel_id` when applicable). This prevents a fresh agent from immediately duplicating work that is already running. Checkpoint freshness is explicit (`missing`, `fresh`, `due`, `overdue`, `future_clock_skew`) rather than inferred from one boolean. Optimistic run revisions prevent stale periodic checkpoints from replacing newer recovery state.

`workflow.checkpoint_interval_seconds` is an advisory freshness target (default 900 seconds). Resume output exposes `checkpoint_age_seconds` and `checkpoint_due`; agents should also checkpoint before long waits and before an anticipated session boundary. This deliberately avoids depending on a guessed host limit such as exactly two hours.

Handoff state stores only compact summary/blockers/next actions and reconnectable handles. Full command output remains in process/job logs and is fetched only when evidence is needed.

## Lifecycle boundaries

This foundation persists workflow intent and evidence; it does **not** yet claim that an OS process survives an MCPD daemon restart. Restart reconciliation belongs to issue #56. MCP-facing create/checkpoint/resume operations are added by issue #48, agent-session handoff by #64, while idempotent creation is issue #47.

Automatic long-horizon retention/garbage collection is intentionally deferred to issue #58 so deletion policy is designed after the durable lifecycle is stable. Until then, durable run directories are preserved rather than removed implicitly.

## Safety properties

- run/job handles are restricted to safe path characters before filesystem use;
- state/log directories use restrictive permissions;
- corrupted JSON fails explicitly instead of being treated as an empty run;
- state writes are atomic and revision checked;
- full logs are not embedded in run metadata;
- the stored schema version provides an explicit future migration boundary.
