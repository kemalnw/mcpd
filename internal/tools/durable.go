package tools

import (
	"context"
	"errors"
	"time"

	"github.com/kemalnw/mcpd/internal/audit"
	durablemgr "github.com/kemalnw/mcpd/internal/durableexec"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type DurableTools struct{ manager *durablemgr.Manager }

func RegisterDurable(server *mcp.Server, manager *durablemgr.Manager, auditStore *audit.Store) {
	t := &DurableTools{manager: manager}
	mcp.AddTool(server, tool("start_durable_job", "Start a restart-surviving command", "Use this for non-interactive work expected to run across MCPD daemon restarts or long AI-session gaps. The command is executed by the separate durable supervisor and writes stdout/stderr to a disk-backed log. Supply idempotency_key before the first call for response-loss retry safety. Prefer ordinary start_process/start_process_batch for short work because those provide richer live interaction.", toolHints{destructive: true, openWorld: true}), audited(auditStore, "start_durable_job", t.start))
	mcp.AddTool(server, tool("get_durable_job", "Inspect a durable command", "Use this with a durable job_id to read its authoritative compact state after client reconnect or MCPD daemon restart. The state includes process identity, terminal reason, exit code, and log path metadata but never the raw command.", toolHints{readOnly: true, idempotent: true}), audited(auditStore, "get_durable_job", t.get))
	mcp.AddTool(server, tool("list_durable_jobs", "List durable commands", "Use this to rediscover recent durable job handles when job_id is unknown. Results are compact and paginated; read_durable_job_log is separate so listing jobs never inlines command output.", toolHints{readOnly: true, idempotent: true}), audited(auditStore, "list_durable_jobs", t.list))
	mcp.AddTool(server, tool("read_durable_job_log", "Read a durable command log tail", "Use this only when durable job state says output evidence is needed. Returns a byte-bounded tail from the disk log; increase max_bytes explicitly for deeper evidence instead of loading the full log into model context.", toolHints{readOnly: true, idempotent: true}), audited(auditStore, "read_durable_job_log", t.readLog))
	mcp.AddTool(server, tool("cancel_durable_job", "Cancel a durable command", "Use this to stop a non-terminal durable job. MCPD validates the persisted boot ID and runner process start identity before signaling, so a stale/reused PID is never treated as the old job. Repeating cancellation of a terminal job is safe.", toolHints{destructive: true, idempotent: true}), audited(auditStore, "cancel_durable_job", t.cancel))
}

type StartDurableJobInput struct {
	Command        string `json:"command" jsonschema:"non-interactive shell command to execute under the durable supervisor"`
	CWD            string `json:"cwd,omitempty" jsonschema:"optional working directory"`
	Shell          string `json:"shell,omitempty" jsonschema:"optional shell executable; defaults to /bin/bash"`
	IdempotencyKey string `json:"idempotency_key,omitempty" jsonschema:"optional retry-safety key; equivalent retries return the same durable job instead of executing twice"`
}

type StartDurableJobOutput struct {
	Job              DurableJobView `json:"job"`
	IdempotentReplay bool           `json:"idempotent_replay,omitempty"`
}

type DurableJobView struct {
	ID            string           `json:"job_id"`
	State         durablemgr.State `json:"state"`
	RunnerPID     int              `json:"runner_pid,omitempty"`
	ChildPID      int              `json:"child_pid,omitempty"`
	CommandSHA256 string           `json:"command_sha256"`
	CommandBytes  int              `json:"command_bytes"`
	CWD           string           `json:"cwd,omitempty"`
	Shell         string           `json:"shell"`
	StartedAt     time.Time        `json:"started_at"`
	UpdatedAt     time.Time        `json:"updated_at"`
	FinishedAt    *time.Time       `json:"finished_at,omitempty"`
	ExitCode      *int             `json:"exit_code,omitempty"`
	Reason        string           `json:"reason,omitempty"`
}

type DurableJobInput struct {
	JobID string `json:"job_id" jsonschema:"durable job identifier returned by start_durable_job"`
}

type ListDurableJobsInput struct {
	Offset int `json:"offset,omitempty" jsonschema:"zero-based job offset; defaults to 0"`
	Limit  int `json:"limit,omitempty" jsonschema:"maximum jobs to return; defaults to 50 and is capped at 200"`
}

type ListDurableJobsOutput struct {
	Jobs       []DurableJobView `json:"jobs"`
	Offset     int              `json:"offset"`
	Returned   int              `json:"returned"`
	Total      int              `json:"total"`
	More       bool             `json:"more"`
	NextOffset int              `json:"next_offset,omitempty"`
}

type ReadDurableJobLogInput struct {
	JobID    string `json:"job_id" jsonschema:"durable job identifier"`
	MaxBytes int    `json:"max_bytes,omitempty" jsonschema:"maximum tail bytes; defaults to 65536 and is capped at 1048576"`
}

func (t *DurableTools) start(ctx context.Context, in StartDurableJobInput) (StartDurableJobOutput, error) {
	job, replay, err := t.manager.Start(ctx, durablemgr.StartRequest{Command: in.Command, CWD: in.CWD, Shell: in.Shell, IdempotencyKey: in.IdempotencyKey})
	if err != nil {
		return StartDurableJobOutput{}, err
	}
	return StartDurableJobOutput{Job: viewDurableJob(job), IdempotentReplay: replay}, nil
}
func (t *DurableTools) get(_ context.Context, in DurableJobInput) (DurableJobView, error) {
	job, err := t.manager.Get(in.JobID)
	if err != nil {
		return DurableJobView{}, err
	}
	return viewDurableJob(job), nil
}
func (t *DurableTools) list(_ context.Context, in ListDurableJobsInput) (ListDurableJobsOutput, error) {
	if in.Offset < 0 || in.Limit < 0 {
		return ListDurableJobsOutput{}, errors.New("offset and limit must be >= 0")
	}
	limit := in.Limit
	if limit == 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	jobs, err := t.manager.List()
	if err != nil {
		return ListDurableJobsOutput{}, err
	}
	offset := in.Offset
	if offset > len(jobs) {
		offset = len(jobs)
	}
	end := offset + limit
	if end > len(jobs) {
		end = len(jobs)
	}
	views := make([]DurableJobView, 0, end-offset)
	for _, job := range jobs[offset:end] {
		views = append(views, viewDurableJob(job))
	}
	out := ListDurableJobsOutput{Jobs: views, Offset: offset, Returned: end - offset, Total: len(jobs), More: end < len(jobs)}
	if out.More {
		out.NextOffset = end
	}
	return out, nil
}
func (t *DurableTools) readLog(_ context.Context, in ReadDurableJobLogInput) (durablemgr.LogTail, error) {
	return t.manager.ReadLogTail(in.JobID, in.MaxBytes)
}
func (t *DurableTools) cancel(ctx context.Context, in DurableJobInput) (DurableJobView, error) {
	job, err := t.manager.Cancel(ctx, in.JobID)
	if err != nil {
		return DurableJobView{}, err
	}
	return viewDurableJob(job), nil
}

func viewDurableJob(job durablemgr.Job) DurableJobView {
	return DurableJobView{ID: job.ID, State: job.State, RunnerPID: job.RunnerPID, ChildPID: job.ChildPID, CommandSHA256: job.CommandSHA256, CommandBytes: job.CommandBytes, CWD: job.CWD, Shell: job.Shell, StartedAt: job.StartedAt, UpdatedAt: job.UpdatedAt, FinishedAt: job.FinishedAt, ExitCode: job.ExitCode, Reason: job.Reason}
}
