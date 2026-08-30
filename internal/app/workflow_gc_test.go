package app

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	workflowmgr "github.com/kemalnw/mcpd/internal/workflow"
)

func TestWorkflowGarbageCollectorDeletesOldTerminalRunAndStops(t *testing.T) {
	store, err := workflowmgr.Open(filepath.Join(t.TempDir(), "runs"))
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create(workflowmgr.CreateRequest{Title: "terminal"})
	if err != nil {
		t.Fatal(err)
	}
	run, err = store.Update(run.ID, run.Revision, func(run *workflowmgr.Run) error { run.State = workflowmgr.RunCompleted; return nil })
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go runWorkflowGarbageCollector(ctx, done, store, slog.New(slog.NewTextHandler(io.Discard, nil)), 5*time.Millisecond, time.Millisecond, 10)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := store.Get(run.ID); err != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := store.Get(run.ID); err == nil {
		t.Fatal("background GC did not remove old terminal run")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("background GC did not stop after cancellation")
	}
}
