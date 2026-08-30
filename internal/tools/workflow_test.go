package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	workflowmgr "github.com/kemalnw/mcpd/internal/workflow"
)

func testWorkflowTools(t *testing.T) *WorkflowTools {
	t.Helper()
	store, err := workflowmgr.Open(filepath.Join(t.TempDir(), "runs"))
	if err != nil {
		t.Fatal(err)
	}
	return &WorkflowTools{store: store, checkpointInterval: 15 * time.Minute, completedRetention: 30 * 24 * time.Hour, gcMaxDeletes: 100, now: func() time.Time { return time.Now().UTC() }}
}

func TestWorkflowCreateCheckpointResume(t *testing.T) {
	tools := testWorkflowTools(t)
	created, err := tools.createRun(context.Background(), CreateRunInput{Title: "upgrade", Objective: "finish", SuccessCriteria: []string{"CI green"}})
	if err != nil {
		t.Fatal(err)
	}
	if created.Run.Revision != 1 || created.Run.Objective != "finish" {
		t.Fatalf("created = %+v", created)
	}
	checkpoint, err := tools.checkpointRun(context.Background(), CheckpointRunInput{
		RunID: created.Run.ID, ExpectedRevision: 1, State: "running", Phase: "implementation",
		ReplaceItems: true, Items: []RunWorkItemInput{{ID: "a", State: "completed"}, {ID: "b", State: "blocked", DependsOn: []string{"a"}}},
		ReplaceNext: true, NextActions: []string{"fix b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint.Run.Revision != 2 || checkpoint.Counts.Completed != 1 || checkpoint.Counts.Blocked != 1 {
		t.Fatalf("checkpoint = %+v", checkpoint)
	}
	resumed, err := tools.getRun(context.Background(), GetRunInput{RunID: created.Run.ID})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Run.Phase != "implementation" || len(resumed.Run.NextActions) != 1 || resumed.Run.NextActions[0] != "fix b" {
		t.Fatalf("resume = %+v", resumed)
	}
	if _, err := tools.checkpointRun(context.Background(), CheckpointRunInput{RunID: created.Run.ID, ExpectedRevision: 1, State: "running"}); !errors.Is(err, workflowmgr.ErrRevisionConflict) {
		t.Fatalf("stale checkpoint error = %v", err)
	}
}

func TestWorkflowCheckpointValidatesItems(t *testing.T) {
	tools := testWorkflowTools(t)
	created, err := tools.createRun(context.Background(), CreateRunInput{Title: "run", Objective: "x"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = tools.checkpointRun(context.Background(), CheckpointRunInput{
		RunID: created.Run.ID, ExpectedRevision: 1, State: "running", ReplaceItems: true,
		Items: []RunWorkItemInput{{ID: "a", State: "ready", DependsOn: []string{"missing"}}},
	})
	if err == nil {
		t.Fatal("unknown dependency accepted")
	}
}

func TestWorkflowJobLogTailIsBounded(t *testing.T) {
	tools := testWorkflowTools(t)
	created, err := tools.createRun(context.Background(), CreateRunInput{Title: "run", Objective: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := tools.store.AppendJobLog(created.Run.ID, "test", []byte("one\ntwo\nthree\n")); err != nil {
		t.Fatal(err)
	}
	out, err := tools.readJobLog(context.Background(), ReadRunJobLogInput{RunID: created.Run.ID, JobID: "test", TailLines: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Lines) != 2 || out.Lines[0] != "two" || out.Lines[1] != "three" {
		t.Fatalf("log tail = %+v", out)
	}
}

func TestWorkflowListRunsIsMetadataOnlyAndPaginated(t *testing.T) {
	tools := testWorkflowTools(t)
	for i := 0; i < 60; i++ {
		marker := fmt.Sprintf("SECRET-OBJECTIVE-%02d", i)
		created, err := tools.createRun(context.Background(), CreateRunInput{Title: fmt.Sprintf("run-%02d", i), Objective: marker + strings.Repeat("x", 2000)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tools.checkpointRun(context.Background(), CheckpointRunInput{RunID: created.Run.ID, ExpectedRevision: created.Run.Revision, State: "running", ReplaceNext: true, NextActions: []string{"very detailed next action " + marker}}); err != nil {
			t.Fatal(err)
		}
	}
	out, err := tools.listRuns(context.Background(), ListRunsInput{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Returned != 50 || out.Total != 60 || !out.HasMore {
		t.Fatalf("list pagination=%+v", out)
	}
	data, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "SECRET-OBJECTIVE") || strings.Contains(string(data), "very detailed next action") {
		t.Fatal("metadata-only list leaked objective/next-action content")
	}
	if len(data) > 100<<10 {
		t.Fatalf("metadata list response unexpectedly large: %d bytes", len(data))
	}
	second, err := tools.listRuns(context.Background(), ListRunsInput{Offset: 50, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if second.Returned != 10 || second.HasMore || second.Offset != 50 {
		t.Fatalf("second list page=%+v", second)
	}
}

func TestWorkflowGetRunPaginatesAuthoritativeState(t *testing.T) {
	tools := testWorkflowTools(t)
	criteria := make([]string, 40)
	for i := range criteria {
		criteria[i] = fmt.Sprintf("criterion-%03d", i)
	}
	created, err := tools.createRun(context.Background(), CreateRunInput{Title: "large run", Objective: "authoritative", SuccessCriteria: criteria})
	if err != nil {
		t.Fatal(err)
	}
	items := make([]RunWorkItemInput, 120)
	for i := range items {
		items[i] = RunWorkItemInput{ID: fmt.Sprintf("item-%03d", i), Title: fmt.Sprintf("item title %03d", i), State: "planned", Summary: strings.Repeat("s", 100)}
	}
	next := make([]string, 40)
	for i := range next {
		next[i] = fmt.Sprintf("next-%03d", i)
	}
	checkpoint, err := tools.checkpointRun(context.Background(), CheckpointRunInput{RunID: created.Run.ID, ExpectedRevision: created.Run.Revision, State: "running", ReplaceItems: true, Items: items, ReplaceNext: true, NextActions: next})
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint.ItemsReturned != 50 || checkpoint.ItemsTotal != 120 || !checkpoint.ItemsHasMore {
		t.Fatalf("checkpoint default view not bounded: %+v", checkpoint)
	}
	page, err := tools.getRun(context.Background(), GetRunInput{RunID: created.Run.ID, ItemOffset: 100, ItemLimit: 30, CriteriaOffset: 20, CriteriaLimit: 20, NextActionOffset: 20, NextActionLimit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if page.ItemsReturned != 20 || page.ItemsHasMore || page.Run.Items[0].ID != "item-100" || page.ItemsTotal != 120 {
		t.Fatalf("item page=%+v", page)
	}
	if page.SuccessCriteriaReturned != 20 || page.Run.SuccessCriteria[0] != "criterion-020" || page.SuccessCriteriaHasMore {
		t.Fatalf("criteria page=%+v", page)
	}
	if page.NextActionsReturned != 20 || page.Run.NextActions[0] != "next-020" || page.NextActionsHasMore {
		t.Fatalf("next-action page=%+v", page)
	}
	stored, err := tools.store.Get(created.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Items) != 120 || len(stored.SuccessCriteria) != 40 || len(stored.NextActions) != 40 {
		t.Fatalf("response pagination mutated authoritative state: %+v", stored)
	}
}

func TestWorkflowJobLogTailIsByteBoundedWithExplicitTruncation(t *testing.T) {
	tools := testWorkflowTools(t)
	created, err := tools.createRun(context.Background(), CreateRunInput{Title: "run", Objective: "x"})
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&b, "line-%03d-%s\n", i, strings.Repeat("x", 1000))
	}
	if err := tools.store.AppendJobLog(created.Run.ID, "noisy", []byte(b.String())); err != nil {
		t.Fatal(err)
	}
	out, err := tools.readJobLog(context.Background(), ReadRunJobLogInput{RunID: created.Run.ID, JobID: "noisy", TailLines: 100, MaxBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Truncated || !out.MoreAvailable || out.BytesReturned > 4096 || out.LinesReturned >= 100 {
		t.Fatalf("byte-bounded log output=%+v", out)
	}
	if len(out.Lines) == 0 || !strings.HasPrefix(out.Lines[len(out.Lines)-1], "line-199-") {
		t.Fatalf("bounded log did not preserve newest failure evidence: last=%q", out.Lines[len(out.Lines)-1])
	}
}

func TestWorkflowRejectsOversizedAuthoritativeFields(t *testing.T) {
	tools := testWorkflowTools(t)
	if _, err := tools.createRun(context.Background(), CreateRunInput{Title: strings.Repeat("t", workflowmgr.MaxRunTitleBytes+1), Objective: "x"}); err == nil {
		t.Fatal("oversized durable title accepted")
	}
	created, err := tools.createRun(context.Background(), CreateRunInput{Title: "run", Objective: "x"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = tools.checkpointRun(context.Background(), CheckpointRunInput{RunID: created.Run.ID, ExpectedRevision: created.Run.Revision, State: "running", ReplaceItems: true, Items: []RunWorkItemInput{{ID: "a", State: "running", Summary: strings.Repeat("s", workflowmgr.MaxWorkItemSummaryBytes+1)}}})
	if err == nil {
		t.Fatal("oversized authoritative item summary accepted")
	}
}

func TestCollectWorkflowGarbageDefaultsToPreview(t *testing.T) {
	tools := testWorkflowTools(t)
	tools.completedRetention = time.Millisecond
	created, err := tools.createRun(context.Background(), CreateRunInput{Title: "old terminal"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tools.checkpointRun(context.Background(), CheckpointRunInput{RunID: created.Run.ID, ExpectedRevision: created.Run.Revision, State: "completed"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	preview, err := tools.collectGarbage(context.Background(), CollectWorkflowGarbageInput{})
	if err != nil {
		t.Fatal(err)
	}
	if !preview.DryRun || preview.EligibleRuns != 1 || preview.DeletedRuns != 0 {
		t.Fatalf("preview=%+v", preview)
	}
	if _, err := tools.store.Get(created.Run.ID); err != nil {
		t.Fatalf("preview deleted run: %v", err)
	}
	executed, err := tools.collectGarbage(context.Background(), CollectWorkflowGarbageInput{Execute: true})
	if err != nil {
		t.Fatal(err)
	}
	if executed.DryRun || executed.DeletedRuns != 1 {
		t.Fatalf("executed=%+v", executed)
	}
	if _, err := tools.store.Get(created.Run.ID); err == nil {
		t.Fatal("executed GC kept terminal run")
	}
}

func TestCollectWorkflowGarbageRejectsInvalidOverrides(t *testing.T) {
	tools := testWorkflowTools(t)
	if _, err := tools.collectGarbage(context.Background(), CollectWorkflowGarbageInput{RetentionSeconds: -1}); err == nil {
		t.Fatal("negative retention accepted")
	}
	if _, err := tools.collectGarbage(context.Background(), CollectWorkflowGarbageInput{MaxDeletes: 1001}); err == nil {
		t.Fatal("oversized delete bound accepted")
	}
}
