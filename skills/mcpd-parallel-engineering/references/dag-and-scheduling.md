# DAG and Scheduling Policy

Represent each work item as a node. Add an edge only when the downstream item cannot be implemented or validated correctly before the upstream item finishes.

## Ready rule

A node is ready when all required dependencies are successfully complete and its required resource/worktree lease is available. Independent failures must not stop unrelated ready nodes.

## Parallelism

Use the lowest of the explicit user/admin cap, MCPD server cap, and a sensible workload/resource cap. Compilation and race tests are CPU/memory-heavy; repository inspection and network/CI polling are usually lighter. More parallel jobs can increase wall-clock time when the VM is saturated.

## Worktree policy

Default independent coding node -> independent branch/worktree. Do not use shared checkout mutation as a concurrency mechanism. File overlap is allowed across worktrees; it becomes an intentional rebase/merge concern later.

## CI scheduling

Once a lane opens a PR and CI starts, immediately select another ready node. When CI completes, consume the result without abandoning unrelated in-flight work.
