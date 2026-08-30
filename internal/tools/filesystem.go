package tools

import (
	"context"
	"errors"

	"github.com/kemalnw/mcpd/internal/audit"
	fsmgr "github.com/kemalnw/mcpd/internal/filesystem"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type FilesystemTools struct {
	manager *fsmgr.Manager
}

func RegisterFilesystem(server *mcp.Server, manager *fsmgr.Manager, auditStore *audit.Store) {
	t := &FilesystemTools{manager: manager}
	mcp.AddTool(server, tool("read_file", "Read a file or URL", "Use this to read one text file with line pagination, or to fetch a textual HTTP/HTTPS URL when isUrl=true. Prefer read_multiple_files when several known local files are needed, start_search when the file/location is unknown, and get_file_info when only metadata is needed. Local text returns a single line-oriented `lines` payload with pagination metadata; URL text returns a single full `content` payload. For local files, offset is zero-based; negative offsets read from the tail.", toolHints{readOnly: true, openWorld: true}), audited(auditStore, "read_file", t.readFile))
	mcp.AddTool(server, tool("read_multiple_files", "Read multiple local files", "Use this when the exact paths of multiple local text files are already known and they can be read independently in one call. Prefer read_file for one file or paginated continuation, and start_search when you need to discover which files contain something.", toolHints{readOnly: true, idempotent: true}), audited(auditStore, "read_multiple_files", t.readMultipleFiles))
	mcp.AddTool(server, tool("write_file", "Write or append a text file", "Use this to create a text file, replace its complete contents, or append text. mode defaults to rewrite; use append only when preserving existing content is intended. Prefer edit_block for a targeted change inside an existing file so unrelated content is not rewritten.", toolHints{destructive: true}), audited(auditStore, "write_file", t.writeFile))
	mcp.AddTool(server, tool("create_directory", "Create a directory", "Use this to create a directory and any missing parent directories. It is additive and safe to repeat for the same path. Do not use start_process with mkdir when this dedicated tool is sufficient.", toolHints{idempotent: true}), audited(auditStore, "create_directory", t.createDirectory))
	mcp.AddTool(server, tool("list_directory", "List directory contents", "Use this to inspect the entries under a known directory path, optionally recursively to depth. Prefer start_search when looking for a filename or content across a larger tree; prefer get_file_info for metadata about one known path.", toolHints{readOnly: true, idempotent: true}), audited(auditStore, "list_directory", t.listDirectory))
	mcp.AddTool(server, tool("move_file", "Move or rename a path", "Use this to rename or move an existing file or directory with the operating-system rename operation. This changes filesystem state. Do not use it for copying; MCPD does not currently expose a dedicated copy tool.", toolHints{destructive: true}), audited(auditStore, "move_file", t.moveFile))
	mcp.AddTool(server, tool("get_file_info", "Inspect file metadata", "Use this when you need metadata for one known file or directory—such as size, permissions, timestamps, detected type, and text line metadata—without reading its contents. Prefer read_file when content is needed and list_directory when inspecting children of a directory.", toolHints{readOnly: true, idempotent: true}), audited(auditStore, "get_file_info", t.getFileInfo))
	mcp.AddTool(server, tool("edit_block", "Edit part of a file", "Use this for precise edits to an existing file. In text mode, provide old_string and new_string; MCPD modifies the file only when the exact expected occurrence count matches, preventing ambiguous replacements. Prefer this over write_file for localized source/config edits. If no exact match exists, inspect the returned closest-match hint before retrying.", toolHints{destructive: true}), audited(auditStore, "edit_block", t.editBlock))
}

type ReadFileInput struct {
	Path    string         `json:"path" jsonschema:"local file path or URL when isUrl is true"`
	IsURL   bool           `json:"isUrl,omitempty" jsonschema:"fetch path as an HTTP or HTTPS URL"`
	Offset  int            `json:"offset,omitempty" jsonschema:"zero-based line offset; negative N reads the last N lines"`
	Length  int            `json:"length,omitempty" jsonschema:"maximum lines to read for non-negative offsets; defaults to configured read limit"`
	Sheet   string         `json:"sheet,omitempty" jsonschema:"reserved for spreadsheet handlers; sheet name or zero-based index encoded as a string"`
	Range   string         `json:"range,omitempty" jsonschema:"reserved for structured file handlers"`
	Options map[string]any `json:"options,omitempty" jsonschema:"format-specific options reserved for structured file handlers"`
}

type ReadMultipleFilesInput struct {
	Paths []string `json:"paths" jsonschema:"local file paths to read"`
}

type ReadMultipleFilesOutput struct {
	Files []fsmgr.MultiReadResult `json:"files"`
}

type WriteFileInput struct {
	Path    string `json:"path" jsonschema:"destination file path"`
	Content string `json:"content" jsonschema:"text content to write"`
	Mode    string `json:"mode,omitempty" jsonschema:"rewrite or append; defaults to rewrite"`
}

type CreateDirectoryInput struct {
	Path string `json:"path" jsonschema:"directory path to create"`
}

type CreateDirectoryOutput struct {
	Path    string `json:"path"`
	Created bool   `json:"created"`
}

type ListDirectoryInput struct {
	Path  string `json:"path" jsonschema:"directory path to list"`
	Depth int    `json:"depth,omitempty" jsonschema:"recursive depth; defaults to 2"`
}

type MoveFileInput struct {
	Source      string `json:"source" jsonschema:"existing source path"`
	Destination string `json:"destination" jsonschema:"destination path"`
}

type GetFileInfoInput struct {
	Path string `json:"path" jsonschema:"file or directory path"`
}

type EditBlockInput struct {
	FilePath             string         `json:"file_path" jsonschema:"path of file to edit"`
	OldString            string         `json:"old_string,omitempty" jsonschema:"exact text to replace for text files"`
	NewString            *string        `json:"new_string,omitempty" jsonschema:"replacement text; may be an empty string"`
	ExpectedReplacements int            `json:"expected_replacements,omitempty" jsonschema:"exact number of replacements expected; defaults to 1"`
	Range                string         `json:"range,omitempty" jsonschema:"reserved for structured file range edits"`
	Content              any            `json:"content,omitempty" jsonschema:"reserved replacement content for structured file range edits"`
	Options              map[string]any `json:"options,omitempty" jsonschema:"format-specific edit options"`
}

func (t *FilesystemTools) readFile(ctx context.Context, in ReadFileInput) (fsmgr.ReadResult, error) {
	return t.manager.Read(ctx, fsmgr.ReadRequest{Path: in.Path, IsURL: in.IsURL, Offset: in.Offset, Length: in.Length, Sheet: in.Sheet, Range: in.Range, Options: in.Options})
}

func (t *FilesystemTools) readMultipleFiles(ctx context.Context, in ReadMultipleFilesInput) (ReadMultipleFilesOutput, error) {
	return ReadMultipleFilesOutput{Files: t.manager.ReadMultiple(ctx, in.Paths)}, nil
}

func (t *FilesystemTools) writeFile(ctx context.Context, in WriteFileInput) (fsmgr.WriteResult, error) {
	return t.manager.Write(ctx, fsmgr.WriteRequest{Path: in.Path, Content: in.Content, Mode: in.Mode})
}

func (t *FilesystemTools) createDirectory(_ context.Context, in CreateDirectoryInput) (CreateDirectoryOutput, error) {
	if err := t.manager.CreateDirectory(in.Path); err != nil {
		return CreateDirectoryOutput{}, err
	}
	return CreateDirectoryOutput{Path: in.Path, Created: true}, nil
}

func (t *FilesystemTools) listDirectory(ctx context.Context, in ListDirectoryInput) (fsmgr.DirectoryResult, error) {
	return t.manager.ListDirectory(ctx, in.Path, in.Depth)
}

func (t *FilesystemTools) moveFile(_ context.Context, in MoveFileInput) (fsmgr.MoveResult, error) {
	return t.manager.Move(in.Source, in.Destination)
}

func (t *FilesystemTools) getFileInfo(_ context.Context, in GetFileInfoInput) (fsmgr.FileInfo, error) {
	return t.manager.Info(in.Path)
}

func (t *FilesystemTools) editBlock(ctx context.Context, in EditBlockInput) (fsmgr.EditResult, error) {
	textMode := in.OldString != "" && in.NewString != nil
	structuredMode := in.Range != "" && in.Content != nil
	if !textMode && !structuredMode {
		return fsmgr.EditResult{}, errors.New("must provide either old_string + new_string or range + content")
	}
	newString := ""
	if in.NewString != nil {
		newString = *in.NewString
	}
	return t.manager.Edit(ctx, fsmgr.EditRequest{
		Path: in.FilePath, OldString: in.OldString, NewString: newString,
		ExpectedReplacements: in.ExpectedReplacements, Range: in.Range, Content: in.Content, Options: in.Options,
	})
}
