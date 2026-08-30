# Failure and Recovery Policy

## Local command/test failure

Preserve the failed job/log. Read the smallest failure tail that explains the cause, inspect relevant source, repair that lane, and rerun affected focused gates before the full gate.

## Timing-sensitive test failure

Do not hide with arbitrary sleeps/retries. Determine whether the test asserted scheduler timing instead of contract semantics. Prefer event/state synchronization and repeated/race testing.

## Merge conflict

Rebase onto latest validated main. Resolve by preserving both issue intents, then rerun tests covering both changes plus the full required gate.

## Lost client response/reconnect

Reuse durable run/batch/job handles and idempotency keys. Do not assume a timed-out transport means the server failed to start work.

## MCPD restart

Resume from durable run metadata/logs. Until restart-safe process reconciliation is available, distinguish persisted workflow state from live OS-process supervision; never report an old PID as running without reconciliation.

## CI failure after local green

Treat CI as new evidence. Reproduce when possible, fix the underlying issue, rerun local gates, and wait for fresh remote green checks before merge.
