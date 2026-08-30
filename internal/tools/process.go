package tools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/kemalnw/mcpd/internal/audit"
	processmgr "github.com/kemalnw/mcpd/internal/process"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type ProcessTools struct {
	manager *processmgr.Manager
	audit   *audit.Store
}

func RegisterProcess(server *mcp.Server, manager *processmgr.Manager, auditStore *audit.Store) {
	t := &ProcessTools{manager: manager, audit: auditStore}
	mcp.AddTool(server, tool("start_process", "Run a shell command", "Use this for shell commands, builds, tests, package managers, Git, service inspection, and other terminal work. For repository-scoped work, pass cwd instead of embedding `cd <path> &&`. For non-PTY commands, set separate_streams=true only when stdout/stderr identity matters; default merged output remains more compact. The call returns on exit, prompt, or timeout without killing a running process. Continue the same PID with read_process_output or interact_with_process.", toolHints{destructive: true, openWorld: true}), audited(auditStore, "start_process", t.start))
	mcp.AddTool(server, tool("start_process_batch", "Run independent commands in parallel", "Use this for two or more independent non-interactive shell/build/test/Git/package-manager commands. MCPD schedules jobs up to max_parallel and returns one batch_id. Prefer this over many separate start_process calls when the jobs do not depend on each other. PTY=always is rejected; interactive work stays on start_process. Continue with read_process_batch and use only_changed=true to avoid repeated output.", toolHints{destructive: true, openWorld: true}), audited(auditStore, "start_process_batch", t.startBatch))
	mcp.AddTool(server, tool("read_process_batch", "Read changed batch jobs", "Use this only with a batch_id from start_process_batch. By default use only_changed=true: the call waits until any job changes and returns only changed jobs with bounded output deltas. Batch output cursors are independent from per-PID read_process_output cursors.", toolHints{readOnly: true}), audited(auditStore, "read_process_batch", t.readBatch))
	mcp.AddTool(server, tool("cancel_process_batch", "Cancel a process batch", "Use this to cancel queued/running jobs in a batch created by start_process_batch. Running MCPD-managed process groups are terminated safely; completed jobs remain completed.", toolHints{destructive: true, idempotent: true}), audited(auditStore, "cancel_process_batch", t.cancelBatch))
	mcp.AddTool(server, tool("read_process_output", "Read command output", "Use this only for a PID returned by start_process. offset=0 waits for unread output and advances the cursor; it also observes same-line/partial updates through generation + latest_line even when no newline was emitted. Positive offsets are absolute retained ranges and negative offsets read from the tail without moving the cursor. Sessions started with separate_streams return stream-tagged records instead of duplicate merged lines.", toolHints{readOnly: true}), audited(auditStore, "read_process_output", t.readOutput))
	mcp.AddTool(server, tool("interact_with_process", "Send input to a command", "Use this only for an interactive PID returned by start_process. By default MCPD appends a newline when absent; set raw_input=true for exact bytes/control-oriented input where no newline should be added. wait_for_prompt controls whether the call waits for another prompt or process completion.", toolHints{destructive: true, openWorld: true}), audited(auditStore, "interact_with_process", t.interact))
	mcp.AddTool(server, tool("resize_process_pty", "Resize a command PTY", "Use this only for a running PTY session returned by start_process when the terminal dimensions need to change, for example before driving a TUI. rows and cols must be positive terminal cell counts. Non-PTY or exited sessions are rejected.", toolHints{destructive: true, idempotent: true}), audited(auditStore, "resize_process_pty", t.resizePTY))
	mcp.AddTool(server, tool("force_terminate", "Stop a managed command", "Use this to stop a process session created by start_process. It targets the managed process group, sends SIGINT first, and escalates to SIGKILL if needed. Prefer this over kill_process for MCPD-managed sessions because it handles the process group and cleanup correctly.", toolHints{destructive: true, idempotent: true}), audited(auditStore, "force_terminate", t.forceTerminate))
	mcp.AddTool(server, tool("list_sessions", "List MCPD command sessions", "Use this to discover PIDs and state for commands previously started through start_process, including retained completed sessions. Prefer list_processes only when you need the full operating-system process table rather than MCPD-managed sessions.", toolHints{readOnly: true, idempotent: true}), audited(auditStore, "list_sessions", t.listSessions))
	mcp.AddTool(server, tool("list_processes", "List Linux processes", "Use this to inspect the operating-system process table visible to the MCPD daemon user, including PID, CPU, memory, and command line. Prefer list_sessions when the target was started through MCPD and you need its managed session state or retained output.", toolHints{readOnly: true, idempotent: true}), audited(auditStore, "list_processes", t.listProcesses))
	mcp.AddTool(server, tool("kill_process", "Terminate a Linux process", "Use this only to send SIGTERM to an arbitrary operating-system PID that was not necessarily started by MCPD. Prefer force_terminate for PIDs from start_process. This changes VM state and can stop unrelated services if the PID is chosen incorrectly.", toolHints{destructive: true, idempotent: true}), audited(auditStore, "kill_process", t.killProcess))
}

type StartProcessInput struct {
	Command         string `json:"command" jsonschema:"shell command to execute"`
	CWD             string `json:"cwd,omitempty" jsonschema:"optional working directory for the command; prefer this over embedding cd in repository-scoped commands"`
	TimeoutMS       int    `json:"timeout_ms" jsonschema:"maximum milliseconds this tool call waits before returning control; the process keeps running after the wait expires"`
	Shell           string `json:"shell,omitempty" jsonschema:"optional shell executable; defaults to the configured shell"`
	VerboseTiming   bool   `json:"verbose_timing,omitempty" jsonschema:"accepted for Desktop Commander compatibility; timing fields are always returned in structured output"`
	PTY             string `json:"pty,omitempty" jsonschema:"mcpd extension: PTY mode auto, always, or never; defaults to auto"`
	SeparateStreams bool   `json:"separate_streams,omitempty" jsonschema:"for non-PTY commands, return stdout/stderr as stream-tagged records instead of merged lines"`
}

type BatchProcessJobInput struct {
	ID              string   `json:"id" jsonschema:"stable caller-chosen job identifier unique within the batch"`
	Command         string   `json:"command" jsonschema:"shell command to execute"`
	CWD             string   `json:"cwd,omitempty" jsonschema:"optional working directory"`
	Shell           string   `json:"shell,omitempty" jsonschema:"optional shell executable"`
	PTY             string   `json:"pty,omitempty" jsonschema:"PTY mode auto or never; interactive PTY=always jobs must use start_process"`
	SeparateStreams bool     `json:"separate_streams,omitempty" jsonschema:"for non-PTY jobs, preserve stdout/stderr identity"`
	DependsOn       []string `json:"depends_on,omitempty" jsonschema:"job ids that must complete successfully before this job becomes ready"`
	ResourceClass   string   `json:"resource_class,omitempty" jsonschema:"normal, io, cpu, or heavy; heavier jobs consume more global concurrency capacity"`
}

type StartProcessBatchInput struct {
	Jobs          []BatchProcessJobInput `json:"jobs" jsonschema:"two or more independent non-interactive jobs to schedule"`
	MaxParallel   int                    `json:"max_parallel,omitempty" jsonschema:"requested concurrency; capped by process.batch_max_parallel"`
	InitialWaitMS int                    `json:"initial_wait_ms,omitempty" jsonschema:"milliseconds to wait for the first batch state/output change; defaults to 40"`
}

type ReadProcessBatchInput struct {
	BatchID     string `json:"batch_id" jsonschema:"batch identifier returned by start_process_batch"`
	TimeoutMS   int    `json:"timeout_ms,omitempty" jsonschema:"milliseconds to wait for a batch change when only_changed is true; defaults to 5000"`
	Length      int    `json:"length,omitempty" jsonschema:"maximum output lines per returned job; defaults to 100"`
	OnlyChanged *bool  `json:"only_changed,omitempty" jsonschema:"with a cursor, return only state/output not yet observed by that cursor; without a cursor, wait from call-time baseline; defaults to true"`
	Cursor      string `json:"cursor,omitempty" jsonschema:"opaque caller-owned cursor returned by start_process_batch/read_process_batch; pass it back so independent clients do not consume each other's progress"`
}

type BatchIDInput struct {
	BatchID string `json:"batch_id" jsonschema:"batch identifier returned by start_process_batch"`
}

type ReadProcessOutputInput struct {
	PID           int  `json:"pid" jsonschema:"PID returned by start_process"`
	TimeoutMS     int  `json:"timeout_ms,omitempty" jsonschema:"milliseconds to wait for new output when offset is zero; defaults to 5000"`
	Offset        int  `json:"offset,omitempty" jsonschema:"0 reads from the per-process cursor, positive values are absolute line offsets, negative values address from the end"`
	Length        int  `json:"length,omitempty" jsonschema:"maximum number of lines to return; defaults to 1000"`
	VerboseTiming bool `json:"verbose_timing,omitempty" jsonschema:"accepted for Desktop Commander compatibility"`
}

type InteractWithProcessInput struct {
	PID           int    `json:"pid" jsonschema:"PID returned by start_process"`
	Input         string `json:"input" jsonschema:"input to send to process stdin; a newline is added when absent"`
	TimeoutMS     int    `json:"timeout_ms,omitempty" jsonschema:"maximum milliseconds to wait for a response; defaults to 8000"`
	WaitForPrompt *bool  `json:"wait_for_prompt,omitempty" jsonschema:"wait for another prompt or process completion before returning; defaults to true"`
	VerboseTiming bool   `json:"verbose_timing,omitempty" jsonschema:"accepted for Desktop Commander compatibility"`
	RawInput      bool   `json:"raw_input,omitempty" jsonschema:"send input exactly as provided without appending a newline"`
}

type ResizePTYInput struct {
	PID  int `json:"pid" jsonschema:"PID returned by start_process for a PTY session"`
	Rows int `json:"rows" jsonschema:"terminal height in rows"`
	Cols int `json:"cols" jsonschema:"terminal width in columns"`
}

type PIDInput struct {
	PID int `json:"pid" jsonschema:"operating system process ID"`
}

type EmptyInput struct{}

type TerminateOutput struct {
	PID        int    `json:"pid"`
	Terminated bool   `json:"terminated"`
	Signal     string `json:"signal"`
}

type SessionsOutput struct {
	Sessions []processmgr.SessionInfo `json:"sessions"`
}

type ProcessesOutput struct {
	Processes []processmgr.SystemProcess `json:"processes"`
}

func (t *ProcessTools) start(ctx context.Context, in StartProcessInput) (processmgr.StartResult, error) {
	return t.manager.Start(ctx, processmgr.StartRequest{Command: in.Command, CWD: in.CWD, Shell: in.Shell, TimeoutMS: in.TimeoutMS, PTY: processmgr.PTYMode(in.PTY), SeparateStreams: in.SeparateStreams})
}

func (t *ProcessTools) startBatch(ctx context.Context, in StartProcessBatchInput) (processmgr.BatchResult, error) {
	jobs := make([]processmgr.BatchJobRequest, 0, len(in.Jobs))
	for _, job := range in.Jobs {
		jobs = append(jobs, processmgr.BatchJobRequest{ID: job.ID, Command: job.Command, CWD: job.CWD, Shell: job.Shell, PTY: processmgr.PTYMode(job.PTY), SeparateStreams: job.SeparateStreams, DependsOn: job.DependsOn, ResourceClass: processmgr.ResourceClass(job.ResourceClass)})
	}
	return t.manager.StartBatch(ctx, processmgr.BatchStartRequest{Jobs: jobs, MaxParallel: in.MaxParallel, InitialWaitMS: in.InitialWaitMS})
}

func (t *ProcessTools) readBatch(ctx context.Context, in ReadProcessBatchInput) (processmgr.BatchResult, error) {
	onlyChanged := true
	if in.OnlyChanged != nil {
		onlyChanged = *in.OnlyChanged
	}
	return t.manager.ReadBatch(ctx, processmgr.BatchReadRequest{BatchID: in.BatchID, TimeoutMS: in.TimeoutMS, Length: in.Length, OnlyChanged: onlyChanged, Cursor: in.Cursor})
}

func (t *ProcessTools) cancelBatch(_ context.Context, in BatchIDInput) (processmgr.BatchCancelResult, error) {
	return t.manager.CancelBatch(in.BatchID)
}

func (t *ProcessTools) readOutput(ctx context.Context, in ReadProcessOutputInput) (processmgr.OutputResult, error) {
	return t.manager.ReadOutput(ctx, processmgr.OutputRequest{PID: in.PID, TimeoutMS: in.TimeoutMS, Offset: in.Offset, Length: in.Length})
}

func (t *ProcessTools) interact(ctx context.Context, in InteractWithProcessInput) (processmgr.InteractResult, error) {
	wait := true
	if in.WaitForPrompt != nil {
		wait = *in.WaitForPrompt
	}
	return t.manager.Interact(ctx, processmgr.InteractRequest{PID: in.PID, Input: in.Input, TimeoutMS: in.TimeoutMS, WaitForPrompt: wait, RawInput: in.RawInput})
}

func (t *ProcessTools) resizePTY(_ context.Context, in ResizePTYInput) (processmgr.PTYSizeResult, error) {
	return t.manager.ResizePTY(in.PID, in.Rows, in.Cols)
}

func (t *ProcessTools) forceTerminate(_ context.Context, in PIDInput) (TerminateOutput, error) {
	if err := t.manager.ForceTerminate(in.PID); err != nil {
		return TerminateOutput{}, err
	}
	return TerminateOutput{PID: in.PID, Terminated: true, Signal: "SIGINT→SIGKILL"}, nil
}

func (t *ProcessTools) listSessions(_ context.Context, _ EmptyInput) (SessionsOutput, error) {
	return SessionsOutput{Sessions: t.manager.ListSessions()}, nil
}

func (t *ProcessTools) listProcesses(_ context.Context, _ EmptyInput) (ProcessesOutput, error) {
	processes, err := processmgr.ListSystemProcesses()
	return ProcessesOutput{Processes: processes}, err
}

func (t *ProcessTools) killProcess(_ context.Context, in PIDInput) (TerminateOutput, error) {
	if err := processmgr.KillSystemProcess(in.PID); err != nil {
		return TerminateOutput{}, err
	}
	return TerminateOutput{PID: in.PID, Terminated: true, Signal: "SIGTERM"}, nil
}

type toolHints struct {
	readOnly    bool
	destructive bool
	idempotent  bool
	openWorld   bool
}

func tool(name, title, description string, hints toolHints) *mcp.Tool {
	return &mcp.Tool{
		Name:        name,
		Title:       title,
		Description: description,
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    hints.readOnly,
			DestructiveHint: boolPtr(hints.destructive),
			IdempotentHint:  hints.idempotent,
			OpenWorldHint:   boolPtr(hints.openWorld),
		},
	}
}

func boolPtr(v bool) *bool { return &v }

func audited[In, Out any](store *audit.Store, name string, fn func(context.Context, In) (Out, error)) mcp.ToolHandlerFor[In, Out] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		started := time.Now()
		eventID := newEventID()
		logToolCall(ctx, eventID, name, in)
		out, err := fn(ctx, in)
		durationMS := time.Since(started).Milliseconds()
		if store != nil {
			event := audit.Event{ID: eventID, Timestamp: started.UTC(), Tool: name, Arguments: auditMetadata(any(in)), DurationMS: durationMS}
			if err != nil {
				event.Error = "tool call failed"
			}
			if auditErr := store.Record(event); auditErr != nil && err == nil {
				err = fmt.Errorf("record audit event: %w", auditErr)
			}
		}
		logToolResult(ctx, eventID, name, out, durationMS, err)
		return nil, out, err
	}
}

func newEventID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("evt_%d", time.Now().UnixNano())
	}
	return "evt_" + hex.EncodeToString(b[:])
}
