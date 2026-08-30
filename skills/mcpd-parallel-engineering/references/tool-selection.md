# MCPD Tool Selection

## Minimize round trips

- Prefer one semantic search over progressively broader duplicate searches. Use `pathHint` when repository name is known but path is not.
- Batch independent known-file reads.
- Use atomic multi-hunk edits for one logical file change instead of sequential edits that can partially apply.
- Pass `cwd` to process execution rather than embedding `cd ... &&`.
- Reuse PIDs/search IDs/batch IDs/run IDs. Handles exist specifically to avoid rediscovery.
- Use output pagination/deltas. Large retained output is evidence storage, not default model context.

## Process choice

A single command belongs in `start_process`. Multiple independent non-interactive commands belong in a process batch when exposed. Interactive REPL/TUI work stays as an individual PTY session because input and resize semantics are session-specific.

If a process returns running after its wait timeout, continue its PID. A wait timeout is not process failure and is never a reason to start a duplicate command.

## Search choice

Use `list_directory` only when browsing a known directory. Use `start_search` for discovery. Continue with `get_more_search_results` until enough evidence is available. Avoid broad home/root searches when a workspace root or path hint can narrow work.

## Mutation choice

Inspect uncertain targets before mutation. Prefer `edit_block` for exact localized source edits, `write_file` for creation/full replacement, and shell commands only when a dedicated filesystem tool does not fit.

## Retry safety

Transport loss does not prove a mutation failed. Reuse `idempotency_key` for the same `start_process`/batch creation, `operation_key` for one logical interactive stdin submission, and `expected_size` for retry-safe append. Verify a `move_file` postcondition before retrying. For arbitrary Linux PIDs, pair `kill_process` with `expected_start_ticks` from `list_processes`; never treat PID alone as a durable identity.
