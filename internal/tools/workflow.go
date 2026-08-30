package tools

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/kemalnw/mcpd/internal/audit"
	workflowmgr "github.com/kemalnw/mcpd/internal/workflow"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type WorkflowTools struct {
	store              *workflowmgr.Store
	checkpointInterval time.Duration
	completedRetention time.Duration
	gcMaxDeletes       int
	now                func() time.Time
}

func RegisterWorkflow(server *mcp.Server, store *workflowmgr.Store, auditStore *audit.Store, checkpointInterval, completedRetention time.Duration, gcMaxDeletes int) {
	t := &WorkflowTools{store: store, checkpointInterval: checkpointInterval, completedRetention: completedRetention, gcMaxDeletes: gcMaxDeletes, now: func() time.Time { return time.Now().UTC() }}
	mcp.AddTool(server, tool("create_run", "Create a durable engineering run", "Use this at the beginning of substantial or long-horizon engineering work that may span many jobs, PRs, waits, or client reconnects. Store the stable objective and success criteria once; use checkpoint_run for mutable progress. The returned run_id is the durable resume handle.", toolHints{destructive: true}), audited(auditStore, "create_run", t.createRun))
	mcp.AddTool(server, tool("checkpoint_run", "Checkpoint engineering progress", "Use this after meaningful workflow transitions such as implementation green, CI result, merge, blocker, or release step. expected_revision provides optimistic concurrency: read the latest run before retrying a revision conflict. Keep summaries and next actions compact; full command logs belong in job logs.", toolHints{destructive: true}), audited(auditStore, "checkpoint_run", t.checkpointRun))
	mcp.AddTool(server, tool("get_run", "Resume a durable engineering run", "Use this with a known run_id to reconstruct current objective, work items, counts, blockers, and next actions without replaying chat history or command logs. A fresh client should prefer this over rediscovering completed work.", toolHints{readOnly: true, idempotent: true}), audited(auditStore, "get_run", t.getRun))
	mcp.AddTool(server, tool("list_runs", "List durable engineering runs", "Use this to rediscover recent durable run handles and compact status when run_id is unknown. The result is a paginated metadata-only summary: it never inlines objectives, work-item bodies, next actions, or job logs.", toolHints{readOnly: true, idempotent: true}), audited(auditStore, "list_runs", t.listRuns))
	mcp.AddTool(server, tool("read_run_job_log", "Read a durable job log tail", "Use this only when a run summary/failure indicates deeper execution evidence is needed. Returns a bounded tail from a disk-backed job log instead of loading the full log into model context.", toolHints{readOnly: true, idempotent: true}), audited(auditStore, "read_run_job_log", t.readJobLog))
	mcp.AddTool(server, tool("collect_workflow_garbage", "Preview or collect stale workflow state", "Use this to preview retention cleanup or explicitly collect old terminal durable runs. It never deletes active or actively leased runs. The default is preview-only; set execute=true to perform bounded restart-safe cleanup. Normal automatic GC already runs from configured retention policy, so manual execution is mainly for disk-pressure or operator verification.", toolHints{destructive: true}), audited(auditStore, "collect_workflow_garbage", t.collectGarbage))
	registerHandoffTools(server, t, auditStore)
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
	IdempotencyKey  string   `json:"idempotency_key,omitempty" jsonschema:"optional retry-safety key; equivalent retries return the existing durable run"`
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
	RunID            string `json:"run_id" jsonschema:"durable run identifier"`
	ItemOffset       int    `json:"item_offset,omitempty" jsonschema:"zero-based work-item offset; actionable states are not reordered in get_run"`
	ItemLimit        int    `json:"item_limit,omitempty" jsonschema:"maximum work items to return; defaults to 50 and is capped at 200"`
	CriteriaOffset   int    `json:"criteria_offset,omitempty" jsonschema:"zero-based success-criteria offset"`
	CriteriaLimit    int    `json:"criteria_limit,omitempty" jsonschema:"maximum success criteria; defaults to 20 and is capped at 100"`
	NextActionOffset int    `json:"next_action_offset,omitempty" jsonschema:"zero-based next-action offset"`
	NextActionLimit  int    `json:"next_action_limit,omitempty" jsonschema:"maximum next actions; defaults to 20 and is capped at 100"`
}

type ListRunsInput struct {
	Offset int `json:"offset,omitempty" jsonschema:"zero-based offset into runs ordered by most recently updated"`
	Limit  int `json:"limit,omitempty" jsonschema:"maximum compact run summaries; defaults to 50 and is capped at 200"`
}

type CollectWorkflowGarbageInput struct {
	RetentionSeconds int  `json:"retention_seconds,omitempty" jsonschema:"terminal-run age threshold in seconds; defaults to configured workflow.completed_retention_seconds"`
	MaxDeletes       int  `json:"max_deletes,omitempty" jsonschema:"maximum runs staged for deletion in one call; defaults to configured limit and is capped at 1000"`
	Execute          bool `json:"execute,omitempty" jsonschema:"false previews eligible cleanup; true performs restart-safe bounded deletion"`
}

type ReadRunJobLogInput struct {
	RunID     string `json:"run_id" jsonschema:"durable run identifier"`
	JobID     string `json:"job_id" jsonschema:"job identifier whose disk log should be tailed"`
	TailLines int    `json:"tail_lines,omitempty" jsonschema:"maximum tail lines to return; defaults to 100 and is capped at 1000"`
	MaxBytes  int    `json:"max_bytes,omitempty" jsonschema:"maximum UTF-8 log content bytes returned; defaults to 65536 and is capped at 262144"`
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
	Run                     workflowmgr.Run `json:"run"`
	Counts                  RunCounts       `json:"counts"`
	IdempotentReplay        bool            `json:"idempotent_replay,omitempty"`
	ItemOffset              int             `json:"item_offset"`
	ItemsReturned           int             `json:"items_returned"`
	ItemsTotal              int             `json:"items_total"`
	ItemsHasMore            bool            `json:"items_has_more"`
	CriteriaOffset          int             `json:"criteria_offset"`
	SuccessCriteriaReturned int             `json:"success_criteria_returned"`
	SuccessCriteriaTotal    int             `json:"success_criteria_total"`
	SuccessCriteriaHasMore  bool            `json:"success_criteria_has_more"`
	NextActionOffset        int             `json:"next_action_offset"`
	NextActionsReturned     int             `json:"next_actions_returned"`
	NextActionsTotal        int             `json:"next_actions_total"`
	NextActionsHasMore      bool            `json:"next_actions_has_more"`
	TruncatedFields         []string        `json:"truncated_fields,omitempty"`
}

type RunSummary struct {
	ID              string               `json:"id"`
	Title           string               `json:"title"`
	State           workflowmgr.RunState `json:"state"`
	Phase           string               `json:"phase,omitempty"`
	Revision        uint64               `json:"revision"`
	ItemCount       int                  `json:"item_count"`
	NextActionCount int                  `json:"next_action_count"`
	UpdatedAt       time.Time            `json:"updated_at"`
}

type ListRunsOutput struct {
	Runs     []RunSummary `json:"runs"`
	Offset   int          `json:"offset"`
	Returned int          `json:"returned"`
	Total    int          `json:"total"`
	HasMore  bool         `json:"has_more"`
}

type RunJobLogOutput struct {
	RunID         string   `json:"run_id"`
	JobID         string   `json:"job_id"`
	Lines         []string `json:"lines"`
	LinesReturned int      `json:"lines_returned"`
	BytesReturned int      `json:"bytes_returned"`
	MoreAvailable bool     `json:"more_available"`
	Truncated     bool     `json:"truncated"`
}

func (t *WorkflowTools) createRun(_ context.Context, in CreateRunInput) (RunView, error) {
	req := workflowmgr.CreateRequest{Title: in.Title, Objective: in.Objective, SuccessCriteria: in.SuccessCriteria}
	if strings.TrimSpace(in.IdempotencyKey) == "" {
		run, err := t.store.Create(req)
		if err != nil {
			return RunView{}, err
		}
		return viewRun(run), nil
	}
	run, replay, err := t.store.CreateIdempotent(req, in.IdempotencyKey)
	if err != nil {
		return RunView{}, err
	}
	view := viewRunPage(run, GetRunInput{})
	view.IdempotentReplay = replay
	return view, nil
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
	checkpointedAt := t.now()
	run, err := t.store.Update(in.RunID, in.ExpectedRevision, func(run *workflowmgr.Run) error {
		run.State = state
		run.Phase = strings.TrimSpace(in.Phase)
		run.LastCheckpointAt = checkpointedAt
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
	return viewRunPage(run, GetRunInput{}), nil
}

func (t *WorkflowTools) getRun(_ context.Context, in GetRunInput) (RunView, error) {
	if err := validateRunViewPage(in); err != nil {
		return RunView{}, err
	}
	run, err := t.store.Get(in.RunID)
	if err != nil {
		return RunView{}, err
	}
	return viewRunPage(run, in), nil
}

func (t *WorkflowTools) listRuns(_ context.Context, in ListRunsInput) (ListRunsOutput, error) {
	if in.Offset < 0 || in.Limit < 0 {
		return ListRunsOutput{}, errors.New("offset/limit must be >= 0")
	}
	limit := in.Limit
	if limit == 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	runs, err := t.store.List()
	if err != nil {
		return ListRunsOutput{}, err
	}
	total := len(runs)
	start := in.Offset
	if start > total {
		start = total
	}
	end := start + limit
	if end > total {
		end = total
	}
	out := ListRunsOutput{Runs: make([]RunSummary, 0, end-start), Offset: start, Total: total, HasMore: end < total}
	for _, run := range runs[start:end] {
		out.Runs = append(out.Runs, summarizeRun(run))
	}
	out.Returned = len(out.Runs)
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
	maxBytes := in.MaxBytes
	if maxBytes == 0 {
		maxBytes = 64 << 10
	}
	if maxBytes < 0 {
		return RunJobLogOutput{}, errors.New("max_bytes must be >= 0")
	}
	if maxBytes > 256<<10 {
		maxBytes = 256 << 10
	}
	// Ask for one extra line so truncation by line count is never silent.
	tail, err := t.store.ReadJobLogTail(in.RunID, in.JobID, lines+1)
	if err != nil {
		return RunJobLogOutput{}, err
	}
	more := len(tail) > lines
	if more {
		tail = tail[len(tail)-lines:]
	}
	bounded, bytesReturned, byteTruncated := boundTailBytes(tail, maxBytes)
	return RunJobLogOutput{RunID: in.RunID, JobID: in.JobID, Lines: bounded, LinesReturned: len(bounded), BytesReturned: bytesReturned, MoreAvailable: more || byteTruncated, Truncated: more || byteTruncated}, nil
}

func (t *WorkflowTools) collectGarbage(_ context.Context, in CollectWorkflowGarbageInput) (workflowmgr.GCResult, error) {
	retention := t.completedRetention
	if in.RetentionSeconds < 0 {
		return workflowmgr.GCResult{}, errors.New("retention_seconds must be >= 0")
	}
	if in.RetentionSeconds > 0 {
		retention = time.Duration(in.RetentionSeconds) * time.Second
	}
	maxDeletes := t.gcMaxDeletes
	if in.MaxDeletes < 0 {
		return workflowmgr.GCResult{}, errors.New("max_deletes must be >= 0")
	}
	if in.MaxDeletes > 0 {
		maxDeletes = in.MaxDeletes
	}
	return t.store.CollectGarbage(workflowmgr.GCPolicy{CompletedRetention: retention, MaxDeletes: maxDeletes, DryRun: !in.Execute})
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

const (
	maxRunViewItems          = 50
	maxRunViewCriteria       = 20
	maxRunViewNextActions    = 20
	maxRunViewTitleBytes     = 512
	maxRunViewObjectiveBytes = 8 << 10
	maxRunViewPhaseBytes     = 1024
	maxRunViewSummaryBytes   = 2 << 10
)

func viewRun(run workflowmgr.Run) RunView { return viewRunPage(run, GetRunInput{}) }

func validateRunViewPage(in GetRunInput) error {
	if in.ItemOffset < 0 || in.ItemLimit < 0 || in.CriteriaOffset < 0 || in.CriteriaLimit < 0 || in.NextActionOffset < 0 || in.NextActionLimit < 0 {
		return errors.New("get_run offsets/limits must be >= 0")
	}
	return nil
}

func pageRange(total, offset, requested, defaultLimit, maxLimit int) (start, end int) {
	limit := requested
	if limit == 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	start = offset
	if start > total {
		start = total
	}
	end = start + limit
	if end > total {
		end = total
	}
	return start, end
}

func viewRunPage(run workflowmgr.Run, page GetRunInput) RunView {
	counts := countRunItems(run.Items)
	bounded := run
	truncated := make([]string, 0, 8)
	bounded.Title, truncated = boundedField(run.Title, maxRunViewTitleBytes, "title", truncated)
	bounded.Objective, truncated = boundedField(run.Objective, maxRunViewObjectiveBytes, "objective", truncated)
	bounded.Phase, truncated = boundedField(run.Phase, maxRunViewPhaseBytes, "phase", truncated)

	criteriaStart, criteriaEnd := pageRange(len(run.SuccessCriteria), page.CriteriaOffset, page.CriteriaLimit, maxRunViewCriteria, 100)
	bounded.SuccessCriteria = append([]string(nil), run.SuccessCriteria[criteriaStart:criteriaEnd]...)
	for i := range bounded.SuccessCriteria {
		bounded.SuccessCriteria[i], truncated = boundedField(bounded.SuccessCriteria[i], 1024, "success_criteria", truncated)
	}

	nextStart, nextEnd := pageRange(len(run.NextActions), page.NextActionOffset, page.NextActionLimit, maxRunViewNextActions, 100)
	bounded.NextActions = append([]string(nil), run.NextActions[nextStart:nextEnd]...)
	for i := range bounded.NextActions {
		bounded.NextActions[i], truncated = boundedField(bounded.NextActions[i], 1024, "next_actions", truncated)
	}

	itemStart, itemEnd := pageRange(len(run.Items), page.ItemOffset, page.ItemLimit, maxRunViewItems, 200)
	bounded.Items = append([]workflowmgr.WorkItem(nil), run.Items[itemStart:itemEnd]...)
	for i := range bounded.Items {
		item := &bounded.Items[i]
		item.DependsOn = append([]string(nil), item.DependsOn...)
		item.Title, truncated = boundedField(item.Title, maxRunViewTitleBytes, "items.title", truncated)
		item.Summary, truncated = boundedField(item.Summary, maxRunViewSummaryBytes, "items.summary", truncated)
		item.Worktree, truncated = boundedField(item.Worktree, 1024, "items.worktree", truncated)
		item.PRURL, truncated = boundedField(item.PRURL, 1024, "items.pr_url", truncated)
		if len(item.DependsOn) > 20 {
			item.DependsOn = append([]string(nil), item.DependsOn[:20]...)
			truncated = appendUnique(truncated, "items.depends_on")
		}
	}
	return RunView{
		Run: bounded, Counts: counts,
		ItemOffset: itemStart, ItemsReturned: itemEnd - itemStart, ItemsTotal: len(run.Items), ItemsHasMore: itemEnd < len(run.Items),
		CriteriaOffset: criteriaStart, SuccessCriteriaReturned: criteriaEnd - criteriaStart, SuccessCriteriaTotal: len(run.SuccessCriteria), SuccessCriteriaHasMore: criteriaEnd < len(run.SuccessCriteria),
		NextActionOffset: nextStart, NextActionsReturned: nextEnd - nextStart, NextActionsTotal: len(run.NextActions), NextActionsHasMore: nextEnd < len(run.NextActions),
		TruncatedFields: truncated,
	}
}
func summarizeRun(run workflowmgr.Run) RunSummary {
	return RunSummary{ID: run.ID, Revision: run.Revision, Title: truncateUTF8Budget(run.Title, maxRunViewTitleBytes), State: run.State, Phase: truncateUTF8Budget(run.Phase, maxRunViewPhaseBytes), ItemCount: len(run.Items), NextActionCount: len(run.NextActions), UpdatedAt: run.UpdatedAt}
}

func countRunItems(items []workflowmgr.WorkItem) RunCounts {
	var counts RunCounts
	for _, item := range items {
		switch item.State {
		case workflowmgr.ItemPlanned:
			counts.Planned++
		case workflowmgr.ItemReady:
			counts.Ready++
		case workflowmgr.ItemRunning:
			counts.Running++
		case workflowmgr.ItemBlocked:
			counts.Blocked++
		case workflowmgr.ItemCompleted:
			counts.Completed++
		case workflowmgr.ItemFailed:
			counts.Failed++
		case workflowmgr.ItemCanceled:
			counts.Canceled++
		}
	}
	return counts
}

func boundedField(value string, maxBytes int, field string, truncated []string) (string, []string) {
	if len(value) <= maxBytes {
		return value, truncated
	}
	return truncateUTF8Budget(value, maxBytes), appendUnique(truncated, field)
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func truncateUTF8Budget(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	cut := maxBytes
	for cut > 0 && (value[cut]&0xc0) == 0x80 {
		cut--
	}
	return value[:cut] + " [truncated]"
}

func truncateTailUTF8Budget(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	start := len(value) - maxBytes
	for start < len(value) && (value[start]&0xc0) == 0x80 {
		start++
	}
	return "[tail truncated] " + value[start:]
}

func boundTailBytes(lines []string, maxBytes int) ([]string, int, bool) {
	if maxBytes <= 0 {
		return nil, 0, len(lines) > 0
	}
	out := make([]string, 0, len(lines))
	used := 0
	truncated := false
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		cost := len(line) + 1
		if used+cost > maxBytes {
			remaining := maxBytes - used
			if len(out) == 0 && remaining > 32 {
				line = truncateTailUTF8Budget(line, remaining-1)
				out = append(out, line)
				used += len(line) + 1
			}
			truncated = true
			break
		}
		out = append(out, line)
		used += cost
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, used, truncated
}
