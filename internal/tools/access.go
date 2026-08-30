package tools

const (
	ScopeRead  = "mcp:read"
	ScopeWrite = "mcp:write"
)

var toolScopes = map[string]string{
	"start_process":           ScopeWrite,
	"start_process_batch":     ScopeWrite,
	"read_process_batch":      ScopeRead,
	"cancel_process_batch":    ScopeWrite,
	"read_process_output":     ScopeRead,
	"interact_with_process":   ScopeWrite,
	"resize_process_pty":      ScopeWrite,
	"force_terminate":         ScopeWrite,
	"list_sessions":           ScopeRead,
	"list_processes":          ScopeRead,
	"kill_process":            ScopeWrite,
	"read_file":               ScopeRead,
	"read_multiple_files":     ScopeRead,
	"write_file":              ScopeWrite,
	"create_directory":        ScopeWrite,
	"list_directory":          ScopeRead,
	"move_file":               ScopeWrite,
	"get_file_info":           ScopeRead,
	"edit_block":              ScopeWrite,
	"start_search":            ScopeRead,
	"get_more_search_results": ScopeRead,
	"stop_search":             ScopeRead,
	"list_searches":           ScopeRead,
}

func RequiredScope(toolName string) string {
	if scope, ok := toolScopes[toolName]; ok {
		return scope
	}
	return ScopeWrite
}
