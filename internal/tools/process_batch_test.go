package tools

import (
	"context"
	"testing"

	processmgr "github.com/kemalnw/mcpd/internal/process"
)

func TestProcessToolBatchDefaultsToFailureOnlyOutput(t *testing.T) {
	m, err := processmgr.NewManager(processmgr.Options{
		DefaultShell: "/bin/bash", DefaultWaitMS: 1000, InitialOutputLines: 20,
		ResponseOutputBytes: 4096, FailureTailLines: 10,
		OutputBufferBytes: 1 << 20, MaxLineBytes: 1 << 16, CompletedSessions: 10,
		BatchMaxParallel: 2, BatchGlobalParallel: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	tools := &ProcessTools{manager: m}
	out, err := tools.startBatch(context.Background(), StartProcessBatchInput{InitialWaitMS: 1000, Jobs: []BatchProcessJobInput{
		{ID: "a", Command: "printf 'noisy-success-a\\n'", PTY: "never"},
		{ID: "b", Command: "printf 'noisy-success-b\\n'", PTY: "never"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for out.State == processmgr.BatchRunning {
		cursor := out.Cursor
		out, err = tools.readBatch(context.Background(), ReadProcessBatchInput{BatchID: out.BatchID, Cursor: cursor, TimeoutMS: 5000})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, job := range out.Jobs {
		if len(job.Lines) != 0 || len(job.Streams) != 0 {
			t.Fatalf("MCP default leaked successful output: %+v", job)
		}
	}
}
