package tools

import (
	"context"
	"errors"
	"path/filepath"
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
	return &WorkflowTools{store: store, checkpointInterval: 15 * time.Minute, now: func() time.Time { return time.Now().UTC() }}
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
