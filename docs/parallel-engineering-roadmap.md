# Parallel engineering roadmap

MCPD is evolving toward a deterministic, high-throughput execution backend for AI engineering agents. Planning remains an agent responsibility; MCPD provides safe parallel execution, durable state, compact logs, scheduling primitives, and standards adapters.

## Roadmap

### P0 foundation

- #45 process batches with delta polling
- #46 durable engineering runs and disk-backed logs
- #47 retry-safe idempotency keys
- #48 checkpoint/resume APIs
- #49 bundled `mcpd-parallel-engineering` Agent Skill

### P1 scheduling and efficiency

- #50 dependency DAG scheduling
- #51 resource-aware concurrency/backpressure
- #52 worktree/resource leases
- #53 compact failure tails and output budgets
- #54 run/server schema diagnostics
- #55 benchmark/regression harness

### P2 long-horizon durability

- #56 restart reconciliation
- #57 MCP Tasks extension adapter
- #58 retention and garbage collection

The umbrella tracker is #59. Independent items should be developed in isolated worktrees and merged only after local gates plus GitHub CI/CodeQL are green.

## Skills

The repository ships Agent Skills under `skills/`. They use the open `SKILL.md` format, so they can be versioned with MCPD and copied into compatible agent environments. The primary skill is `mcpd-parallel-engineering`, which captures DAG planning, worktree isolation, batch execution, CI gating, recovery, checkpointing, and release verification.
