package process

import (
	"log/slog"
)

const maxLifecycleCommandLogBytes = 4096

func logProcessStarted(info SessionInfo) {
	attrs := []slog.Attr{
		slog.Int("pid", info.PID),
		slog.String("command", truncateLifecycleLogString(info.Command, maxLifecycleCommandLogBytes)),
		slog.String("shell", info.Shell),
		slog.Bool("pty", info.PTY),
		slog.String("process_state", string(info.State)),
	}
	if len(info.Command) > maxLifecycleCommandLogBytes {
		attrs = append(attrs, slog.Bool("command_truncated", true))
	}
	slog.Default().LogAttrs(nil, slog.LevelInfo, "process started", attrs...)
}

func logProcessExited(info SessionInfo, waitErr error) {
	level := slog.LevelInfo
	attrs := []slog.Attr{
		slog.Int("pid", info.PID),
		slog.String("process_state", string(info.State)),
		slog.Int64("runtime_ms", info.RuntimeMS),
	}
	if info.ExitCode != nil {
		attrs = append(attrs, slog.Int("exit_code", *info.ExitCode))
		if *info.ExitCode != 0 {
			level = slog.LevelWarn
		}
	}
	if waitErr != nil {
		attrs = append(attrs, slog.String("wait_error", truncateLifecycleLogString(waitErr.Error(), 1024)))
	}
	slog.Default().LogAttrs(nil, level, "process exited", attrs...)
}

func truncateLifecycleLogString(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return value[:limit] + " [truncated]"
}
