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
| `read_process_output` | implemented |
| `interact_with_process` | implemented |
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

Total target: **24 MCP tools**.

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
  "pty": "auto | always | never, optional mcpd extension"
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
- Initial process output is capped by `process.initial_output_lines` (default 200) even when more output is retained server-side.
- The result includes `read_from`, `read_count`, `total_lines`, `remaining`, and `evicted_lines` so callers can detect truncated initial output and continue with `read_process_output`.

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

Returned structure includes PID, process state, exit code, lines, read range,
total line count, remaining lines, evicted lines, prompt state, and runtime.

### `interact_with_process`

```json
{
  "pid": "integer, required",
  "input": "string, required",
  "timeout_ms": "integer, optional; default 8000",
  "wait_for_prompt": "boolean, optional; default true",
  "verbose_timing": "boolean, optional"
}
```

A newline is appended when absent. When `wait_for_prompt` is true, the call
returns when another prompt is detected, the process exits, or timeout elapses.
Only output produced after the input snapshot is returned.

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
`move_file` uses native rename semantics. `list_directory` defaults to depth 2,
returns every top-level entry, and caps each nested directory to the configured
entry limit while returning the hidden count.

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
  "range": "string, structured mode",
  "content": "any, structured mode",
  "options": "object, optional"
}
```

Text mode requires the exact occurrence count before modifying the file. When
no exact match exists, `mcpd` computes the closest candidate and returns a
character diff. Similarity of 70% or greater is reported as a correction hint,
but fuzzy text is never modified automatically. Atomic replacement preserves
symlink targets, and hard-linked files are edited in place so inode sharing is
not silently broken.

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

When `path` is broad and the daemon has discovered conventional workspace roots
(such as `~/src`, `~/workspace`, or `~/projects`), `pathHint` can name the intended
project/repository. MCPD resolves that hint to a matching workspace directory before
running the search, avoiding repeated searches from progressively broader roots.
For exact filename searches with early termination and no hint, MCPD also checks
workspace roots before noisy home-level dependency/cache trees. If no preferred
match is found, normal broad-search semantics are preserved.

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
