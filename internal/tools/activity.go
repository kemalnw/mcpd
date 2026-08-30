package tools

import (
	"context"
	"log/slog"
	"strings"

	processmgr "github.com/kemalnw/mcpd/internal/process"
)

const (
	maxCommandLogBytes  = 4096
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
		attrs = append(attrs, slog.String("error", truncateLogString(err.Error(), maxMetadataLogBytes)))
	}
	slog.Default().LogAttrs(ctx, level, "mcp tool result", attrs...)
}

func toolInputAttrs(in any) []slog.Attr {
	switch v := in.(type) {
	case StartProcessInput:
		attrs := []slog.Attr{slog.Int("timeout_ms", v.TimeoutMS)}
		attrs = appendLogString(attrs, "command", v.Command, maxCommandLogBytes)
		attrs = appendLogString(attrs, "shell", v.Shell, maxMetadataLogBytes)
		attrs = appendLogString(attrs, "pty_mode", v.PTY, maxMetadataLogBytes)
		return attrs
	case ReadProcessOutputInput:
		return []slog.Attr{slog.Int("pid", v.PID), slog.Int("timeout_ms", v.TimeoutMS), slog.Int("offset", v.Offset), slog.Int("length", v.Length)}
	case InteractWithProcessInput:
		return []slog.Attr{slog.Int("pid", v.PID), slog.Int("input_bytes", len(v.Input)), slog.Int("input_lines", lineCount(v.Input)), slog.Int("timeout_ms", v.TimeoutMS)}
	case PIDInput:
		return []slog.Attr{slog.Int("pid", v.PID)}
	case ReadFileInput:
		attrs := []slog.Attr{slog.Bool("is_url", v.IsURL), slog.Int("offset", v.Offset), slog.Int("length", v.Length)}
		attrs = appendLogString(attrs, "path", v.Path, maxMetadataLogBytes)
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
		attrs := []slog.Attr{slog.Int("old_string_bytes", len(v.OldString)), slog.Int("expected_replacements", v.ExpectedReplacements)}
		if v.NewString != nil {
			attrs = append(attrs, slog.Int("new_string_bytes", len(*v.NewString)))
		}
		attrs = appendLogString(attrs, "path", v.FilePath, maxMetadataLogBytes)
		attrs = appendLogString(attrs, "range", v.Range, maxMetadataLogBytes)
		return attrs
	case StartSearchInput:
		attrs := []slog.Attr{slog.Int("max_results", v.MaxResults), slog.Bool("include_hidden", v.IncludeHidden), slog.Int("timeout_ms", v.TimeoutMS)}
		attrs = appendLogString(attrs, "path", v.Path, maxMetadataLogBytes)
		attrs = appendLogString(attrs, "pattern", v.Pattern, maxMetadataLogBytes)
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
	case processmgr.StartResult:
		attrs := []slog.Attr{
			slog.Int("pid", v.PID),
			slog.String("process_state", string(v.State)),
			slog.Bool("pty", v.PTY),
			slog.Bool("waiting_for_input", v.WaitingForInput),
			slog.Int64("waited_ms", v.WaitedMS),
		}
		attrs = appendLogString(attrs, "shell", v.Shell, maxMetadataLogBytes)
		return appendExitCode(attrs, v.ExitCode)
	case processmgr.OutputResult:
		attrs := []slog.Attr{slog.Int("pid", v.PID), slog.String("process_state", string(v.State)), slog.Int64("runtime_ms", v.RuntimeMS), slog.Bool("waiting_for_input", v.WaitingForInput)}
		return appendExitCode(attrs, v.ExitCode)
	case processmgr.InteractResult:
		attrs := []slog.Attr{slog.Int("pid", v.PID), slog.String("process_state", string(v.State)), slog.Int64("runtime_ms", v.RuntimeMS), slog.Bool("waiting_for_input", v.WaitingForInput)}
		return appendExitCode(attrs, v.ExitCode)
	case TerminateOutput:
		attrs := []slog.Attr{slog.Int("pid", v.PID), slog.Bool("terminated", v.Terminated)}
		return appendLogString(attrs, "signal", v.Signal, maxMetadataLogBytes)
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
