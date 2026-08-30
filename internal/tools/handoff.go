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

const (
	maxResumeItems          = 50
	maxResumeCriteria       = 20
	maxResumeDependencies   = 20
	maxResumeTitleBytes     = 512
	maxResumeObjectiveBytes = 8 << 10
	maxResumePhaseBytes     = 1024
	maxResumeSummaryBytes   = 2 << 10
)

func registerHandoffTools(server *mcp.Server, t *WorkflowTools, auditStore *audit.Store) {
	mcp.AddTool(server, tool("handoff_run", "Checkpoint an AI-session handoff", "Use this before a long wait, periodically during multi-hour work, before an anticipated agent/session/turn limit, or during error recovery. It stores a compact durable handoff with active handles, blockers, and ordered next actions. Do not paste full command logs; reference durable handles instead.", toolHints{destructive: true}), audited(auditStore, "handoff_run", t.handoffRun))
	mcp.AddTool(server, tool("resume_run", "Resume from a compact AI handoff", "Use this first in a fresh agent/session when a durable run_id is known. It returns a bounded context-efficient summary, progress counts, actionable work items, last handoff, checkpoint age/due metadata, and next actions without replaying chat history or full logs.", toolHints{readOnly: true, idempotent: true}), audited(auditStore, "resume_run", t.resumeRun))
}

type HandoffActiveHandleInput struct {
	Kind              string `json:"kind" jsonschema:"handle kind such as process, batch, job, pr, worktree, search, or other"`
	ID                string `json:"id" jsonschema:"durable or reconnectable handle identifier; never put credentials or tokens here"`
	ItemID            string `json:"item_id,omitempty" jsonschema:"associated work-item id when applicable"`
	LastObservedState string `json:"last_observed_state,omitempty" jsonschema:"last factually observed external state, especially before a long wait"`
	NextPollAt        string `json:"next_poll_at,omitempty" jsonschema:"RFC3339 earliest useful next poll time when waiting on this handle"`
	Deadline          string `json:"deadline,omitempty" jsonschema:"RFC3339 timeout/deadline when known"`
	CancelTool        string `json:"cancel_tool,omitempty" jsonschema:"MCPD cancellation tool/action, or none when no safe cancellation exists"`
	CancelID          string `json:"cancel_id,omitempty" jsonschema:"handle to pass to the cancellation action when applicable"`
}

type HandoffEvidenceInput struct {
	Kind       string `json:"kind" jsonschema:"evidence kind such as test, ci, commit, pr, log, file, or other"`
	ID         string `json:"id" jsonschema:"durable evidence handle/reference; do not inline the evidence payload"`
	Summary    string `json:"summary,omitempty" jsonschema:"compact factual evidence summary"`
	VerifiedAt string `json:"verified_at,omitempty" jsonschema:"RFC3339 time this evidence was last verified"`
}

type HandoffRecommendationInput struct {
	Action           string `json:"action" jsonschema:"advisory next action; facts belong in evidence/handles/state instead"`
	Source           string `json:"source,omitempty" jsonschema:"why/who produced this recommendation, for example agent, runbook, CI policy"`
	Confidence       string `json:"confidence,omitempty" jsonschema:"high, medium, or low"`
	RevalidateAfter  string `json:"revalidate_after,omitempty" jsonschema:"RFC3339 time after which a fresh agent must revalidate this recommendation"`
	RequiresApproval bool   `json:"requires_approval,omitempty" jsonschema:"true when a fresh current-session approval is required before acting"`
}

type HandoffRunInput struct {
	RunID             string                       `json:"run_id" jsonschema:"durable run identifier"`
	ExpectedRevision  uint64                       `json:"expected_revision" jsonschema:"current run revision; stale handoffs are rejected"`
	Reason            string                       `json:"reason" jsonschema:"periodic, before_wait, before_session_end, manual, or error_recovery"`
	Summary           string                       `json:"summary,omitempty" jsonschema:"compact factual handoff summary; never include credentials, tokens, or full logs"`
	Blockers          []string                     `json:"blockers,omitempty" jsonschema:"current factual blockers, at most 20 compact items"`
	Evidence          []HandoffEvidenceInput       `json:"evidence,omitempty" jsonschema:"verified durable evidence references; keep large content behind handles"`
	ActiveHandles     []HandoffActiveHandleInput   `json:"active_handles,omitempty" jsonschema:"active/reconnectable handles plus wait/poll/cancellation facts"`
	ActiveSideEffects []string                     `json:"active_side_effects,omitempty" jsonschema:"side effects already in progress or already initiated"`
	PendingApprovals  []string                     `json:"pending_approvals,omitempty" jsonschema:"descriptions only; approval authority never transfers to a fresh session"`
	DoNotRepeat       []string                     `json:"do_not_repeat,omitempty" jsonschema:"operations a resumed agent must verify rather than blindly repeat"`
	CleanupState      []string                     `json:"cleanup_state,omitempty" jsonschema:"cleanup or rollback facts still relevant to recovery"`
	Recommendations   []HandoffRecommendationInput `json:"recommendations,omitempty" jsonschema:"advisory actions with provenance/revalidation metadata"`
	NextActions       []string                     `json:"next_actions,omitempty" jsonschema:"compatibility advisory actions; prefer structured recommendations"`
}

type ResumeRunInput struct {
	RunID string `json:"run_id" jsonschema:"durable run identifier"`
}

type ResumeWorkItem struct {
	ID        string                `json:"id"`
	Title     string                `json:"title,omitempty"`
	State     workflowmgr.ItemState `json:"state"`
	DependsOn []string              `json:"depends_on,omitempty"`
	JobID     string                `json:"job_id,omitempty"`
	PID       int                   `json:"pid,omitempty"`
	Branch    string                `json:"branch,omitempty"`
	Worktree  string                `json:"worktree,omitempty"`
	PRURL     string                `json:"pr_url,omitempty"`
	Summary   string                `json:"summary,omitempty"`
}

type CheckpointFreshness string

const (
	CheckpointMissing CheckpointFreshness = "missing"
	CheckpointFresh   CheckpointFreshness = "fresh"
	CheckpointDue     CheckpointFreshness = "due"
	CheckpointOverdue CheckpointFreshness = "overdue"
	CheckpointFuture  CheckpointFreshness = "future_clock_skew"
)

type ResumeRunOutput struct {
	RunID                                string                         `json:"run_id"`
	Revision                             uint64                         `json:"revision"`
	Title                                string                         `json:"title"`
	Objective                            string                         `json:"objective,omitempty"`
	SuccessCriteria                      []string                       `json:"success_criteria,omitempty"`
	State                                workflowmgr.RunState           `json:"state"`
	Phase                                string                         `json:"phase,omitempty"`
	Counts                               RunCounts                      `json:"counts"`
	Items                                []ResumeWorkItem               `json:"items,omitempty"`
	ItemsOmitted                         int                            `json:"items_omitted"`
	NextActions                          []string                       `json:"next_actions,omitempty"`
	Handoff                              *workflowmgr.HandoffCheckpoint `json:"handoff,omitempty"`
	LastCheckpointAt                     time.Time                      `json:"last_checkpoint_at"`
	CheckpointAgeSeconds                 int64                          `json:"checkpoint_age_seconds"`
	CheckpointFreshness                  CheckpointFreshness            `json:"checkpoint_freshness"`
	CheckpointDue                        bool                           `json:"checkpoint_due"`
	PreviousAuthorityInherited           bool                           `json:"previous_authority_inherited"`
	PendingApprovalsRequireFreshApproval bool                           `json:"pending_approvals_require_fresh_approval"`
	RecommendedCheckpointSeconds         int64                          `json:"recommended_checkpoint_seconds"`
}

func (t *WorkflowTools) handoffRun(_ context.Context, in HandoffRunInput) (ResumeRunOutput, error) {
	reason, err := parseCheckpointReason(in.Reason)
	if err != nil {
		return ResumeRunOutput{}, err
	}
	checkpointedAt := t.now()
	handles := make([]workflowmgr.ActiveHandle, 0, len(in.ActiveHandles))
	for _, handle := range in.ActiveHandles {
		nextPollAt, err := parseOptionalRFC3339(handle.NextPollAt, "next_poll_at")
		if err != nil {
			return ResumeRunOutput{}, err
		}
		deadline, err := parseOptionalRFC3339(handle.Deadline, "deadline")
		if err != nil {
			return ResumeRunOutput{}, err
		}
		handles = append(handles, workflowmgr.ActiveHandle{
			Kind: strings.TrimSpace(handle.Kind), ID: strings.TrimSpace(handle.ID), ItemID: strings.TrimSpace(handle.ItemID),
			LastObservedState: strings.TrimSpace(handle.LastObservedState), NextPollAt: nextPollAt, Deadline: deadline,
			CancelTool: strings.TrimSpace(handle.CancelTool), CancelID: strings.TrimSpace(handle.CancelID),
		})
	}
	if reason == workflowmgr.CheckpointBeforeWait && !hasSafeWaitHandle(handles) {
		return ResumeRunOutput{}, errors.New("before_wait handoff requires an active handle with last_observed_state, next_poll_at, and cancellation path")
	}
	evidence := make([]workflowmgr.EvidenceReference, 0, len(in.Evidence))
	for _, item := range in.Evidence {
		verifiedAt, err := parseOptionalRFC3339(item.VerifiedAt, "verified_at")
		if err != nil {
			return ResumeRunOutput{}, err
		}
		evidence = append(evidence, workflowmgr.EvidenceReference{Kind: strings.TrimSpace(item.Kind), ID: strings.TrimSpace(item.ID), Summary: strings.TrimSpace(item.Summary), VerifiedAt: verifiedAt})
	}
	recommendations := make([]workflowmgr.Recommendation, 0, len(in.Recommendations))
	for _, item := range in.Recommendations {
		revalidateAfter, err := parseOptionalRFC3339(item.RevalidateAfter, "revalidate_after")
		if err != nil {
			return ResumeRunOutput{}, err
		}
		recommendations = append(recommendations, workflowmgr.Recommendation{
			Action: strings.TrimSpace(item.Action), Source: strings.TrimSpace(item.Source), Confidence: strings.TrimSpace(item.Confidence),
			RevalidateAfter: revalidateAfter, RequiresApproval: item.RequiresApproval,
		})
	}
	run, err := t.store.Update(in.RunID, in.ExpectedRevision, func(run *workflowmgr.Run) error {
		generation := uint64(1)
		if run.Handoff != nil {
			generation = run.Handoff.Generation + 1
		}
		run.LastCheckpointAt = checkpointedAt
		run.Handoff = &workflowmgr.HandoffCheckpoint{
			Generation: generation, Reason: reason, Summary: strings.TrimSpace(in.Summary), Blockers: compactStrings(in.Blockers),
			Evidence: evidence, ActiveHandles: handles, ActiveSideEffects: compactStrings(in.ActiveSideEffects),
			PendingApprovals: compactStrings(in.PendingApprovals), DoNotRepeat: compactStrings(in.DoNotRepeat), CleanupState: compactStrings(in.CleanupState),
			Recommendations: recommendations, NextActions: compactStrings(in.NextActions), CheckpointedAt: checkpointedAt, RunRevision: run.Revision + 1,
		}
		if len(run.Handoff.NextActions) > 0 {
			run.NextActions = append([]string(nil), run.Handoff.NextActions...)
		} else if len(run.Handoff.Recommendations) > 0 {
			run.NextActions = run.NextActions[:0]
			for _, rec := range run.Handoff.Recommendations {
				run.NextActions = append(run.NextActions, rec.Action)
			}
		}
		return workflowmgr.ValidateRun(*run)
	})
	if err != nil {
		return ResumeRunOutput{}, err
	}
	return t.resumeView(run), nil
}

func parseOptionalRFC3339(raw, field string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, errors.New(field + " must be RFC3339")
	}
	return parsed.UTC(), nil
}

func hasSafeWaitHandle(handles []workflowmgr.ActiveHandle) bool {
	for _, handle := range handles {
		if handle.LastObservedState != "" && !handle.NextPollAt.IsZero() && handle.CancelTool != "" {
			if handle.CancelTool == "none" || handle.CancelID != "" {
				return true
			}
		}
	}
	return false
}

func (t *WorkflowTools) resumeRun(_ context.Context, in ResumeRunInput) (ResumeRunOutput, error) {
	run, err := t.store.Get(in.RunID)
	if err != nil {
		return ResumeRunOutput{}, err
	}
	return t.resumeView(run), nil
}

func (t *WorkflowTools) resumeView(run workflowmgr.Run) ResumeRunOutput {
	now := t.now()
	last := run.LastCheckpointAt
	interval := t.checkpointInterval
	if interval <= 0 {
		interval = 15 * time.Minute
	}
	ageSeconds, freshness, due := checkpointFreshness(now, last, interval)
	view := viewRun(run)
	items, omitted := boundedResumeItems(run.Items)
	next := boundedStrings(run.NextActions, workflowmgrMaxListItems(), 1024)
	if run.Handoff != nil && len(run.Handoff.NextActions) > 0 {
		next = boundedStrings(run.Handoff.NextActions, workflowmgrMaxListItems(), 1024)
	}
	return ResumeRunOutput{
		RunID:                                run.ID,
		Revision:                             run.Revision,
		Title:                                truncateUTF8(run.Title, maxResumeTitleBytes),
		Objective:                            truncateUTF8(run.Objective, maxResumeObjectiveBytes),
		SuccessCriteria:                      boundedStrings(run.SuccessCriteria, maxResumeCriteria, 1024),
		State:                                run.State,
		Phase:                                truncateUTF8(run.Phase, maxResumePhaseBytes),
		Counts:                               view.Counts,
		Items:                                items,
		ItemsOmitted:                         omitted,
		NextActions:                          next,
		Handoff:                              cloneHandoffForResume(run.Handoff),
		LastCheckpointAt:                     last,
		CheckpointAgeSeconds:                 ageSeconds,
		CheckpointFreshness:                  freshness,
		CheckpointDue:                        due,
		PreviousAuthorityInherited:           false,
		PendingApprovalsRequireFreshApproval: run.Handoff != nil && len(run.Handoff.PendingApprovals) > 0,
		RecommendedCheckpointSeconds:         int64(interval / time.Second),
	}
}

func checkpointFreshness(now, last time.Time, interval time.Duration) (int64, CheckpointFreshness, bool) {
	if last.IsZero() {
		return -1, CheckpointMissing, true
	}
	if last.After(now.Add(5 * time.Second)) {
		return 0, CheckpointFuture, true
	}
	age := now.Sub(last)
	if age < 0 {
		age = 0
	}
	switch {
	case age >= 2*interval:
		return int64(age / time.Second), CheckpointOverdue, true
	case age >= interval:
		return int64(age / time.Second), CheckpointDue, true
	default:
		return int64(age / time.Second), CheckpointFresh, false
	}
}

func parseCheckpointReason(raw string) (workflowmgr.CheckpointReason, error) {
	reason := workflowmgr.CheckpointReason(strings.TrimSpace(raw))
	switch reason {
	case workflowmgr.CheckpointPeriodic, workflowmgr.CheckpointBeforeWait, workflowmgr.CheckpointBeforeSessionEnd, workflowmgr.CheckpointManual, workflowmgr.CheckpointErrorRecovery:
		return reason, nil
	default:
		return "", errors.New("invalid checkpoint reason")
	}
}

func boundedResumeItems(items []workflowmgr.WorkItem) ([]ResumeWorkItem, int) {
	ordered := make([]workflowmgr.WorkItem, 0, len(items))
	for _, state := range []workflowmgr.ItemState{workflowmgr.ItemRunning, workflowmgr.ItemBlocked, workflowmgr.ItemFailed, workflowmgr.ItemReady, workflowmgr.ItemPlanned, workflowmgr.ItemCompleted, workflowmgr.ItemCanceled} {
		for _, item := range items {
			if item.State == state {
				ordered = append(ordered, item)
			}
		}
	}
	limit := len(ordered)
	if limit > maxResumeItems {
		limit = maxResumeItems
	}
	out := make([]ResumeWorkItem, 0, limit)
	for _, item := range ordered[:limit] {
		out = append(out, ResumeWorkItem{
			ID:        truncateUTF8(item.ID, 128),
			Title:     truncateUTF8(item.Title, maxResumeTitleBytes),
			State:     item.State,
			DependsOn: boundedStrings(item.DependsOn, maxResumeDependencies, 128),
			JobID:     truncateUTF8(item.JobID, 512),
			PID:       item.PID,
			Branch:    truncateUTF8(item.Branch, 512),
			Worktree:  truncateUTF8(item.Worktree, 1024),
			PRURL:     truncateUTF8(item.PRURL, 1024),
			Summary:   truncateUTF8(item.Summary, maxResumeSummaryBytes),
		})
	}
	return out, len(ordered) - limit
}

func boundedStrings(values []string, maxItems, maxBytes int) []string {
	if len(values) > maxItems {
		values = values[:maxItems]
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, truncateUTF8(value, maxBytes))
		}
	}
	return out
}

func truncateUTF8(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	cut := maxBytes
	for cut > 0 && (value[cut]&0xc0) == 0x80 {
		cut--
	}
	return value[:cut] + " [truncated]"
}

func cloneHandoffForResume(in *workflowmgr.HandoffCheckpoint) *workflowmgr.HandoffCheckpoint {
	if in == nil {
		return nil
	}
	out := *in
	out.Blockers = boundedStrings(in.Blockers, workflowmgrMaxListItems(), 1024)
	out.ActiveSideEffects = boundedStrings(in.ActiveSideEffects, workflowmgrMaxListItems(), 1024)
	out.PendingApprovals = boundedStrings(in.PendingApprovals, workflowmgrMaxListItems(), 1024)
	out.DoNotRepeat = boundedStrings(in.DoNotRepeat, workflowmgrMaxListItems(), 1024)
	out.CleanupState = boundedStrings(in.CleanupState, workflowmgrMaxListItems(), 1024)
	out.NextActions = boundedStrings(in.NextActions, workflowmgrMaxListItems(), 1024)
	out.Evidence = append([]workflowmgr.EvidenceReference(nil), in.Evidence...)
	if len(out.Evidence) > 50 {
		out.Evidence = out.Evidence[:50]
	}
	for i := range out.Evidence {
		out.Evidence[i].Summary = truncateUTF8(out.Evidence[i].Summary, 1024)
	}
	out.Recommendations = append([]workflowmgr.Recommendation(nil), in.Recommendations...)
	if len(out.Recommendations) > 20 {
		out.Recommendations = out.Recommendations[:20]
	}
	for i := range out.Recommendations {
		out.Recommendations[i].Action = truncateUTF8(out.Recommendations[i].Action, 1024)
		out.Recommendations[i].Source = truncateUTF8(out.Recommendations[i].Source, 512)
	}
	if len(in.ActiveHandles) > 50 {
		out.ActiveHandles = append([]workflowmgr.ActiveHandle(nil), in.ActiveHandles[:50]...)
	} else {
		out.ActiveHandles = append([]workflowmgr.ActiveHandle(nil), in.ActiveHandles...)
	}
	out.Summary = truncateUTF8(out.Summary, 8<<10)
	return &out
}

func workflowmgrMaxListItems() int { return 20 }
