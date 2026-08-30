# Tool contracts

This document is the compatibility target for `mcpd`. The project keeps
Desktop Commander-style tool names and core semantics where useful, while
returning typed structured output and omitting Desktop Commander product/UI
fields such as `origin`.

## Catalog

### Process and terminal

| Tool | Status |
| --- | --- |
| `start_process` | implemented |
| `start_process_batch` | implemented |
| `read_process_batch` | implemented |
| `cancel_process_batch` | implemented |
| `read_process_output` | implemented |
| `interact_with_process` | implemented |
| `resize_process_pty` | implemented |
| `force_terminate` | implemented |
| `list_sessions` | implemented |
| `list_processes` | implemented |
| `kill_process` | implemented |

### Filesystem and documents

| Tool | Status |
| --- | --- |
| `read_file` | implemented: text + URL; structured formats pending |
| `read_multiple_files` | implemented: text |
| `write_file` | implemented: text |
| `write_pdf` | planned |
| `create_directory` | implemented |
| `list_directory` | implemented |
| `move_file` | implemented |
| `get_file_info` | implemented: native/text metadata; structured metadata pending |
| `edit_block` | implemented: text; Excel/DOCX modes pending |

### Search

| Tool | Status |
| --- | --- |
| `start_search` | implemented |
| `get_more_search_results` | implemented |
| `stop_search` | implemented |
| `list_searches` | implemented |

### Configuration and diagnostics

| Tool | Status |
| --- | --- |
| `get_config` | planned |
| `set_config_value` | planned |
| `get_usage_stats` | planned |
| `get_recent_tool_calls` | planned |

Total target: **28 MCP tools**.

Desktop Commander-specific feedback, prompts, UI telemetry, and `node:local`
virtual-session behavior are intentionally outside the compatibility target.


## Authorization contracts

When OAuth is enabled, every listed tool advertises an OAuth `securitySchemes`
entry. Scope requirements are intentionally coarse and stable:

- `mcp:read`: read-only process/file/search inspection.
- `mcp:write`: commands, stdin interaction, termination, filesystem mutation,
  and other operations capable of changing VM state.

The authoritative check happens after the MCP request body has been parsed.
`Mcp-Name` is never trusted as the source of authorization policy. Unknown future
tools default to `mcp:write` until explicitly classified.

An unauthenticated or under-scoped `tools/call` returns an MCP error result with
`_meta["mcp/www_authenticate"]`, including the required scope and protected
resource metadata URL. Discovery/listing remains available so an OAuth-capable
client can understand the server and initiate linking before a tool executes.

## Process contracts

### `start_process`

```json
{
  "command": "string, required",
  "cwd": "string, optional working directory",
  "timeout_ms": "integer, required",
  "shell": "string, optional",
  "verbose_timing": "boolean, optional",
  "pty": "auto | always | never, optional mcpd extension",
  "separate_streams": "boolean, optional; non-PTY stdout/stderr tagging"
}
```

Semantics:

- `cwd`, when provided, is validated as an existing directory before process creation and is assigned through `exec.Cmd.Dir`; repository-scoped callers should prefer it over embedding `cd <path> &&` in the shell command.
- Process is created before waiting.
- `timeout_ms` limits the tool-call wait only.
- Wait ends on process exit, detected interactive prompt, or timeout.
- Prompt detection considers only the current terminal line after the most recent newline/carriage return; prompt-like text in historical output does not put the session into `waiting_for_input`.
- A running process is never killed merely because `timeout_ms` elapsed.
- The returned `pid` is the real OS PID and is the explicit resource handle.
- Initial process output is capped by both `process.initial_output_lines` (default 200) and `process.response_output_bytes` (default 65536) even when more output is retained server-side. A byte-truncated retained line is previewed without advancing the process cursor; retry with a larger `max_bytes` (capped at 2 MiB) to retrieve it fully. Responses expose byte/truncation metadata instead of silently dropping evidence.
- The result includes `read_from`, `read_count`, `total_lines`, `remaining`, and `evicted_lines` so callers can detect truncated initial output and continue with `read_process_output`.
- `separate_streams=true` is only meaningful for non-PTY commands. Those sessions return `streams` records with `stdout`/`stderr` identity instead of duplicating the same source text in merged `output`/`lines`. PTYs remain a single terminal stream by definition.

### `start_process_batch`, `read_process_batch`, `cancel_process_batch`

Use batches for two or more independent, non-interactive commands. MCP `start_process_batch` / `read_process_batch` default to `output_mode=failures`: running/successful stdout/stderr is suppressed while cursors advance, and terminal failures return bounded tail evidence. Use `output_mode=delta` only when live output is needed, or `none` for state-only polling; full retained per-PID output remains independently retrievable. A fresh terminal failure returns a bounded failure tail (default `process.failure_tail_lines = 100`) rather than the beginning of a noisy build log; `failure_tail`, `omitted_before`, byte/truncation metadata, and the PID make this explicit, while full retained process output remains independently retrievable.  `start_process_batch` accepts stable per-batch job IDs plus the same command/cwd/shell and non-interactive output options as `start_process`. `max_parallel` is capped by `process.batch_max_parallel` (default 4); excess jobs remain queued until a slot is available. `PTY=always` is rejected because interactive terminal control remains an individual-session concern. Jobs may optionally declare `resource_class` as `normal`, `io`, `cpu`, or `heavy`. MCPD applies a global weighted concurrency limiter across all batches so CPU/heavy jobs consume more capacity than lightweight I/O work. `process.batch_global_parallel = 0` chooses a CPU/memory-aware host default; an explicit positive value is a hard global capacity.

Each batch response includes lightweight host CPU/load/available-memory telemetry plus the effective global capacity. Telemetry influences only deterministic scheduling weights; if memory telemetry is unavailable MCPD falls back to CPU/static limits rather than failing execution.

`read_process_batch` defaults to changed-only polling. It waits until any batch job changes state/output (or timeout), then returns only changed jobs and bounded output deltas. Batch output cursors are independent from `read_process_output`, so a caller can later inspect a job PID from its beginning without batch polling having consumed the per-process cursor.

A job failure does not cancel independent siblings. `cancel_process_batch` marks queued jobs canceled and safely terminates running MCPD-managed process groups. Batch state remains `canceled`; completed jobs are not rewritten as failures.

### `read_process_output`

```json
{
  "pid": "integer, required",
  "timeout_ms": "integer, optional; default 5000",
  "offset": "integer, optional; default 0",
  "length": "integer, optional; default 1000",
  "verbose_timing": "boolean, optional"
}
```

Offset semantics:

- `0`: read new output from the cursor and advance it.
- positive: absolute retained line position; cursor unchanged.
- negative: start relative to the end, then read `length`; cursor unchanged.

Returned structure includes PID, process state, exit code, read range, total line count, remaining lines, evicted lines, prompt state, runtime, and an output `generation`. For `offset=0`, any new output bytes increment the generation; if a progress/status line changes without increasing the line count, `latest_line` exposes the updated line so callers do not sleep through `\r`/partial-line progress. Sessions created with `separate_streams=true` return stream-tagged `streams` records instead of merged `lines`.

### `interact_with_process`

```json
{
  "pid": "integer, required",
  "input": "string, required",
  "timeout_ms": "integer, optional; default 8000",
  "wait_for_prompt": "boolean, optional; default true",
  "verbose_timing": "boolean, optional",
  "raw_input": "boolean, optional; default false"
}
```

A newline is appended when absent by default. Set `raw_input=true` to write the exact input bytes without adding a newline, which is useful for control-oriented terminal protocols and programs that read fixed byte counts. When `wait_for_prompt` is true, the call returns when another prompt is detected, the process exits, or timeout elapses. Only output produced after the input snapshot is returned.

### `resize_process_pty`

```json
{
  "pid": "integer, required",
  "rows": "integer, required; 1..65535",
  "cols": "integer, required; 1..65535"
}
```

Resizes a running MCPD-managed PTY using the native terminal ioctl. Non-PTY sessions and exited processes are rejected.

### `force_terminate`

Input: `{ "pid": integer }`.

Operates on an `mcpd`-managed session. Linux behavior is SIGINT to the process
group followed by SIGKILL escalation after a grace period.

### `list_sessions`

Input: `{}`.

Returns active and retained completed `mcpd` process sessions. This differs from
`list_processes`, which enumerates the operating system process table.

### `list_processes`

Input: `{}`.

Returns PID, CPU percentage, memory percentage, and full command line for all
processes visible to the daemon user.

### `kill_process`

Input: `{ "pid": integer }`.

Operates on an arbitrary OS process rather than only an `mcpd` session. The
operation is governed solely by the Unix permissions of the daemon user.

## File facade contracts

`read_file`, `write_file`, and `edit_block` are polymorphic facades. Their
schema already reserves format-specific parameters so adding document handlers
does not require renaming tools or fragmenting the model-facing API.

### `read_file`

```json
{
  "path": "string, required",
  "isUrl": "boolean, optional; default false",
  "offset": "integer, optional; default 0",
  "length": "integer, optional; default configured read limit",
  "sheet": "string, optional; reserved for spreadsheets",
  "range": "string, optional; reserved for structured formats",
  "options": "object, optional; format-specific"
}
```

Current text semantics:

- Local text returns source exactly once in `lines`; `content` is omitted. This
  keeps the payload aligned with line-based pagination without duplicating source text.
- `offset >= 0`: zero-based line position, returning at most `length` lines.
- `offset < 0`: return the last `abs(offset)` lines; `length` is ignored.
- Reads stream through the file and keep only the requested range/tail in
  memory; the complete file is not loaded merely to paginate it.
- `isUrl=true` performs a full textual HTTP/HTTPS fetch with response-size and
  request-time bounds. URL text is returned exactly once in `content`; `lines`
  is omitted while `read_count` and `total_lines` remain available as metadata.

Planned handlers behind this same tool: PNG/JPEG/GIF/WebP, Excel, PDF, and
DOCX outline/raw-XML modes.

### `read_multiple_files`

Input: `{ "paths": ["..."] }`.

Each path is read independently. One failed path produces an error on that item
without aborting successful sibling reads.

### `write_file`

```json
{
  "path": "string, required",
  "content": "string, required",
  "mode": "rewrite | append, optional; default rewrite"
}
```

Current handler writes text directly with the permissions of the daemon user.
Structured format creation will reuse this facade where the format supports it.

### `create_directory`, `move_file`, `list_directory`

`create_directory` recursively creates missing parent directories.
`move_file` uses native rename semantics. `list_directory` defaults to depth 2, keeps the existing per-directory nested limit, prunes developer-noise trees such as `.git/objects`, `node_modules`, caches, and build outputs during recursive traversal, and enforces a global `maxEntries` cap (default 1000). The result reports `truncated` and `pruned` metadata. Set `includePruned=true` only when those normally skipped internals are explicitly needed; directly listing a pruned directory path remains allowed.

### `get_file_info`

Returns size, modification/access/creation timestamps when Linux exposes them,
permissions, file/directory flags, detected file type, and for text files
`line_count`, `last_line`, and `append_position`.

### `edit_block`

```json
{
  "file_path": "string, required",
  "old_string": "string, text mode",
  "new_string": "string, text mode; empty is valid",
  "expected_replacements": "integer, optional; default 1",
  "edits": [{"old_string": "string", "new_string": "string", "expected_replacements": "integer, optional; default 1"}],
  "range": "string, structured mode",
  "content": "any, structured mode",
  "options": "object, optional"
}
```

Single text mode requires the exact occurrence count before modifying the file. Multi-hunk `edits` mode validates every hunk and every original byte range before writing once; ambiguous/missing counts or overlapping hunks reject the entire batch and leave the file unchanged. Per-hunk validation results are returned in `edits`. When no exact match exists, `mcpd` computes the closest candidate and returns a character diff. Similarity of 70% or greater is reported as a correction hint, but fuzzy text is never modified automatically. Replacement preserves symlink targets, and hard-linked files are edited in place so inode sharing is not silently broken.

Planned structured edit handlers: Excel range replacement and DOCX XML
replacement.

## Search contracts

Search is progressive and uses an explicit application-level `sessionId`. That
identifier is a search-resource handle and does not introduce an MCP transport
session.

### `start_search`

```json
{
  "path": "string, required",
  "pattern": "string, required",
  "pathHint": "string, optional; project/repository name used to prioritize likely workspace paths",
  "searchType": "files | content, optional; default files",
  "filePattern": "string, optional; pipe-separated globs",
  "ignoreCase": "boolean, optional; default true",
  "maxResults": "integer, optional; default configured limit",
  "includeHidden": "boolean, optional; default false",
  "contextLines": "integer, optional; default 5",
  "timeout_ms": "integer, optional",
  "earlyTermination": "boolean, optional; default true for files",
  "literalSearch": "boolean, optional; default false"
}
```

The call starts work in the background and returns after the first result chunk,
completion, or the configured initial wait (40 ms by default). Exact filename
searches inherit Desktop Commander-style 1500 ms default timeout when no timeout
is supplied.

When `path` is broad and the daemon has discovered conventional workspace roots (such as `~/src`, `~/workspace`, or `~/projects`), `pathHint` can name the intended project/repository. MCPD maintains a bounded workspace index of Git/module roots under those directories (default refresh 30 seconds, depth 4, max 2048 entries), prefers exact repository-name matches deterministically, and reuses the index across repeated lookups instead of walking the tree each time. Deleted stale entries trigger refresh; newly created repositories become visible on the next refresh. If the index has no match, the existing bounded filesystem fallback preserves broad-search behavior. For exact filename searches with early termination and no hint, MCPD also checks workspace roots before noisy home-level dependency/cache trees.

Ripgrep is selected automatically when available. Otherwise a native Go walker
provides the same MCP contract. The fallback does not currently interpret
`.gitignore`/`.ignore` files, while ripgrep does.

Unlike raw ripgrep `-m`, `maxResults` is a **global match cap** across the whole
search. `filePattern` is an additional filter and therefore intersects the main
filename pattern.

Content-match results include `file`, `line`, the complete source line in `text`,
the matched substring in `match`, and `column` / `end_column`. Columns are 1-based
Unicode character positions; `end_column` is exclusive. Context-only results use
`text` for the complete source line and omit match-span fields. Native and ripgrep
backends share this result contract.

### `get_more_search_results`

```json
{
  "sessionId": "string, required",
  "offset": "integer, optional; default 0",
  "length": "integer, optional; default 100"
}
```

- non-negative `offset`: absolute result range, no implicit cursor;
- negative `offset`: return the last `abs(offset)` retained results and ignore
  `length`;
- `hasMoreResults` remains true while more retained results exist or the search
  is still running.

### `stop_search`

Input: `{ "sessionId": string }`. Cancels a running search but preserves its
already-discovered results until normal retention cleanup.

### `list_searches`

Input: `{}`. Returns running and retained completed sessions with search type,
pattern, backend, status, runtime, total results, and total matches.

Excel and DOCX content search will later merge format-aware results into this
same session contract rather than treating ZIP/XML containers as plain text.

### Resumable batch cursors

`read_process_batch` returns only changed jobs by default. Batch observation uses an opaque **caller-owned cursor** returned by `start_process_batch` / each batch read. Pass that cursor back on continuation reads so separate agents/clients can observe the same batch independently without consuming a shared server cursor. A changed-only read without a cursor establishes a baseline at call time and waits for the next change; a fresh/resumed agent should take a bounded snapshot first. Cursor eviction is explicit via `cursor_evicted` / `evicted_lines`, while per-PID `read_process_output` remains independent.

### Durable run response budgets

`list_runs` is paginated metadata-only output and never inlines objectives, work-item bodies, next actions, or logs. `get_run` returns paginated work items/success criteria/next actions with total/has-more metadata and byte-bounds long descriptive fields; pagination never mutates authoritative stored state. Durable authoritative fields have validation budgets so content cannot grow beyond retrievable/operable limits. `read_run_job_log` applies both line and byte budgets and returns explicit `truncated`/`more_available` metadata while preserving newest tail evidence.
