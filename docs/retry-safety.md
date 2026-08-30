# Mutation retry safety

MCP transports and clients can lose a response after the server has already performed a side effect. MCPD therefore distinguishes **transport retry** from simply repeating a command.

| Tool | Retry class | Safe retry contract |
|---|---|---|
| `start_process` | conditionally replay-safe | Set `idempotency_key` before the first call and reuse it only for the same execution request. Timeout/wait duration may change on retry. |
| `start_process_batch` | replay-safe with key | Set `idempotency_key`; conflicting reuse is rejected. |
| `interact_with_process` | conditionally replay-safe | Set a unique `operation_key` for each logical stdin submission; retry the same key+input after response loss. |
| `cancel_process_batch`, `force_terminate`, `stop_search` | idempotent | Repetition has no additional managed-resource side effect. |
| `resize_process_pty`, `create_directory` | idempotent | Repeating the same requested state is safe. |
| `create_run` | replay-safe with key | Set `idempotency_key`; the key digest and request fingerprint are durable. |
| `checkpoint_run`, `handoff_run` | optimistic precondition | Use the current run revision/generation. Stale updates are rejected rather than silently overwriting newer state. |
| `write_file` rewrite | repeat-to-same-content | Same request rewrites the same bytes, but it can overwrite unrelated concurrent changes; inspect before retry when concurrency is possible. |
| `write_file` append | conditionally replay-safe | Pass `expected_size` observed before the first append. A retry verifies an already-applied identical append or fails closed. Do not blindly retry append without it. |
| `edit_block` | exact-precondition / at-most-once effect | Exact old text/count must still match. A successful edit normally makes an identical retry fail rather than applying twice. |
| `move_file` | verify-before-retry | A lost response is ambiguous. Inspect source and destination with `get_file_info` before deciding whether another rename is needed. |
| `kill_process` | identity-precondition | PID alone is unsafe because Linux can reuse it. Prefer `force_terminate` for MCPD sessions. For arbitrary PIDs, copy `start_ticks` from `list_processes` into `expected_start_ticks`; blind retries are not idempotent. |

## Keys and secrets

MCPD never logs raw retry keys. Durable run/batch idempotency persists only digests/fingerprints. Single-process and interactive operation keys are bounded in memory and are not written to audit logs.

An idempotency/operation key identifies **one logical side effect**, not a human workflow. Reusing one key for different command/input content is an explicit conflict.
