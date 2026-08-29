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
	mcp.AddTool(server, tool("start_process", "Start a terminal process", "Start a shell command and return when it exits, reaches an interactive prompt, or timeout_ms elapses. timeout_ms only limits how long this call waits; it never limits the lifetime of the spawned process. Long-running processes remain available through their PID.", false, true), audited(auditStore, "start_process", t.start))
	mcp.AddTool(server, tool("read_process_output", "Read process output", "Read retained output from a managed process. offset=0 reads new output since the previous cursor read; positive offsets are absolute line positions; negative offsets read from the tail. Cursor reads may wait up to timeout_ms for new output.", true, false), audited(auditStore, "read_process_output", t.readOutput))
	mcp.AddTool(server, tool("interact_with_process", "Interact with a process", "Send input to a managed interactive process or REPL. A trailing newline is added automatically. By default the call waits until the process reaches another prompt, exits, or timeout_ms elapses.", false, true), audited(auditStore, "interact_with_process", t.interact))
	mcp.AddTool(server, tool("force_terminate", "Force terminate a managed process", "Terminate a process session created by start_process. Sends SIGINT to the process group first, then escalates to SIGKILL if it does not exit promptly.", false, true), audited(auditStore, "force_terminate", t.forceTerminate))
	mcp.AddTool(server, tool("list_sessions", "List managed process sessions", "List active and recently completed process sessions created by start_process, including PID, state, runtime, output counters, and exit code.", true, false), audited(auditStore, "list_sessions", t.listSessions))
	mcp.AddTool(server, tool("list_processes", "List operating system processes", "List all Linux processes visible to the mcpd daemon user with PID, CPU usage, memory usage, and full command line.", true, false), audited(auditStore, "list_processes", t.listProcesses))
	mcp.AddTool(server, tool("kill_process", "Terminate an operating system process", "Send SIGTERM to an arbitrary Linux process by PID. This is not limited to processes created by mcpd and uses the permissions of the user running the daemon.", false, true), audited(auditStore, "kill_process", t.killProcess))
}

type StartProcessInput struct {
	Command       string `json:"command" jsonschema:"shell command to execute"`
	TimeoutMS     int    `json:"timeout_ms" jsonschema:"maximum milliseconds this tool call waits before returning control; the process keeps running after the wait expires"`
	Shell         string `json:"shell,omitempty" jsonschema:"optional shell executable; defaults to the configured shell"`
	VerboseTiming bool   `json:"verbose_timing,omitempty" jsonschema:"accepted for Desktop Commander compatibility; timing fields are always returned in structured output"`
	PTY           string `json:"pty,omitempty" jsonschema:"mcpd extension: PTY mode auto, always, or never; defaults to auto"`
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
	return t.manager.Start(ctx, processmgr.StartRequest{Command: in.Command, Shell: in.Shell, TimeoutMS: in.TimeoutMS, PTY: processmgr.PTYMode(in.PTY)})
}

func (t *ProcessTools) readOutput(ctx context.Context, in ReadProcessOutputInput) (processmgr.OutputResult, error) {
	return t.manager.ReadOutput(ctx, processmgr.OutputRequest{PID: in.PID, TimeoutMS: in.TimeoutMS, Offset: in.Offset, Length: in.Length})
}

func (t *ProcessTools) interact(ctx context.Context, in InteractWithProcessInput) (processmgr.InteractResult, error) {
	wait := true
	if in.WaitForPrompt != nil {
		wait = *in.WaitForPrompt
	}
	return t.manager.Interact(ctx, processmgr.InteractRequest{PID: in.PID, Input: in.Input, TimeoutMS: in.TimeoutMS, WaitForPrompt: wait})
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

func tool(name, title, description string, readOnly, destructive bool) *mcp.Tool {
	openWorld := true
	return &mcp.Tool{
		Name: name, Title: title, Description: description,
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnly, DestructiveHint: boolPtr(destructive), OpenWorldHint: &openWorld},
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
			event := audit.Event{ID: eventID, Timestamp: started.UTC(), Tool: name, Arguments: in, DurationMS: durationMS}
			if err != nil {
				event.Error = err.Error()
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
