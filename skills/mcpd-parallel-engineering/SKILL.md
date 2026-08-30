---
name: mcpd-parallel-engineering
description: Use for substantial software-engineering work executed through MCPD, especially multi-issue implementations, repository audits, long-running builds/tests, parallel Git worktrees, CI-gated pull requests, releases, or tasks where wall-clock time and context efficiency matter. Build a dependency DAG, maximize safe parallelism, preserve correctness gates, use MCPD batch/run primitives when available, checkpoint long work, and recover without repeating completed work.
license: MIT
compatibility: Requires access to an MCPD server and Git for repository workflows. GitHub-specific PR/CI steps require authenticated gh CLI.
metadata:
  author: Kemal
  version: "1.0.0"
---

# MCPD Parallel Engineering

Use this workflow when engineering work has multiple independent lanes, expensive validation, or may outlive one chat/session. Optimize **wall-clock time, tool calls, and model-context bytes** without weakening correctness.

## Operating principles

1. Convert the goal into a verifiable final state before mutation.
2. Inspect enough repository state to identify dependencies and likely file overlap.
3. Build a DAG of work items. Parallelize only nodes that are actually independent.
4. Prefer one branch + one Git worktree + one PR per independent coding item.
5. Keep the VM busy: when one lane is waiting on tests/CI, work on another ready lane.
6. Use MCPD batch execution for 2+ independent non-interactive commands when the server exposes it. Otherwise start independent processes separately before polling any of them.
7. Poll deltas, not history. Never restart a command merely to learn the state of an existing MCPD process/job.
8. Keep full logs on the VM. Return compact status, changed output, and failure tails unless deeper evidence is needed.
9. Every item must satisfy its local quality gate before PR/merge. Remote CI must be green before merge.
10. After upstream merges, rebase overlapping/in-flight branches and rerun affected validation.
11. Run a final integration gate on resulting `main` before release/deploy.
12. For long work, checkpoint objective, completed work, blockers, evidence, and next actions in MCPD durable runs when available.
13. Retries of expensive work must use idempotency keys when supported.
14. Never claim background completion. If work cannot be completed in the current agent session, leave a durable checkpoint that a fresh session can resume.

## Tool-selection fast path

Read [references/tool-selection.md](references/tool-selection.md) before a large MCPD workflow if tool choice is uncertain.

- Known file -> `read_file`; several known files -> `read_multiple_files`.
- Unknown filename/content -> `start_search`, then continue that search ID instead of duplicating it.
- Localized source mutation -> `edit_block`; use atomic multi-hunk edits when multiple exact hunks belong to one logical change.
- Shell/build/test/Git/package manager -> `start_process` with `cwd` instead of shell `cd`.
- Two or more independent shell jobs -> prefer `start_process_batch` when present.
- Existing PID -> `read_process_output` / `interact_with_process`; do not spawn a replacement inspection command.
- Existing batch -> `read_process_batch` with changed-only semantics when present.
- Long-horizon workflow -> use durable run/checkpoint tools when present.

Capability rule: MCP clients can cache old tool schemas. Use the best primitive actually exposed in the current tool catalog; do not invent unavailable fields/tools. If server and client schemas disagree after an upgrade, verify fresh `tools/list` and reconnect/reload the client.

## Workflow

### 1. Discover and classify

Inspect repository status, current branch, relevant code/tests, existing issues, and CI/release rules. Classify each proposed item as:

- independent now;
- dependent on another item;
- likely file-conflicting but logically independent;
- integration/release gate.

Do not serially inspect unrelated areas when independent reads/searches can be batched.

### 2. Build the execution DAG

For each item record: ID, objective, acceptance criteria, dependencies, likely files, validation commands, and final evidence. Keep dependencies minimal; a shared final integration test is not a reason to serialize independent implementation.

See [references/dag-and-scheduling.md](references/dag-and-scheduling.md) for scheduling and backpressure rules.

### 3. Isolate coding lanes

For independent repository mutations, create separate branches/worktrees from the same validated base. Never let two active workers edit the same checkout. If work later overlaps, merge/rebase intentionally rather than sharing a working tree.

### 4. Execute and validate in parallel

Start independent implementations and expensive validations concurrently. Prefer server-side batch execution when available. For each item, progress through:

`inspect -> implement -> focused tests -> repeated concurrency/timing tests when relevant -> race detector when relevant -> full tests/vet/lint -> PR -> remote CI -> merge`

A lane that fails should be repaired and rerun only for affected gates; do not restart successful independent lanes.

### 5. Consume waits productively

CI, race tests, builds, and external checks are opportunities to work on other ready DAG nodes. Avoid tight polling. Poll changed state at meaningful intervals or use batch wait-for-any-change semantics.

### 6. Merge safely

Merge only after required remote checks are green. After a merge, identify remaining branches whose base/files overlap with the merged change. Rebase those branches, resolve conflicts preserving both intents, and rerun affected tests before their PRs merge.

### 7. Integrate and release

Once all required items are merged:

- sync `main`;
- run the complete integration/release gate;
- confirm the working tree is clean;
- tag only the validated main commit;
- deploy the exact validated/released binary;
- verify service health/version and a fresh MCP `tools/list` schema.

### 8. Checkpoint long work

When durable run tools are exposed, checkpoint after meaningful state transitions: issue/PR created, implementation green, CI green, merge, conflict/blocker, release/deploy. A resume summary should answer: what is done, what is running, what failed, what is blocked, and what is ready next—without embedding full logs.

## Quality bar

Do not trade correctness for parallelism. Tests that depend on scheduler timing should be rewritten around observable state/events rather than enlarged sleeps. Concurrency changes require race-detector coverage. Failures discovered by CI must be reproduced and fixed rather than hidden by retries.

For detailed failure/recovery policy, read [references/recovery.md](references/recovery.md).
