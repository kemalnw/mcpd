# Tools

MCPD exposes Linux capabilities through MCP tools. This page explains how the
tool groups fit together and which execution model to choose.

The MCP `tools/list` response is the source of truth for current input/output
schemas. Keeping field-by-field schemas in this document would duplicate the
server and become stale as the API evolves.

## Process and terminal

Use the normal process tools for interactive work, short commands, builds, tests,
package managers, and shell-driven repository work.

Core tools include:

- `start_process` — run one command, optionally with a PTY;
- `read_process_output` — continue reading retained output from a process handle;
- `interact_with_process` — send stdin to an interactive process;
- `resize_process_pty` — resize an active PTY;
- `force_terminate` — stop an MCPD-managed process group safely;
- `list_sessions` — rediscover MCPD-managed process sessions;
- `list_processes` / `kill_process` — inspect or terminate arbitrary visible Linux processes.

For two or more independent non-interactive commands, prefer
`start_process_batch`. MCPD applies per-batch and global concurrency limits rather
than allowing every agent request to create unbounded parallel work. Continue a
batch with `read_process_batch` and stop it with `cancel_process_batch`.

A timeout on `start_process` controls how long the MCP call waits; it does not kill
a command that is still running.

## Filesystem

Filesystem tools operate directly with the daemon user's Unix permissions.

- `read_file` and `read_multiple_files` read local text files; `read_file` can also fetch textual HTTP/HTTPS URLs.
- `write_file` creates, rewrites, or appends text.
- `edit_block` performs exact, validated partial edits and supports atomic multi-hunk changes.
- `create_directory`, `list_directory`, `move_file`, and `get_file_info` provide native filesystem operations.

Prefer `edit_block` for localized source changes. Prefer `write_file` when the
complete desired contents are known.

## Search

`start_search` performs progressive filename or content search and returns a
search handle. MCPD uses ripgrep when available and falls back to native Go search.

Continue an existing search with `get_more_search_results`, rediscover handles with
`list_searches`, and use `stop_search` only when an active search is no longer
needed.

Prefer `list_directory` for browsing one known directory and `read_file` when the
exact path is already known.

## Durable execution

Normal process sessions are optimized for live interaction. Use durable jobs for
non-interactive work that must remain discoverable across MCP client disconnects,
long agent-session gaps, or MCPD daemon restarts.

- `start_durable_job` starts a command under the durable supervisor;
- `get_durable_job` reads authoritative compact state;
- `list_durable_jobs` rediscovers recent job handles;
- `read_durable_job_log` reads a bounded tail of the disk-backed log;
- `cancel_durable_job` stops a non-terminal durable job safely.

Durable job state is intentionally compact. Command output stays in disk-backed
logs and is read only when evidence is needed.

## Durable workflows

Workflow tools persist higher-level engineering progress separately from process
handles and chat history.

- `create_run` creates a durable work record;
- `checkpoint_run` stores progress using revision-based concurrency control;
- `get_run` and `list_runs` recover current state without replaying chat history;
- `handoff_run` stores a compact session handoff;
- `resume_run` reconstructs the actionable handoff state;
- `read_run_job_log` reads bounded execution evidence;
- `collect_workflow_garbage` previews or performs bounded cleanup of stale terminal runs.

Workflow state is for objectives, progress, blockers, and reconnectable handles.
Do not duplicate large command logs inside checkpoints.

## Retry safety

A transport retry is not automatically safe just because the original response
was lost. MCPD exposes explicit retry contracts for operations with side effects.

| Operation | Safe retry pattern |
| --- | --- |
| `start_process`, `start_process_batch`, `start_durable_job`, run creation | Set an idempotency key before the first call and reuse it only for the same logical request. |
| `interact_with_process` | Use a unique `operation_key` for each logical stdin submission. |
| `checkpoint_run`, `handoff_run` | Use the current revision/generation; stale updates fail instead of overwriting newer state. |
| `write_file` append | Pass the observed `expected_size`; do not blindly retry append without it. |
| `edit_block` | Exact preconditions make an already-applied edit normally fail rather than apply twice. |
| `move_file` | Inspect source and destination before retrying after an ambiguous response loss. |
| `kill_process` | Use `expected_start_ticks` from `list_processes` so PID reuse cannot target the wrong process. |
| Managed cancellation/termination | Repeating the same cancellation is designed to be safe. |

Retry keys identify one logical side effect, not an entire human workflow. Reusing
a key for different command or input content is rejected or unsafe by design.
