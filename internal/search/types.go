package search

import "time"

type Type string

const (
	TypeFiles   Type = "files"
	TypeContent Type = "content"
)

type Options struct {
	RootPath         string
	Pattern          string
	SearchType       Type
	FilePattern      string
	PathHint         string
	IgnoreCase       bool
	MaxResults       int
	IncludeHidden    bool
	ContextLines     int
	TimeoutMS        int
	EarlyTermination bool
	LiteralSearch    bool
}

type Result struct {
	File      string `json:"file"`
	Line      int    `json:"line,omitempty"`
	Text      string `json:"text,omitempty"`
	Match     string `json:"match,omitempty"`
	Column    int    `json:"column,omitempty"`
	EndColumn int    `json:"end_column,omitempty"`
	Type      string `json:"type"`
	IsContext bool   `json:"is_context,omitempty"`
}

type StartResult struct {
	SessionID    string   `json:"sessionId"`
	IsComplete   bool     `json:"isComplete"`
	IsError      bool     `json:"isError"`
	Results      []Result `json:"results"`
	TotalResults int      `json:"totalResults"`
	TotalMatches int      `json:"totalMatches"`
	RuntimeMS    int64    `json:"runtime_ms"`
	Backend      string   `json:"backend"`
}

type ReadResult struct {
	SessionID      string   `json:"sessionId"`
	Results        []Result `json:"results"`
	ReturnedCount  int      `json:"returnedCount"`
	TotalResults   int      `json:"totalResults"`
	TotalMatches   int      `json:"totalMatches"`
	IsComplete     bool     `json:"isComplete"`
	IsError        bool     `json:"isError"`
	Error          string   `json:"error,omitempty"`
	HasMoreResults bool     `json:"hasMoreResults"`
	RuntimeMS      int64    `json:"runtime_ms"`
	WasIncomplete  bool     `json:"wasIncomplete,omitempty"`
	Backend        string   `json:"backend"`
}

type StopResult struct {
	SessionID string `json:"sessionId"`
	Stopped   bool   `json:"stopped"`
}

type SessionInfo struct {
	SessionID     string `json:"sessionId"`
	SearchType    string `json:"searchType"`
	Pattern       string `json:"pattern"`
	IsComplete    bool   `json:"isComplete"`
	IsError       bool   `json:"isError"`
	RuntimeMS     int64  `json:"runtime_ms"`
	TotalResults  int    `json:"totalResults"`
	TotalMatches  int    `json:"totalMatches"`
	WasIncomplete bool   `json:"wasIncomplete,omitempty"`
	Backend       string `json:"backend"`
}

type ManagerOptions struct {
	DefaultMaxResults        int
	Retention                time.Duration
	InitialWait              time.Duration
	RipgrepPath              string
	DisableRipgrep           bool
	PreferredRoots           []string
	WorkspaceIndexTTL        time.Duration
	WorkspaceIndexMaxEntries int
}
