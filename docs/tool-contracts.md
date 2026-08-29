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
| `read_file` | planned |
| `read_multiple_files` | planned |
| `write_file` | planned |
| `write_pdf` | planned |
| `create_directory` | planned |
| `list_directory` | planned |
| `move_file` | planned |
| `get_file_info` | planned |
| `edit_block` | planned |

### Search

| Tool | Status |
| --- | --- |
| `start_search` | planned |
| `get_more_search_results` | planned |
| `stop_search` | planned |
| `list_searches` | planned |

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

## Process contracts

### `start_process`

```json
{
  "command": "string, required",
  "timeout_ms": "integer, required",
  "shell": "string, optional",
  "verbose_timing": "boolean, optional",
  "pty": "auto | always | never, optional mcpd extension"
}
```

Semantics:

- Process is created before waiting.
- `timeout_ms` limits the tool-call wait only.
- Wait ends on process exit, detected interactive prompt, or timeout.
- A running process is never killed merely because `timeout_ms` elapsed.
- The returned `pid` is the real OS PID and is the explicit resource handle.

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

## Planned file facade semantics

`read_file`, `write_file`, and `edit_block` will remain polymorphic facades so
models can use a small stable tool surface across formats.

`read_file` target handlers:

- text/code with line pagination,
- URL reads,
- PNG/JPEG/GIF/WebP image content,
- Excel sheet/range reads,
- PDF page/markdown reads,
- DOCX outline mode plus raw XML pagination mode.

`edit_block` target handlers:

- exact/fuzzy text replacement,
- Excel range replacement,
- DOCX XML replacement.

## Planned search semantics

Search remains progressive and uses an explicit application-level `sessionId`.
That identifier is a search-resource handle and does not introduce an MCP
transport session.

Target tools:

```text
start_search -> sessionId
get_more_search_results(sessionId)
stop_search(sessionId)
list_searches()
```

Text/file search will use ripgrep semantics. Excel and DOCX content require
format-aware search adapters rather than treating ZIP/XML containers as plain
text.
