package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	durablemgr "github.com/kemalnw/mcpd/internal/durableexec"
	processmgr "github.com/kemalnw/mcpd/internal/process"
)

const (
	maxMetadataLogBytes = 1024
	maxPathPreviewItems = 5
)

func logToolCall[In any](ctx context.Context, eventID, name string, in In) {
	attrs := []slog.Attr{
		slog.String("event_id", eventID),
		slog.String("tool", name),
	}
	attrs = append(attrs, toolInputAttrs(any(in))...)
	slog.Default().LogAttrs(ctx, slog.LevelInfo, "mcp tool call", attrs...)
}

func logToolResult[Out any](ctx context.Context, eventID, name string, out Out, durationMS int64, err error) {
	status := "success"
	level := slog.LevelInfo
	if err != nil {
		status = "error"
		level = slog.LevelError
	}
	attrs := []slog.Attr{
		slog.String("event_id", eventID),
		slog.String("tool", name),
		slog.String("status", status),
		slog.Int64("duration_ms", durationMS),
	}
	attrs = append(attrs, toolOutputAttrs(any(out))...)
	if err != nil {
		attrs = append(attrs,
			slog.Int("error_bytes", len(err.Error())),
			slog.String("error_sha256", auditDigest(err.Error())),
			slog.String("error_type", fmt.Sprintf("%T", err)),
		)
	}
	slog.Default().LogAttrs(ctx, level, "mcp tool result", attrs...)
}

func toolInputAttrs(in any) []slog.Attr {
	switch v := in.(type) {
	case StartDurableJobInput:
		attrs := []slog.Attr{slog.Int("command_bytes", len(v.Command)), slog.String("command_sha256", auditDigest(v.Command)), slog.Bool("has_idempotency_key", strings.TrimSpace(v.IdempotencyKey) != "")}
		attrs = appendLogString(attrs, "cwd", v.CWD, maxMetadataLogBytes)
		return appendLogString(attrs, "shell", v.Shell, maxMetadataLogBytes)
	case DurableJobInput:
		return appendLogString(nil, "durable_job_id", v.JobID, maxMetadataLogBytes)
	case ListDurableJobsInput:
		return []slog.Attr{slog.Int("offset", v.Offset), slog.Int("limit", v.Limit)}
	case ReadDurableJobLogInput:
		return append(appendLogString(nil, "durable_job_id", v.JobID, maxMetadataLogBytes), slog.Int("max_bytes", v.MaxBytes))
	case StartProcessInput:
		attrs := []slog.Attr{slog.Int("timeout_ms", v.TimeoutMS), slog.Int("command_bytes", len(v.Command))}
		attrs = appendLogString(attrs, "command_sha256", auditDigest(v.Command), maxMetadataLogBytes)
		attrs = appendLogString(attrs, "cwd", v.CWD, maxMetadataLogBytes)
		attrs = appendLogString(attrs, "shell", v.Shell, maxMetadataLogBytes)
		attrs = appendLogString(attrs, "pty_mode", v.PTY, maxMetadataLogBytes)
		attrs = append(attrs, slog.Bool("separate_streams", v.SeparateStreams), slog.Bool("has_idempotency_key", strings.TrimSpace(v.IdempotencyKey) != ""))
		return attrs
	case StartProcessBatchInput:
		attrs := []slog.Attr{slog.Int("job_count", len(v.Jobs)), slog.Int("max_parallel", v.MaxParallel), slog.Int("initial_wait_ms", v.InitialWaitMS), slog.Bool("has_idempotency_key", strings.TrimSpace(v.IdempotencyKey) != "")}
		return appendLogString(attrs, "output_mode", v.OutputMode, maxMetadataLogBytes)
	case ReadProcessBatchInput:
		attrs := []slog.Attr{slog.Int("timeout_ms", v.TimeoutMS), slog.Int("length", v.Length), slog.Int("max_bytes_per_job", v.MaxBytesPerJob)}
		attrs = appendLogString(attrs, "output_mode", v.OutputMode, maxMetadataLogBytes)
		if v.OnlyChanged != nil {
			attrs = append(attrs, slog.Bool("only_changed", *v.OnlyChanged))
		}
		return appendLogString(attrs, "batch_id", v.BatchID, maxMetadataLogBytes)
	case BatchIDInput:
		return appendLogString(nil, "batch_id", v.BatchID, maxMetadataLogBytes)
	case ReadProcessOutputInput:
		return []slog.Attr{slog.Int("pid", v.PID), slog.Int("timeout_ms", v.TimeoutMS), slog.Int("offset", v.Offset), slog.Int("length", v.Length), slog.Int("max_bytes", v.MaxBytes)}
	case InteractWithProcessInput:
		return []slog.Attr{slog.Int("pid", v.PID), slog.Int("input_bytes", len(v.Input)), slog.Int("input_lines", lineCount(v.Input)), slog.Int("timeout_ms", v.TimeoutMS), slog.Bool("raw_input", v.RawInput), slog.Bool("has_operation_key", strings.TrimSpace(v.OperationKey) != "")}
	case ResizePTYInput:
		return []slog.Attr{slog.Int("pid", v.PID), slog.Int("rows", v.Rows), slog.Int("cols", v.Cols)}
	case PIDInput:
		attrs := []slog.Attr{slog.Int("pid", v.PID)}
		if v.ExpectedStartTicks != 0 {
			attrs = append(attrs, slog.Uint64("expected_start_ticks", v.ExpectedStartTicks))
		}
		return attrs
	case ReadFileInput:
		attrs := []slog.Attr{slog.Bool("is_url", v.IsURL), slog.Int("offset", v.Offset), slog.Int("length", v.Length)}
		path := v.Path
		if v.IsURL {
			path = safeURLForLog(v.Path)
		}
		attrs = appendLogString(attrs, "path", path, maxMetadataLogBytes)
		attrs = appendLogString(attrs, "sheet", v.Sheet, maxMetadataLogBytes)
		attrs = appendLogString(attrs, "range", v.Range, maxMetadataLogBytes)
		return attrs
	case ReadMultipleFilesInput:
		preview := make([]string, 0, min(len(v.Paths), maxPathPreviewItems))
		for _, path := range v.Paths[:min(len(v.Paths), maxPathPreviewItems)] {
			preview = append(preview, truncateLogString(path, maxMetadataLogBytes))
		}
		return []slog.Attr{slog.Int("path_count", len(v.Paths)), slog.Any("paths_preview", preview), slog.Bool("paths_truncated", len(v.Paths) > maxPathPreviewItems)}
	case WriteFileInput:
		attrs := []slog.Attr{slog.Int("content_bytes", len(v.Content))}
		if v.ExpectedSize != nil {
			attrs = append(attrs, slog.Int64("expected_size", *v.ExpectedSize))
		}
		attrs = appendLogString(attrs, "path", v.Path, maxMetadataLogBytes)
		attrs = appendLogString(attrs, "mode", v.Mode, maxMetadataLogBytes)
		return attrs
	case CreateDirectoryInput:
		return appendLogString(nil, "path", v.Path, maxMetadataLogBytes)
	case ListDirectoryInput:
		return append(appendLogString(nil, "path", v.Path, maxMetadataLogBytes), slog.Int("depth", v.Depth))
	case MoveFileInput:
		attrs := appendLogString(nil, "source", v.Source, maxMetadataLogBytes)
		return appendLogString(attrs, "destination", v.Destination, maxMetadataLogBytes)
	case GetFileInfoInput:
		return appendLogString(nil, "path", v.Path, maxMetadataLogBytes)
	case EditBlockInput:
		attrs := []slog.Attr{slog.Int("old_string_bytes", len(v.OldString)), slog.Int("expected_replacements", v.ExpectedReplacements), slog.Int("edit_count", len(v.Edits))}
		if v.NewString != nil {
			attrs = append(attrs, slog.Int("new_string_bytes", len(*v.NewString)))
		}
		attrs = appendLogString(attrs, "path", v.FilePath, maxMetadataLogBytes)
		attrs = appendLogString(attrs, "range", v.Range, maxMetadataLogBytes)
		return attrs
	case CreateRunInput:
		return []slog.Attr{slog.Int("success_criteria_count", len(v.SuccessCriteria)), slog.Int("objective_bytes", len(v.Objective)), slog.Bool("has_idempotency_key", strings.TrimSpace(v.IdempotencyKey) != "")}
	case CheckpointRunInput:
		attrs := []slog.Attr{slog.Uint64("expected_revision", v.ExpectedRevision), slog.Int("item_count", len(v.Items)), slog.Int("next_action_count", len(v.NextActions))}
		return appendLogString(attrs, "run_id", v.RunID, maxMetadataLogBytes)
	case GetRunInput:
		attrs := []slog.Attr{slog.Int("item_offset", v.ItemOffset), slog.Int("item_limit", v.ItemLimit), slog.Int("criteria_offset", v.CriteriaOffset), slog.Int("criteria_limit", v.CriteriaLimit), slog.Int("next_action_offset", v.NextActionOffset), slog.Int("next_action_limit", v.NextActionLimit)}
		return appendLogString(attrs, "run_id", v.RunID, maxMetadataLogBytes)
	case ListRunsInput:
		return []slog.Attr{slog.Int("offset", v.Offset), slog.Int("limit", v.Limit)}
	case ReadRunJobLogInput:
		attrs := []slog.Attr{slog.Int("tail_lines", v.TailLines), slog.Int("max_bytes", v.MaxBytes)}
		attrs = appendLogString(attrs, "run_id", v.RunID, maxMetadataLogBytes)
		return appendLogString(attrs, "job_id", v.JobID, maxMetadataLogBytes)
	case HandoffRunInput:
		attrs := []slog.Attr{slog.Uint64("expected_revision", v.ExpectedRevision), slog.Int("summary_bytes", len(v.Summary)), slog.Int("blocker_count", len(v.Blockers)), slog.Int("active_handle_count", len(v.ActiveHandles)), slog.Int("next_action_count", len(v.NextActions))}
		attrs = appendLogString(attrs, "run_id", v.RunID, maxMetadataLogBytes)
		return appendLogString(attrs, "checkpoint_reason", v.Reason, maxMetadataLogBytes)
	case ResumeRunInput:
		return appendLogString(nil, "run_id", v.RunID, maxMetadataLogBytes)
	case StartSearchInput:
		attrs := []slog.Attr{slog.Int("max_results", v.MaxResults), slog.Bool("include_hidden", v.IncludeHidden), slog.Int("timeout_ms", v.TimeoutMS), slog.Int("pattern_bytes", len(v.Pattern))}
		attrs = appendLogString(attrs, "path", v.Path, maxMetadataLogBytes)
		attrs = appendLogString(attrs, "pattern_sha256", auditDigest(v.Pattern), maxMetadataLogBytes)
		attrs = appendLogString(attrs, "path_hint", v.PathHint, maxMetadataLogBytes)
		attrs = appendLogString(attrs, "search_type", v.SearchType, maxMetadataLogBytes)
		attrs = appendLogString(attrs, "file_pattern", v.FilePattern, maxMetadataLogBytes)
		return attrs
	case SearchResultsInput:
		attrs := []slog.Attr{slog.Int("offset", v.Offset), slog.Int("length", v.Length)}
		return appendLogString(attrs, "search_id", v.SessionID, maxMetadataLogBytes)
	case SearchSessionInput:
		return appendLogString(nil, "search_id", v.SessionID, maxMetadataLogBytes)
	default:
		return nil
	}
}

func toolOutputAttrs(out any) []slog.Attr {
	switch v := out.(type) {
	case StartDurableJobOutput:
		return []slog.Attr{slog.String("durable_job_id", v.Job.ID), slog.String("durable_job_state", string(v.Job.State)), slog.Bool("idempotent_replay", v.IdempotentReplay)}
	case DurableJobView:
		attrs := []slog.Attr{slog.String("durable_job_id", v.ID), slog.String("durable_job_state", string(v.State)), slog.Int("runner_pid", v.RunnerPID), slog.Int("child_pid", v.ChildPID)}
		return appendExitCode(attrs, v.ExitCode)
	case ListDurableJobsOutput:
		return []slog.Attr{slog.Int("returned", v.Returned), slog.Int("total", v.Total), slog.Bool("more", v.More)}
	case durablemgr.LogTail:
		return []slog.Attr{slog.String("durable_job_id", v.JobID), slog.Int("bytes_returned", v.BytesReturned), slog.Int64("total_bytes", v.TotalBytes), slog.Bool("truncated", v.Truncated)}
	case processmgr.StartResult:
		attrs := []slog.Attr{
			slog.Int("pid", v.PID),
			slog.String("process_state", string(v.State)),
			slog.Bool("pty", v.PTY),
			slog.Bool("waiting_for_input", v.WaitingForInput),
			slog.Int("read_count", v.ReadCount),
			slog.Int("total_lines", v.TotalLines),
			slog.Int("remaining", v.Remaining),
			slog.Int64("waited_ms", v.WaitedMS),
		}
		attrs = appendLogString(attrs, "cwd", v.CWD, maxMetadataLogBytes)
		attrs = appendLogString(attrs, "shell", v.Shell, maxMetadataLogBytes)
		return appendExitCode(attrs, v.ExitCode)
	case processmgr.BatchResult:
		return []slog.Attr{slog.String("batch_id", v.BatchID), slog.String("batch_state", string(v.State)), slog.Uint64("generation", v.Generation), slog.Int("job_count", len(v.Jobs)), slog.Int("queued", v.Counts.Queued), slog.Int("running", v.Counts.Running), slog.Int("completed", v.Counts.Completed), slog.Int("failed", v.Counts.Failed), slog.Int("canceled", v.Counts.Canceled)}
	case processmgr.BatchCancelResult:
		return []slog.Attr{slog.String("batch_id", v.BatchID), slog.String("batch_state", string(v.State)), slog.Int("canceled", v.Canceled)}
	case processmgr.OutputResult:
		attrs := []slog.Attr{slog.Int("pid", v.PID), slog.String("process_state", string(v.State)), slog.Int64("runtime_ms", v.RuntimeMS), slog.Bool("waiting_for_input", v.WaitingForInput)}
		return appendExitCode(attrs, v.ExitCode)
	case processmgr.InteractResult:
		attrs := []slog.Attr{slog.Int("pid", v.PID), slog.String("process_state", string(v.State)), slog.Int64("runtime_ms", v.RuntimeMS), slog.Bool("waiting_for_input", v.WaitingForInput)}
		return appendExitCode(attrs, v.ExitCode)
	case processmgr.PTYSizeResult:
		return []slog.Attr{slog.Int("pid", v.PID), slog.Int("rows", v.Rows), slog.Int("cols", v.Cols)}
	case TerminateOutput:
		attrs := []slog.Attr{slog.Int("pid", v.PID), slog.Bool("terminated", v.Terminated)}
		return appendLogString(attrs, "signal", v.Signal, maxMetadataLogBytes)
	case RunView:
		return []slog.Attr{slog.String("run_id", v.Run.ID), slog.Uint64("revision", v.Run.Revision), slog.String("run_state", string(v.Run.State)), slog.Int("running", v.Counts.Running), slog.Int("blocked", v.Counts.Blocked), slog.Int("failed", v.Counts.Failed), slog.Int("completed", v.Counts.Completed)}
	case ListRunsOutput:
		return []slog.Attr{slog.Int("run_count", len(v.Runs))}
	case RunJobLogOutput:
		attrs := []slog.Attr{slog.Int("line_count", len(v.Lines))}
		attrs = appendLogString(attrs, "run_id", v.RunID, maxMetadataLogBytes)
		return appendLogString(attrs, "job_id", v.JobID, maxMetadataLogBytes)
	case ResumeRunOutput:
		return []slog.Attr{slog.String("run_id", v.RunID), slog.Uint64("revision", v.Revision), slog.String("run_state", string(v.State)), slog.Bool("checkpoint_due", v.CheckpointDue), slog.Int64("checkpoint_age_seconds", v.CheckpointAgeSeconds), slog.Int("returned_items", len(v.Items)), slog.Int("items_omitted", v.ItemsOmitted)}
	default:
		return nil
	}
}

func appendExitCode(attrs []slog.Attr, exitCode *int) []slog.Attr {
	if exitCode != nil {
		attrs = append(attrs, slog.Int("exit_code", *exitCode))
	}
	return attrs
}

func appendLogString(attrs []slog.Attr, key, value string, limit int) []slog.Attr {
	if value == "" {
		return attrs
	}
	truncated := len(value) > limit
	attrs = append(attrs, slog.String(key, truncateLogString(value, limit)))
	if truncated {
		attrs = append(attrs, slog.Bool(key+"_truncated", true))
	}
	return attrs
}

func truncateLogString(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return value[:limit] + " [truncated]"
}

func auditDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func safeURLForLog(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "[invalid-url sha256:" + auditDigest(raw) + "]"
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

func lineCount(value string) int {
	if value == "" {
		return 0
	}
	lines := strings.Count(value, "\n")
	if !strings.HasSuffix(value, "\n") {
		lines++
	}
	return lines
}

// auditMetadata converts the already-curated activity attributes into a small
// JSON-safe map. This deliberately avoids serializing typed tool inputs: those
// inputs can contain commands, stdin, file bodies, search secrets, tokens, or
// other user content that does not belong in durable audit persistence.
func auditMetadata(in any) map[string]any {
	attrs := toolInputAttrs(in)
	if len(attrs) == 0 {
		return nil
	}
	out := make(map[string]any, len(attrs))
	for _, attr := range attrs {
		value := attr.Value.Resolve()
		switch value.Kind() {
		case slog.KindString:
			out[attr.Key] = value.String()
		case slog.KindInt64:
			out[attr.Key] = value.Int64()
		case slog.KindUint64:
			out[attr.Key] = value.Uint64()
		case slog.KindFloat64:
			out[attr.Key] = value.Float64()
		case slog.KindBool:
			out[attr.Key] = value.Bool()
		case slog.KindDuration:
			out[attr.Key] = value.Duration().String()
		case slog.KindTime:
			out[attr.Key] = value.Time().UTC()
		default:
			// Any-valued metadata is intentionally omitted from durable audit.
			// This prevents a future preview/object from bypassing data minimization.
		}
	}
	return out
}
