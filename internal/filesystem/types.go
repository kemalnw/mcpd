package filesystem

import "time"

const (
	FileTypeText        = "text"
	FileTypeDirectory   = "directory"
	FileTypeImage       = "image"
	FileTypeExcel       = "excel"
	FileTypePDF         = "pdf"
	FileTypeDOCX        = "docx"
	FileTypeBinary      = "binary"
	FileTypeUnsupported = "unsupported"
)

type Options struct {
	DefaultReadLines int
	MaxLineBytes     int
	NestedEntryLimit int
	HTTPTimeout      time.Duration
	MaxRemoteBytes   int64
}

type ReadRequest struct {
	Path    string
	IsURL   bool
	Offset  int
	Length  int
	Sheet   string
	Range   string
	Options map[string]any
}

type ReadResult struct {
	Path       string   `json:"path"`
	Source     string   `json:"source"`
	FileType   string   `json:"file_type"`
	MIMEType   string   `json:"mime_type,omitempty"`
	Content    string   `json:"content"`
	Lines      []string `json:"lines,omitempty"`
	Offset     int      `json:"offset"`
	ReadFrom   int      `json:"read_from"`
	ReadCount  int      `json:"read_count"`
	TotalLines int      `json:"total_lines"`
	Remaining  int      `json:"remaining"`
	Size       int64    `json:"size_bytes,omitempty"`
}

type MultiReadResult struct {
	Path   string      `json:"path"`
	Result *ReadResult `json:"result,omitempty"`
	Error  string      `json:"error,omitempty"`
}

type WriteRequest struct {
	Path    string
	Content string
	Mode    string
}

type WriteResult struct {
	Path         string `json:"path"`
	Mode         string `json:"mode"`
	BytesWritten int    `json:"bytes_written"`
	LineCount    int    `json:"line_count"`
}

type DirectoryEntry struct {
	Path   string `json:"path"`
	Type   string `json:"type"`
	Depth  int    `json:"depth"`
	Size   int64  `json:"size_bytes,omitempty"`
	Error  string `json:"error,omitempty"`
	Hidden int    `json:"hidden,omitempty"`
}

type DirectoryResult struct {
	Root    string           `json:"root"`
	Depth   int              `json:"depth"`
	Entries []DirectoryEntry `json:"entries"`
}

type MoveResult struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
}

type FileInfo struct {
	Path           string     `json:"path"`
	Name           string     `json:"name"`
	Size           int64      `json:"size"`
	Created        *time.Time `json:"created,omitempty"`
	Modified       time.Time  `json:"modified"`
	Accessed       *time.Time `json:"accessed,omitempty"`
	IsDirectory    bool       `json:"is_directory"`
	IsFile         bool       `json:"is_file"`
	Permissions    string     `json:"permissions"`
	FileType       string     `json:"file_type"`
	LineCount      *int       `json:"line_count,omitempty"`
	LastLine       *int       `json:"last_line,omitempty"`
	AppendPosition *int       `json:"append_position,omitempty"`
}

type EditRequest struct {
	Path                 string
	OldString            string
	NewString            string
	ExpectedReplacements int
	Range                string
	Content              any
	Options              map[string]any
}

type EditResult struct {
	Path                 string  `json:"path"`
	Applied              bool    `json:"applied"`
	Replacements         int     `json:"replacements"`
	ExpectedReplacements int     `json:"expected_replacements"`
	ClosestMatch         string  `json:"closest_match,omitempty"`
	Similarity           float64 `json:"similarity,omitempty"`
	Diff                 string  `json:"diff,omitempty"`
	Message              string  `json:"message"`
}
