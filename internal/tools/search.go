package tools

import (
	"context"

	"github.com/kemalnw/mcpd/internal/audit"
	searchmgr "github.com/kemalnw/mcpd/internal/search"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type SearchTools struct {
	manager *searchmgr.Manager
}

func RegisterSearch(server *mcp.Server, manager *searchmgr.Manager, auditStore *audit.Store) {
	t := &SearchTools{manager: manager}
	mcp.AddTool(server, tool("start_search", "Start progressive search", "Start a background file-name or file-content search and return a sessionId immediately. Results are retained and can be paginated with get_more_search_results. Uses ripgrep when available and a native Go fallback otherwise.", true, false), audited(auditStore, "start_search", t.start))
	mcp.AddTool(server, tool("get_more_search_results", "Read search results", "Read retained results from a search session. Positive offsets are absolute result positions; negative offsets return the last N retained results. Search sessions remain readable after completion until retention cleanup.", true, false), audited(auditStore, "get_more_search_results", t.read))
	mcp.AddTool(server, tool("stop_search", "Stop search", "Cancel a running search while preserving results already found for later reads.", false, true), audited(auditStore, "stop_search", t.stop))
	mcp.AddTool(server, tool("list_searches", "List search sessions", "List running and recently completed search sessions with type, pattern, backend, runtime, and result counts.", true, false), audited(auditStore, "list_searches", t.list))
}

type StartSearchInput struct {
	Path             string `json:"path" jsonschema:"root directory or file to search"`
	Pattern          string `json:"pattern" jsonschema:"file-name pattern or content pattern"`
	SearchType       string `json:"searchType,omitempty" jsonschema:"search mode: files or content; defaults to files"`
	FilePattern      string `json:"filePattern,omitempty" jsonschema:"optional pipe-separated glob filters such as *.go|*.md"`
	IgnoreCase       *bool  `json:"ignoreCase,omitempty" jsonschema:"case-insensitive matching; defaults to true"`
	MaxResults       int    `json:"maxResults,omitempty" jsonschema:"global maximum number of match results retained; defaults to configured limit"`
	IncludeHidden    bool   `json:"includeHidden,omitempty" jsonschema:"include hidden files and directories"`
	ContextLines     *int   `json:"contextLines,omitempty" jsonschema:"context lines around content matches; defaults to 5"`
	TimeoutMS        int    `json:"timeout_ms,omitempty" jsonschema:"optional maximum search runtime in milliseconds; zero means no explicit timeout"`
	EarlyTermination *bool  `json:"earlyTermination,omitempty" jsonschema:"stop after an exact filename match; defaults to true for file search and false for content search"`
	LiteralSearch    bool   `json:"literalSearch,omitempty" jsonschema:"treat content pattern as a literal string instead of a regular expression"`
}

type SearchResultsInput struct {
	SessionID string `json:"sessionId" jsonschema:"search session identifier returned by start_search"`
	Offset    int    `json:"offset,omitempty" jsonschema:"absolute result offset; negative values return the last N results"`
	Length    int    `json:"length,omitempty" jsonschema:"maximum results to return for non-negative offsets; defaults to 100"`
}

type SearchSessionInput struct {
	SessionID string `json:"sessionId" jsonschema:"search session identifier returned by start_search"`
}

type SearchListOutput struct {
	Searches []searchmgr.SessionInfo `json:"searches"`
}

func (t *SearchTools) start(ctx context.Context, in StartSearchInput) (searchmgr.StartResult, error) {
	ignoreCase := true
	if in.IgnoreCase != nil {
		ignoreCase = *in.IgnoreCase
	}
	contextLines := 5
	if in.ContextLines != nil {
		contextLines = *in.ContextLines
	}
	searchType := searchmgr.Type(in.SearchType)
	if searchType == "" {
		searchType = searchmgr.TypeFiles
	}
	early := searchType == searchmgr.TypeFiles
	if in.EarlyTermination != nil {
		early = *in.EarlyTermination
	}
	return t.manager.Start(ctx, searchmgr.Options{
		RootPath: in.Path, Pattern: in.Pattern, SearchType: searchType, FilePattern: in.FilePattern, IgnoreCase: ignoreCase,
		MaxResults: in.MaxResults, IncludeHidden: in.IncludeHidden, ContextLines: contextLines, TimeoutMS: in.TimeoutMS,
		EarlyTermination: early, LiteralSearch: in.LiteralSearch,
	})
}

func (t *SearchTools) read(_ context.Context, in SearchResultsInput) (searchmgr.ReadResult, error) {
	return t.manager.Read(in.SessionID, in.Offset, in.Length)
}

func (t *SearchTools) stop(_ context.Context, in SearchSessionInput) (searchmgr.StopResult, error) {
	return t.manager.Stop(in.SessionID), nil
}

func (t *SearchTools) list(_ context.Context, _ EmptyInput) (SearchListOutput, error) {
	return SearchListOutput{Searches: t.manager.List()}, nil
}
