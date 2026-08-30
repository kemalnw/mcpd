package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	durablemgr "github.com/kemalnw/mcpd/internal/durableexec"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	tasksMissingCapabilityCode int64 = -32003
	TasksExtensionID                 = "io.modelcontextprotocol/tasks"
	tasksMinProtocolVersion          = "2026-07-28"
	taskPollIntervalMS               = int64(1000)
	taskTTLMS                        = int64(30 * 24 * 60 * 60 * 1000)
)

type durableTaskStore interface {
	Get(string) (durablemgr.Job, error)
	RequestCancel(string) (durablemgr.Job, error)
}

type taskParams struct {
	mcp.ParamsBase
	TaskID string `json:"taskId"`
}

type taskUpdateParams struct {
	mcp.ParamsBase
	TaskID         string         `json:"taskId"`
	InputResponses map[string]any `json:"inputResponses,omitempty"`
}

type taskBase struct {
	TaskID         string `json:"taskId"`
	Status         string `json:"status"`
	StatusMessage  string `json:"statusMessage,omitempty"`
	CreatedAt      string `json:"createdAt"`
	LastUpdatedAt  string `json:"lastUpdatedAt"`
	TTLMS          *int64 `json:"ttlMs"`
	PollIntervalMS int64  `json:"pollIntervalMs,omitempty"`
}

type createTaskResult struct {
	mcp.ResultBase
	ResultType string `json:"resultType"`
	taskBase
}

type getTaskResult struct {
	mcp.ResultBase
	ResultType string `json:"resultType"`
	taskBase
	Result *taskCallToolResult `json:"result,omitempty"`
	Error  *taskJSONRPCError   `json:"error,omitempty"`
}

type taskAckResult struct {
	mcp.ResultBase
	ResultType string `json:"resultType"`
}

type taskJSONRPCError struct {
	Code    int64  `json:"code"`
	Message string `json:"message"`
}

type taskCallToolResult struct {
	ResultType        string        `json:"resultType"`
	Content           []mcp.Content `json:"content"`
	StructuredContent any           `json:"structuredContent,omitempty"`
	IsError           bool          `json:"isError,omitempty"`
}

// RegisterTasksExtension progressively augments start_durable_job for clients
// that declare io.modelcontextprotocol/tasks on the same tools/call request.
// Legacy/non-declaring clients keep the ordinary synchronous tool result.
func RegisterTasksExtension(server *mcp.Server, store durableTaskStore) error {
	server.AddReceivingMiddleware(taskAugmentationMiddleware(store))
	if err := mcp.AddReceivingCustomMethod(server, "tasks/get", func(ctx context.Context, ss *mcp.ServerSession, params *taskParams) (*getTaskResult, error) {
		if err := requireTasksCapability(ss, params.GetMeta()); err != nil {
			return nil, err
		}
		job, err := store.Get(strings.TrimSpace(params.TaskID))
		if err != nil {
			return nil, invalidTaskError("retrieve", err)
		}
		result := detailedTask(job)
		return &result, nil
	}); err != nil {
		return err
	}
	if err := mcp.AddReceivingCustomMethod(server, "tasks/update", func(ctx context.Context, ss *mcp.ServerSession, params *taskUpdateParams) (*taskAckResult, error) {
		if err := requireTasksCapability(ss, params.GetMeta()); err != nil {
			return nil, err
		}
		if _, err := store.Get(strings.TrimSpace(params.TaskID)); err != nil {
			return nil, invalidTaskError("update", err)
		}
		// Durable shell jobs are intentionally non-interactive, so they never
		// enter input_required. Per SEP-2663, responses for unknown/already-
		// satisfied input request keys are ignored.
		return &taskAckResult{ResultType: "complete"}, nil
	}); err != nil {
		return err
	}
	if err := mcp.AddReceivingCustomMethod(server, "tasks/cancel", func(ctx context.Context, ss *mcp.ServerSession, params *taskParams) (*taskAckResult, error) {
		if err := requireTasksCapability(ss, params.GetMeta()); err != nil {
			return nil, err
		}
		if _, err := store.RequestCancel(strings.TrimSpace(params.TaskID)); err != nil {
			return nil, invalidTaskError("cancel", err)
		}
		return &taskAckResult{ResultType: "complete"}, nil
	}); err != nil {
		return err
	}
	return nil
}

func taskAugmentationMiddleware(store durableTaskStore) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method != "tools/call" {
				return next(ctx, method, req)
			}
			call, ok := req.(*mcp.CallToolRequest)
			if !ok || call.Params == nil || call.Params.Name != "start_durable_job" || !requestSupportsTasks(call) {
				return next(ctx, method, req)
			}
			res, err := next(ctx, method, req)
			if err != nil {
				return nil, err
			}
			toolResult, ok := res.(*mcp.CallToolResult)
			if !ok || toolResult == nil || toolResult.IsError {
				return res, nil
			}
			jobID, err := durableJobIDFromToolResult(toolResult)
			if err != nil {
				return nil, err
			}
			job, err := store.Get(jobID)
			if err != nil {
				return nil, fmt.Errorf("resolve durably-created task %s: %w", jobID, err)
			}
			created := createTask(job)
			return &created, nil
		}
	}
}

func requestSupportsTasks(req *mcp.CallToolRequest) bool {
	if req == nil || req.ProtocolVersion() < tasksMinProtocolVersion {
		return false
	}
	caps := req.ClientCapabilities()
	if caps == nil || caps.Extensions == nil {
		return false
	}
	_, ok := caps.Extensions[TasksExtensionID]
	return ok
}

func requireTasksCapability(session *mcp.ServerSession, meta map[string]any) error {
	version := ""
	if meta != nil {
		version, _ = meta[mcp.MetaKeyProtocolVersion].(string)
	}
	if version == "" && session != nil {
		if init := session.InitializeParams(); init != nil {
			version = init.ProtocolVersion
		}
	}
	if version < tasksMinProtocolVersion {
		return &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "Method not found"}
	}
	if taskCapabilityFromMeta(meta) {
		return nil
	}
	required := &mcp.ClientCapabilities{}
	required.AddExtension(TasksExtensionID, nil)
	data, _ := json.Marshal(mcp.MissingRequiredClientCapabilityData{RequiredCapabilities: required})
	return &jsonrpc.Error{Code: tasksMissingCapabilityCode, Message: "Missing required client capability", Data: data}
}

func taskCapabilityFromMeta(meta map[string]any) bool {
	if meta == nil {
		return false
	}
	version, _ := meta[mcp.MetaKeyProtocolVersion].(string)
	if version < tasksMinProtocolVersion {
		return false
	}
	raw, ok := meta[mcp.MetaKeyClientCapabilities]
	if !ok || raw == nil {
		return false
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return false
	}
	var caps mcp.ClientCapabilities
	if err := json.Unmarshal(data, &caps); err != nil {
		return false
	}
	_, ok = caps.Extensions[TasksExtensionID]
	return ok
}

func createTask(job durablemgr.Job) createTaskResult {
	return createTaskResult{ResultType: "task", taskBase: baseTask(job)}
}

func detailedTask(job durablemgr.Job) getTaskResult {
	base := baseTask(job)
	out := getTaskResult{ResultType: "complete", taskBase: base}
	switch job.State {
	case durablemgr.StateCompleted:
		out.Status = "completed"
		out.StatusMessage = "Durable command completed successfully."
		out.Result = finalToolResult(job, false)
	case durablemgr.StateFailed:
		// A non-zero shell/tool outcome is a completed MCP tool result with
		// isError=true, not a failed task lifecycle (SEP-2663).
		out.Status = "completed"
		out.StatusMessage = "Durable command completed with an execution error."
		out.Result = finalToolResult(job, true)
	case durablemgr.StateCanceled:
		out.Status = "cancelled"
		out.StatusMessage = "Durable command was cancelled."
	case durablemgr.StateOrphaned:
		out.Status = "failed"
		out.StatusMessage = "Durable execution state could not be reconciled safely."
		out.Error = &taskJSONRPCError{Code: jsonrpc.CodeInternalError, Message: "durable execution became orphaned: " + job.Reason}
	default:
		out.Status = "working"
		out.StatusMessage = "Durable command is running."
	}
	return out
}

func baseTask(job durablemgr.Job) taskBase {
	status := "working"
	if job.State == durablemgr.StateCanceled {
		status = "cancelled"
	}
	return taskBase{TaskID: job.ID, Status: status, CreatedAt: job.StartedAt.UTC().Format(time.RFC3339Nano), LastUpdatedAt: job.UpdatedAt.UTC().Format(time.RFC3339Nano), TTLMS: nil, PollIntervalMS: taskPollIntervalMS}
}

func finalToolResult(job durablemgr.Job, isError bool) *taskCallToolResult {
	payload := StartDurableJobOutput{Job: viewDurableJob(job)}
	data, _ := json.Marshal(payload)
	return &taskCallToolResult{ResultType: "complete", Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}, StructuredContent: payload, IsError: isError}
}

func durableJobIDFromToolResult(result *mcp.CallToolResult) (string, error) {
	raw, ok := result.StructuredContent.(json.RawMessage)
	if !ok {
		if bytes, ok := result.StructuredContent.([]byte); ok {
			raw = json.RawMessage(bytes)
		}
	}
	if len(raw) == 0 {
		return "", errors.New("start_durable_job returned no structured job handle")
	}
	var payload struct {
		Job struct {
			ID    string `json:"id"`
			JobID string `json:"job_id"`
		} `json:"job"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", fmt.Errorf("decode start_durable_job task handle: %w", err)
	}
	id := payload.Job.JobID
	if id == "" {
		id = payload.Job.ID
	}
	if strings.TrimSpace(id) == "" {
		return "", errors.New("start_durable_job returned an empty durable job id")
	}
	return id, nil
}

func invalidTaskError(action string, err error) error {
	return &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: fmt.Sprintf("Failed to %s task: %v", action, err)}
}
