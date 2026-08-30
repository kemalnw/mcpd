package tools

import (
	"context"
	"errors"
	"strings"

	"github.com/kemalnw/mcpd/internal/audit"
	workflowmgr "github.com/kemalnw/mcpd/internal/workflow"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type WorkflowTools struct {
	store *workflowmgr.Store
}

func RegisterWorkflow(server *mcp.Server, store *workflowmgr.Store, auditStore *audit.Store) {
	t := &WorkflowTools{store: store}
	mcp.AddTool(server, tool("create_run", "Create a durable engineering run", "Use this at the beginning of substantial or long-horizon engineering work that may span many jobs, PRs, waits, or client reconnects. Store the stable objective and success criteria once; use checkpoint_run for mutable progress. The returned run_id is the durable resume handle.", toolHints{destructive: true}), audited(auditStore, "create_run", t.createRun))
	mcp.AddTool(server, tool("checkpoint_run", "Checkpoint engineering progress", "Use this after meaningful workflow transitions such as implementation green, CI result, merge, blocker, or release step. expected_revision provides optimistic concurrency: read the latest run before retrying a revision conflict. Keep summaries and next actions compact; full command logs belong in job logs.", toolHints{destructive: true}), audited(auditStore, "checkpoint_run", t.checkpointRun))
	mcp.AddTool(server, tool("get_run", "Resume a durable engineering run", "Use this with a known run_id to reconstruct current objective, work items, counts, blockers, and next actions without replaying chat history or command logs. A fresh client should prefer this over rediscovering completed work.", toolHints{readOnly: true, idempotent: true}), audited(auditStore, "get_run", t.getRun))
	mcp.AddTool(server, tool("list_runs", "List durable engineering runs", "Use this to rediscover recent durable run handles and compact status when run_id is unknown. The result is metadata-only and does not inline job logs.", toolHints{readOnly: true, idempotent: true}), audited(auditStore, "list_runs", t.listRuns))
	mcp.AddTool(server, tool("read_run_job_log", "Read a durable job log tail", "Use this only when a run summary/failure indicates deeper execution evidence is needed. Returns a bounded tail from a disk-backed job log instead of loading the full log into model context.", toolHints{readOnly: true, idempotent: true}), audited(auditStore, "read_run_job_log", t.readJobLog))
}

type RunWorkItemInput struct {
	ID        string   `json:"id" jsonschema:"stable work-item identifier unique within the run"`
	Title     string   `json:"title,omitempty" jsonschema:"short work-item title"`
	State     string   `json:"state" jsonschema:"planned, ready, running, blocked, completed, failed, or canceled"`
	DependsOn []string `json:"depends_on,omitempty" jsonschema:"work-item ids this item depends on"`
	JobID     string   `json:"job_id,omitempty" jsonschema:"associated MCPD job identifier when applicable"`
	PID       int      `json:"pid,omitempty" jsonschema:"associated live process PID when applicable"`
	Branch    string   `json:"branch,omitempty" jsonschema:"associated Git branch"`
	Worktree  string   `json:"worktree,omitempty" jsonschema:"associated isolated Git worktree path"`
	PRURL     string   `json:"pr_url,omitempty" jsonschema:"associated pull request URL"`
	Summary   string   `json:"summary,omitempty" jsonschema:"compact current evidence/status; do not paste full logs"`
}

type CreateRunInput struct {
	Title           string   `json:"title" jsonschema:"short durable run title"`
	Objective       string   `json:"objective" jsonschema:"stable end goal for this engineering run"`
	SuccessCriteria []string `json:"success_criteria" jsonschema:"verifiable conditions that define completion"`
}

type CheckpointRunInput struct {
	RunID            string             `json:"run_id" jsonschema:"durable run identifier returned by create_run"`
	ExpectedRevision uint64             `json:"expected_revision" jsonschema:"current revision from create_run/get_run; stale revisions are rejected"`
	State            string             `json:"state" jsonschema:"planned, running, blocked, completed, failed, or canceled"`
	Phase            string             `json:"phase,omitempty" jsonschema:"compact current workflow phase"`
	Items            []RunWorkItemInput `json:"items,omitempty" jsonschema:"complete current work-item checkpoint; omitted means preserve existing items"`
	ReplaceItems     bool               `json:"replace_items,omitempty" jsonschema:"when true, replace stored items with items; otherwise omitted/empty items preserve current items"`
	NextActions      []string           `json:"next_actions,omitempty" jsonschema:"small ordered set of next actionable steps"`
	ReplaceNext      bool               `json:"replace_next_actions,omitempty" jsonschema:"when true, replace next actions including with an empty list"`
}

type GetRunInput struct {
	RunID string `json:"run_id" jsonschema:"durable run identifier"`
}

type ReadRunJobLogInput struct {
	RunID     string `json:"run_id" jsonschema:"durable run identifier"`
	JobID     string `json:"job_id" jsonschema:"job identifier whose disk log should be tailed"`
	TailLines int    `json:"tail_lines,omitempty" jsonschema:"maximum tail lines to return; defaults to 100 and is capped at 1000"`
}

type RunCounts struct {
	Planned   int `json:"planned"`
	Ready     int `json:"ready"`
	Running   int `json:"running"`
	Blocked   int `json:"blocked"`
	Completed int `json:"completed"`
	Failed    int `json:"failed"`
	Canceled  int `json:"canceled"`
}

type RunView struct {
	Run    workflowmgr.Run `json:"run"`
	Counts RunCounts       `json:"counts"`
}

type ListRunsOutput struct {
	Runs []RunView `json:"runs"`
}

type RunJobLogOutput struct {
	RunID string   `json:"run_id"`
	JobID string   `json:"job_id"`
	Lines []string `json:"lines"`
}

func (t *WorkflowTools) createRun(_ context.Context, in CreateRunInput) (RunView, error) {
	run, err := t.store.Create(workflowmgr.CreateRequest{Title: in.Title, Objective: in.Objective, SuccessCriteria: in.SuccessCriteria})
	if err != nil {
		return RunView{}, err
	}
	return viewRun(run), nil
}

func (t *WorkflowTools) checkpointRun(_ context.Context, in CheckpointRunInput) (RunView, error) {
	state, err := parseRunState(in.State)
	if err != nil {
		return RunView{}, err
	}
	items, err := convertRunItems(in.Items)
	if err != nil {
		return RunView{}, err
	}
	run, err := t.store.Update(in.RunID, in.ExpectedRevision, func(run *workflowmgr.Run) error {
		run.State = state
		run.Phase = strings.TrimSpace(in.Phase)
		if in.ReplaceItems {
			run.Items = items
		}
		if in.ReplaceNext {
			run.NextActions = compactStrings(in.NextActions)
		}
		return workflowmgr.ValidateRun(*run)
	})
	if err != nil {
		return RunView{}, err
	}
	return viewRun(run), nil
}

func (t *WorkflowTools) getRun(_ context.Context, in GetRunInput) (RunView, error) {
	run, err := t.store.Get(in.RunID)
	if err != nil {
		return RunView{}, err
	}
	return viewRun(run), nil
}

func (t *WorkflowTools) listRuns(_ context.Context, _ EmptyInput) (ListRunsOutput, error) {
	runs, err := t.store.List()
	if err != nil {
		return ListRunsOutput{}, err
	}
	out := ListRunsOutput{Runs: make([]RunView, 0, len(runs))}
	for _, run := range runs {
		out.Runs = append(out.Runs, viewRun(run))
	}
	return out, nil
}

func (t *WorkflowTools) readJobLog(_ context.Context, in ReadRunJobLogInput) (RunJobLogOutput, error) {
	lines := in.TailLines
	if lines == 0 {
		lines = 100
	}
	if lines < 0 {
		return RunJobLogOutput{}, errors.New("tail_lines must be >= 0")
	}
	if lines > 1000 {
		lines = 1000
	}
	tail, err := t.store.ReadJobLogTail(in.RunID, in.JobID, lines)
	if err != nil {
		return RunJobLogOutput{}, err
	}
	return RunJobLogOutput{RunID: in.RunID, JobID: in.JobID, Lines: tail}, nil
}

func parseRunState(raw string) (workflowmgr.RunState, error) {
	state := workflowmgr.RunState(strings.TrimSpace(raw))
	switch state {
	case workflowmgr.RunPlanned, workflowmgr.RunRunning, workflowmgr.RunBlocked, workflowmgr.RunCompleted, workflowmgr.RunFailed, workflowmgr.RunCanceled:
		return state, nil
	default:
		return "", errors.New("invalid run state")
	}
}

func convertRunItems(inputs []RunWorkItemInput) ([]workflowmgr.WorkItem, error) {
	items := make([]workflowmgr.WorkItem, 0, len(inputs))
	for _, input := range inputs {
		item := workflowmgr.WorkItem{ID: strings.TrimSpace(input.ID), Title: strings.TrimSpace(input.Title), State: workflowmgr.ItemState(strings.TrimSpace(input.State)), DependsOn: compactStrings(input.DependsOn), JobID: strings.TrimSpace(input.JobID), PID: input.PID, Branch: strings.TrimSpace(input.Branch), Worktree: strings.TrimSpace(input.Worktree), PRURL: strings.TrimSpace(input.PRURL), Summary: strings.TrimSpace(input.Summary)}
		items = append(items, item)
	}
	probe := workflowmgr.Run{SchemaVersion: workflowmgr.SchemaVersion, ID: "run_validation", Revision: 1, Title: "validation", State: workflowmgr.RunRunning, Items: items}
	if err := workflowmgr.ValidateRun(probe); err != nil {
		return nil, err
	}
	return items, nil
}

func compactStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func viewRun(run workflowmgr.Run) RunView {
	view := RunView{Run: run}
	for _, item := range run.Items {
		switch item.State {
		case workflowmgr.ItemPlanned:
			view.Counts.Planned++
		case workflowmgr.ItemReady:
			view.Counts.Ready++
		case workflowmgr.ItemRunning:
			view.Counts.Running++
		case workflowmgr.ItemBlocked:
			view.Counts.Blocked++
		case workflowmgr.ItemCompleted:
			view.Counts.Completed++
		case workflowmgr.ItemFailed:
			view.Counts.Failed++
		case workflowmgr.ItemCanceled:
			view.Counts.Canceled++
		}
	}
	return view
}
