package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	durablemgr "github.com/kemalnw/mcpd/internal/durableexec"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type fakeTaskStore struct {
	jobs map[string]durablemgr.Job
}

func (s *fakeTaskStore) Get(id string) (durablemgr.Job, error) {
	job, ok := s.jobs[id]
	if !ok {
		return durablemgr.Job{}, os.ErrNotExist
	}
	return job, nil
}

func (s *fakeTaskStore) RequestCancel(id string) (durablemgr.Job, error) {
	job, ok := s.jobs[id]
	if !ok {
		return durablemgr.Job{}, os.ErrNotExist
	}
	job.State = durablemgr.StateCanceled
	now := time.Now().UTC()
	job.UpdatedAt = now
	job.FinishedAt = &now
	s.jobs[id] = job
	return job, nil
}

func testTaskJob(state durablemgr.State) durablemgr.Job {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	return durablemgr.Job{ID: "job_0123456789abcdefabcd", State: state, CommandSHA256: strings.Repeat("a", 64), CommandBytes: 12, Shell: "/bin/bash", StartedAt: now, UpdatedAt: now}
}

func taskRequestMeta() mcp.Meta {
	return mcp.Meta{
		mcp.MetaKeyProtocolVersion:    "2026-07-28",
		mcp.MetaKeyClientCapabilities: map[string]any{"extensions": map[string]any{TasksExtensionID: map[string]any{}}},
	}
}

func TestTasksMiddlewareRequiresPerRequestOptIn(t *testing.T) {
	job := testTaskJob(durablemgr.StateRunning)
	store := &fakeTaskStore{jobs: map[string]durablemgr.Job{job.ID: job}}
	ordinary := &mcp.CallToolResult{StructuredContent: json.RawMessage(`{"job":{"job_id":"` + job.ID + `"}}`)}
	next := func(context.Context, string, mcp.Request) (mcp.Result, error) { return ordinary, nil }
	handler := taskAugmentationMiddleware(store)(next)

	legacy := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "start_durable_job"}}
	got, err := handler(context.Background(), "tools/call", legacy)
	if err != nil {
		t.Fatal(err)
	}
	if got != ordinary {
		t.Fatalf("non-declaring client was converted to task: %#v", got)
	}

	oldProtocol := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Meta: mcp.Meta{mcp.MetaKeyProtocolVersion: "2025-11-25", mcp.MetaKeyClientCapabilities: map[string]any{"extensions": map[string]any{TasksExtensionID: map[string]any{}}}}, Name: "start_durable_job"}}
	got, err = handler(context.Background(), "tools/call", oldProtocol)
	if err != nil {
		t.Fatal(err)
	}
	if got != ordinary {
		t.Fatalf("old protocol was converted to extension task: %#v", got)
	}

	opted := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Meta: taskRequestMeta(), Name: "start_durable_job"}}
	got, err = handler(context.Background(), "tools/call", opted)
	if err != nil {
		t.Fatal(err)
	}
	created, ok := got.(*createTaskResult)
	if !ok {
		t.Fatalf("opted-in result type=%T", got)
	}
	if created.ResultType != "task" || created.TaskID != job.ID || created.Status != "working" || created.PollIntervalMS <= 0 || created.TTLMS != nil {
		t.Fatalf("created=%+v", created)
	}
}

func TestTasksCapabilityErrorUsesExtensionWireCode(t *testing.T) {
	err := requireTasksCapability(nil, mcp.Meta{mcp.MetaKeyProtocolVersion: "2026-07-28", mcp.MetaKeyClientCapabilities: map[string]any{}})
	wire, ok := err.(*jsonrpc.Error)
	if !ok {
		t.Fatalf("error type=%T", err)
	}
	if wire.Code != -32003 {
		t.Fatalf("code=%d want -32003", wire.Code)
	}
	if !strings.Contains(string(wire.Data), TasksExtensionID) {
		t.Fatalf("required capability missing from data: %s", wire.Data)
	}
}

func TestTasksMethodsAreUnavailableOnOldProtocol(t *testing.T) {
	err := requireTasksCapability(nil, mcp.Meta{mcp.MetaKeyProtocolVersion: "2025-11-25", mcp.MetaKeyClientCapabilities: map[string]any{"extensions": map[string]any{TasksExtensionID: map[string]any{}}}})
	wire, ok := err.(*jsonrpc.Error)
	if !ok || wire.Code != jsonrpc.CodeMethodNotFound {
		t.Fatalf("old protocol error=%T %+v", err, err)
	}
}

func TestDurableTaskStateMapping(t *testing.T) {
	completed := testTaskJob(durablemgr.StateCompleted)
	zero := 0
	completed.ExitCode = &zero
	finished := completed.UpdatedAt.Add(time.Second)
	completed.FinishedAt = &finished
	out := detailedTask(completed)
	if out.Status != "completed" || out.Result == nil || out.Result.IsError || out.Error != nil {
		t.Fatalf("completed=%+v", out)
	}

	failedCommand := testTaskJob(durablemgr.StateFailed)
	exit := 7
	failedCommand.ExitCode = &exit
	failedCommand.FinishedAt = &finished
	out = detailedTask(failedCommand)
	if out.Status != "completed" || out.Result == nil || !out.Result.IsError || out.Error != nil {
		t.Fatalf("command failure must be completed tool error: %+v", out)
	}

	orphan := testTaskJob(durablemgr.StateOrphaned)
	orphan.Reason = "process identity mismatch"
	out = detailedTask(orphan)
	if out.Status != "failed" || out.Error == nil || out.Result != nil {
		t.Fatalf("orphan=%+v", out)
	}
}

func TestTasksWireProgressiveEnhancementAndPolling(t *testing.T) {
	job := testTaskJob(durablemgr.StateRunning)
	store := &fakeTaskStore{jobs: map[string]durablemgr.Job{job.ID: job}}
	caps := &mcp.ServerCapabilities{}
	caps.AddExtension(TasksExtensionID, nil)
	server := mcp.NewServer(&mcp.Implementation{Name: "tasks-test", Version: "dev"}, &mcp.ServerOptions{Capabilities: caps})
	mcp.AddTool(server, &mcp.Tool{Name: "start_durable_job"}, func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, map[string]any, error) {
		return nil, map[string]any{"job": map[string]any{"job_id": job.ID}}, nil
	})
	if err := RegisterTasksExtension(server, store); err != nil {
		t.Fatal(err)
	}
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, PropagateRequestCancellation: true})

	post := func(body, method, name string) map[string]any {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Mcp-Protocol-Version", "2026-07-28")
		if method != "" {
			req.Header.Set("Mcp-Method", method)
		}
		if name != "" {
			req.Header.Set("Mcp-Name", name)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		var envelope map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("decode %s: %v", rec.Body.String(), err)
		}
		return envelope
	}

	discovery := post(`{"jsonrpc":"2.0","id":0,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"test","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`, "server/discover", "")
	discoveryResult := discovery["result"].(map[string]any)
	capabilities := discoveryResult["capabilities"].(map[string]any)
	extensions := capabilities["extensions"].(map[string]any)
	if _, ok := extensions[TasksExtensionID]; !ok {
		t.Fatalf("server/discover did not advertise tasks: %#v", discoveryResult)
	}

	legacy := post(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"start_durable_job","arguments":{},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"test","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`, "tools/call", "start_durable_job")
	legacyResult := legacy["result"].(map[string]any)
	if _, ok := legacyResult["taskId"]; ok {
		t.Fatalf("legacy client received task: %#v", legacyResult)
	}
	if _, ok := legacyResult["structuredContent"]; !ok {
		t.Fatalf("legacy result lost ordinary tool output: %#v", legacyResult)
	}

	opted := post(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"start_durable_job","arguments":{},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"test","version":"1"},"io.modelcontextprotocol/clientCapabilities":{"extensions":{"io.modelcontextprotocol/tasks":{}}}}}}`, "tools/call", "start_durable_job")
	optedResult := opted["result"].(map[string]any)
	if optedResult["resultType"] != "task" || optedResult["taskId"] != job.ID {
		t.Fatalf("task result=%#v", optedResult)
	}

	polled := post(`{"jsonrpc":"2.0","id":3,"method":"tasks/get","params":{"taskId":"`+job.ID+`","_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"test","version":"1"},"io.modelcontextprotocol/clientCapabilities":{"extensions":{"io.modelcontextprotocol/tasks":{}}}}}}`, "tasks/get", job.ID)
	pollResult := polled["result"].(map[string]any)
	if pollResult["status"] != "working" || pollResult["taskId"] != job.ID {
		t.Fatalf("poll result=%#v", pollResult)
	}

	updated := post(`{"jsonrpc":"2.0","id":31,"method":"tasks/update","params":{"taskId":"`+job.ID+`","inputResponses":{"unknown":{"action":"accept"}},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"test","version":"1"},"io.modelcontextprotocol/clientCapabilities":{"extensions":{"io.modelcontextprotocol/tasks":{}}}}}}`, "tasks/update", job.ID)
	if result := updated["result"].(map[string]any); result["resultType"] != "complete" {
		t.Fatalf("update result=%#v", result)
	}

	cancelled := post(`{"jsonrpc":"2.0","id":32,"method":"tasks/cancel","params":{"taskId":"`+job.ID+`","_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"test","version":"1"},"io.modelcontextprotocol/clientCapabilities":{"extensions":{"io.modelcontextprotocol/tasks":{}}}}}}`, "tasks/cancel", job.ID)
	if result := cancelled["result"].(map[string]any); result["resultType"] != "complete" {
		t.Fatalf("cancel result=%#v", result)
	}
	postCancel := post(`{"jsonrpc":"2.0","id":33,"method":"tasks/get","params":{"taskId":"`+job.ID+`","_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"test","version":"1"},"io.modelcontextprotocol/clientCapabilities":{"extensions":{"io.modelcontextprotocol/tasks":{}}}}}}`, "tasks/get", job.ID)
	if result := postCancel["result"].(map[string]any); result["status"] != "cancelled" {
		t.Fatalf("post-cancel=%#v", result)
	}

	missing := post(`{"jsonrpc":"2.0","id":4,"method":"tasks/get","params":{"taskId":"`+job.ID+`","_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"test","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`, "tasks/get", job.ID)
	errObj := missing["error"].(map[string]any)
	if int(errObj["code"].(float64)) != -32003 {
		t.Fatalf("missing capability error=%#v", errObj)
	}
}
