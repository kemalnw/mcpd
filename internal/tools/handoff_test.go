package tools

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	workflowmgr "github.com/kemalnw/mcpd/internal/workflow"
)

func TestHandoffResumeSurvivesStoreReopen(t *testing.T) {
	root := filepath.Join(t.TempDir(), "runs")
	store, err := workflowmgr.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	tools := &WorkflowTools{store: store, checkpointInterval: 15 * time.Minute, now: func() time.Time { return now }}
	created, err := tools.createRun(context.Background(), CreateRunInput{Title: "overnight refactor", Objective: "finish all lanes", SuccessCriteria: []string{"CI green"}})
	if err != nil {
		t.Fatal(err)
	}

	checkpoint, err := tools.checkpointRun(context.Background(), CheckpointRunInput{
		RunID: created.Run.ID, ExpectedRevision: created.Run.Revision, State: "running", Phase: "ci",
		ReplaceItems: true, Items: []RunWorkItemInput{
			{ID: "a", State: "completed", PRURL: "https://example.invalid/pr/1", Summary: "merged"},
			{ID: "b", State: "running", JobID: "job-b", PID: 4242, Summary: "race test running"},
		},
		ReplaceNext: true, NextActions: []string{"read job-b delta", "merge b when green"},
	})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	handoff, err := tools.handoffRun(context.Background(), HandoffRunInput{
		RunID: checkpoint.Run.ID, ExpectedRevision: checkpoint.Run.Revision, Reason: "before_session_end",
		Summary:       "One lane merged; race test still running.",
		ActiveHandles: []HandoffActiveHandleInput{{Kind: "job", ID: "job-b", ItemID: "b"}},
		NextActions:   []string{"read job-b delta", "merge b when green"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if handoff.Handoff == nil || handoff.Handoff.Reason != workflowmgr.CheckpointBeforeSessionEnd {
		t.Fatalf("handoff missing: %+v", handoff)
	}
	if handoff.Handoff.RunRevision != handoff.Revision {
		t.Fatalf("handoff revision=%d run revision=%d", handoff.Handoff.RunRevision, handoff.Revision)
	}

	reopened, err := workflowmgr.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	fresh := &WorkflowTools{store: reopened, checkpointInterval: 15 * time.Minute, now: func() time.Time { return now.Add(2 * time.Minute) }}
	resumed, err := fresh.resumeRun(context.Background(), ResumeRunInput{RunID: created.Run.ID})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Counts.Completed != 1 || resumed.Counts.Running != 1 || resumed.CheckpointDue {
		t.Fatalf("unexpected resume summary: %+v", resumed)
	}
	if len(resumed.Items) != 2 || resumed.Items[0].ID != "b" {
		t.Fatalf("actionable item was not prioritized: %+v", resumed.Items)
	}
	if resumed.Handoff == nil || len(resumed.Handoff.ActiveHandles) != 1 || resumed.Handoff.ActiveHandles[0].ID != "job-b" {
		t.Fatalf("active handle not preserved: %+v", resumed.Handoff)
	}
}

func TestResumeCheckpointDueUsesInjectedClock(t *testing.T) {
	tools := testWorkflowTools(t)
	created, err := tools.createRun(context.Background(), CreateRunInput{Title: "run", Objective: "x"})
	if err != nil {
		t.Fatal(err)
	}
	base := created.Run.LastCheckpointAt
	tools.checkpointInterval = 15 * time.Minute
	tools.now = func() time.Time { return base.Add(14*time.Minute + 59*time.Second) }
	before, err := tools.resumeRun(context.Background(), ResumeRunInput{RunID: created.Run.ID})
	if err != nil {
		t.Fatal(err)
	}
	if before.CheckpointDue {
		t.Fatalf("checkpoint due early: %+v", before)
	}
	tools.now = func() time.Time { return base.Add(15 * time.Minute) }
	due, err := tools.resumeRun(context.Background(), ResumeRunInput{RunID: created.Run.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !due.CheckpointDue || due.CheckpointAgeSeconds != 900 || due.RecommendedCheckpointSeconds != 900 {
		t.Fatalf("checkpoint due metadata = %+v", due)
	}
}

func TestOrdinaryCheckpointResetsCheckpointAge(t *testing.T) {
	tools := testWorkflowTools(t)
	created, err := tools.createRun(context.Background(), CreateRunInput{Title: "run", Objective: "x"})
	if err != nil {
		t.Fatal(err)
	}
	checkpointAt := created.Run.CreatedAt.Add(20 * time.Minute)
	tools.now = func() time.Time { return checkpointAt }
	updated, err := tools.checkpointRun(context.Background(), CheckpointRunInput{RunID: created.Run.ID, ExpectedRevision: created.Run.Revision, State: "running"})
	if err != nil {
		t.Fatal(err)
	}
	if !updated.Run.LastCheckpointAt.Equal(checkpointAt) {
		t.Fatalf("last checkpoint=%s want %s", updated.Run.LastCheckpointAt, checkpointAt)
	}
	tools.now = func() time.Time { return checkpointAt.Add(10 * time.Minute) }
	resumed, err := tools.resumeRun(context.Background(), ResumeRunInput{RunID: created.Run.ID})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.CheckpointDue || resumed.CheckpointAgeSeconds != 600 {
		t.Fatalf("checkpoint age after ordinary checkpoint = %+v", resumed)
	}
}

func TestHandoffRejectsStaleRevisionAndInvalidReason(t *testing.T) {
	tools := testWorkflowTools(t)
	created, err := tools.createRun(context.Background(), CreateRunInput{Title: "run", Objective: "x"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := tools.handoffRun(context.Background(), HandoffRunInput{RunID: created.Run.ID, ExpectedRevision: created.Run.Revision, Reason: "manual", Summary: "safe"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tools.handoffRun(context.Background(), HandoffRunInput{RunID: created.Run.ID, ExpectedRevision: created.Run.Revision, Reason: "manual"}); !errors.Is(err, workflowmgr.ErrRevisionConflict) {
		t.Fatalf("stale handoff error=%v", err)
	}
	if _, err := tools.handoffRun(context.Background(), HandoffRunInput{RunID: created.Run.ID, ExpectedRevision: first.Revision, Reason: "two_hours_magic"}); err == nil {
		t.Fatal("invalid checkpoint reason accepted")
	}
}

func TestResumePayloadBoundsLargeRunState(t *testing.T) {
	tools := testWorkflowTools(t)
	created, err := tools.createRun(context.Background(), CreateRunInput{Title: strings.Repeat("t", 2000), Objective: strings.Repeat("o", 20000)})
	if err != nil {
		t.Fatal(err)
	}
	items := make([]RunWorkItemInput, 0, 80)
	for i := 0; i < 80; i++ {
		state := "completed"
		if i < 5 {
			state = "running"
		}
		items = append(items, RunWorkItemInput{ID: "item-" + string(rune('A'+i%26)) + strings.Repeat("x", 5), State: state, Summary: strings.Repeat("s", 10000)})
	}
	// Make IDs unique without growing the response assertions around exact values.
	for i := range items {
		items[i].ID += "-" + time.Unix(int64(i), 0).UTC().Format("150405")
	}
	updated, err := tools.checkpointRun(context.Background(), CheckpointRunInput{RunID: created.Run.ID, ExpectedRevision: created.Run.Revision, State: "running", ReplaceItems: true, Items: items})
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := tools.resumeRun(context.Background(), ResumeRunInput{RunID: updated.Run.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(resumed.Items) != maxResumeItems || resumed.ItemsOmitted != 30 {
		t.Fatalf("resume item bounds = %d items, %d omitted", len(resumed.Items), resumed.ItemsOmitted)
	}
	if len(resumed.Objective) > maxResumeObjectiveBytes+20 || len(resumed.Title) > maxResumeTitleBytes+20 {
		t.Fatalf("resume top-level strings were not bounded: title=%d objective=%d", len(resumed.Title), len(resumed.Objective))
	}
	for _, item := range resumed.Items {
		if len(item.Summary) > maxResumeSummaryBytes+20 {
			t.Fatalf("resume item summary unbounded: %d", len(item.Summary))
		}
	}
}

func TestHandoffFactsRecommendationsAndApprovalBoundary(t *testing.T) {
	tools := testWorkflowTools(t)
	created, err := tools.createRun(context.Background(), CreateRunInput{Title: "release", Objective: "ship safely"})
	if err != nil {
		t.Fatal(err)
	}
	now := created.Run.CreatedAt.Add(time.Minute)
	tools.now = func() time.Time { return now }
	resumed, err := tools.handoffRun(context.Background(), HandoffRunInput{
		RunID: created.Run.ID, ExpectedRevision: created.Run.Revision, Reason: "manual", Summary: "CI is green; deploy has not started.",
		Evidence:          []HandoffEvidenceInput{{Kind: "ci", ID: "run-123", Summary: "all required checks green", VerifiedAt: now.Format(time.RFC3339Nano)}},
		ActiveSideEffects: []string{"none"}, PendingApprovals: []string{"production deploy approval"},
		DoNotRepeat: []string{"do not recreate PR #42"}, CleanupState: []string{"temporary worktree still exists"},
		Recommendations: []HandoffRecommendationInput{{Action: "revalidate CI then request deploy approval", Source: "release policy", Confidence: "high", RevalidateAfter: now.Add(5 * time.Minute).Format(time.RFC3339Nano), RequiresApproval: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Handoff == nil || resumed.Handoff.Generation != 1 || len(resumed.Handoff.Evidence) != 1 || len(resumed.Handoff.Recommendations) != 1 {
		t.Fatalf("safe handoff facts/recommendations missing: %+v", resumed)
	}
	if resumed.PreviousAuthorityInherited {
		t.Fatal("resume incorrectly inherited previous-session authority")
	}
	if !resumed.PendingApprovalsRequireFreshApproval {
		t.Fatal("pending approval was not marked as requiring fresh approval")
	}
	if got := resumed.Handoff.DoNotRepeat; len(got) != 1 || !strings.Contains(got[0], "PR #42") {
		t.Fatalf("do_not_repeat missing: %+v", got)
	}
}

func TestBeforeWaitRequiresObservedPollAndCancellationFacts(t *testing.T) {
	tools := testWorkflowTools(t)
	created, err := tools.createRun(context.Background(), CreateRunInput{Title: "wait"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tools.handoffRun(context.Background(), HandoffRunInput{
		RunID: created.Run.ID, ExpectedRevision: created.Run.Revision, Reason: "before_wait",
		ActiveHandles: []HandoffActiveHandleInput{{Kind: "batch", ID: "batch_123"}},
	}); err == nil {
		t.Fatal("unsafe before_wait handoff without poll/cancel facts was accepted")
	}
	nextPoll := created.Run.CreatedAt.Add(time.Minute)
	resumed, err := tools.handoffRun(context.Background(), HandoffRunInput{
		RunID: created.Run.ID, ExpectedRevision: created.Run.Revision, Reason: "before_wait",
		ActiveHandles: []HandoffActiveHandleInput{{Kind: "batch", ID: "batch_123", LastObservedState: "running", NextPollAt: nextPoll.Format(time.RFC3339Nano), CancelTool: "cancel_process_batch", CancelID: "batch_123"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	h := resumed.Handoff.ActiveHandles[0]
	if h.LastObservedState != "running" || !h.NextPollAt.Equal(nextPoll) || h.CancelTool != "cancel_process_batch" {
		t.Fatalf("wait facts not persisted: %+v", h)
	}
}

func TestErrorRecoveryHandoffCannotBeOverwrittenByStalePeriodicCheckpoint(t *testing.T) {
	tools := testWorkflowTools(t)
	created, err := tools.createRun(context.Background(), CreateRunInput{Title: "recover"})
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := tools.handoffRun(context.Background(), HandoffRunInput{RunID: created.Run.ID, ExpectedRevision: created.Run.Revision, Reason: "error_recovery", Summary: "rollback required"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tools.handoffRun(context.Background(), HandoffRunInput{RunID: created.Run.ID, ExpectedRevision: created.Run.Revision, Reason: "periodic", Summary: "older periodic"}); !errors.Is(err, workflowmgr.ErrRevisionConflict) {
		t.Fatalf("stale periodic handoff overwrote error recovery: %v", err)
	}
	fresh, err := tools.resumeRun(context.Background(), ResumeRunInput{RunID: created.Run.ID})
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Revision != recovery.Revision || fresh.Handoff.Reason != workflowmgr.CheckpointErrorRecovery || fresh.Handoff.Summary != "rollback required" {
		t.Fatalf("error-recovery handoff changed unexpectedly: %+v", fresh.Handoff)
	}
}

func TestCheckpointFreshnessClassifiesMissingFutureDueAndOverdue(t *testing.T) {
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	interval := 15 * time.Minute
	cases := []struct {
		name string
		last time.Time
		want CheckpointFreshness
		due  bool
	}{
		{"missing", time.Time{}, CheckpointMissing, true},
		{"fresh", now.Add(-14 * time.Minute), CheckpointFresh, false},
		{"due", now.Add(-15 * time.Minute), CheckpointDue, true},
		{"overdue", now.Add(-31 * time.Minute), CheckpointOverdue, true},
		{"future", now.Add(time.Minute), CheckpointFuture, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, got, due := checkpointFreshness(now, tc.last, interval)
			if got != tc.want || due != tc.due {
				t.Fatalf("got freshness=%s due=%v want %s/%v", got, due, tc.want, tc.due)
			}
		})
	}
}
