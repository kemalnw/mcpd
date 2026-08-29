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
	mcp.AddTool(server, tool("read_file", "Read a file or URL", "Read text files with line pagination or fetch textual HTTP/HTTPS URLs. offset is zero-based; negative offsets return the last N lines. The schema also reserves sheet/range/options for Excel, PDF, and DOCX handlers added through the same facade.", true, false), audited(auditStore, "read_file", t.readFile))
	mcp.AddTool(server, tool("read_multiple_files", "Read multiple files", "Read multiple local files independently. A failure for one path is returned on that item and does not abort other reads.", true, false), audited(auditStore, "read_multiple_files", t.readMultipleFiles))
	mcp.AddTool(server, tool("write_file", "Write a file", "Write or append text content using the daemon user's filesystem permissions. mode defaults to rewrite and may be rewrite or append.", false, true), audited(auditStore, "write_file", t.writeFile))
	mcp.AddTool(server, tool("create_directory", "Create a directory", "Create a directory and any missing parent directories.", false, false), audited(auditStore, "create_directory", t.createDirectory))
	mcp.AddTool(server, tool("list_directory", "List a directory recursively", "List directory contents recursively to the requested depth. Top-level entries are unlimited; nested directories are capped by configuration and report hidden counts.", true, false), audited(auditStore, "list_directory", t.listDirectory))
	mcp.AddTool(server, tool("move_file", "Move or rename a file", "Move or rename a file or directory using the operating system rename operation.", false, true), audited(auditStore, "move_file", t.moveFile))
	mcp.AddTool(server, tool("get_file_info", "Get file metadata", "Return filesystem metadata including size, permissions, timestamps, detected file type, and text line metadata when applicable.", true, false), audited(auditStore, "get_file_info", t.getFileInfo))
	mcp.AddTool(server, tool("edit_block", "Edit a file block", "Perform exact text replacement with an expected occurrence count. If no exact match exists, returns the closest fuzzy match and character diff without modifying the file. range/content is reserved for structured file handlers.", false, true), audited(auditStore, "edit_block", t.editBlock))
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
