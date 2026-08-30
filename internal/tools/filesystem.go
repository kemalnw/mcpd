package tools

import (
	"context"
	"errors"
	"fmt"

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
	mcp.AddTool(server, tool("list_directory", "List directory contents", "Use this to inspect a known directory, optionally recursively. Recursive developer-noise directories are pruned by default and a global max_entries cap prevents response explosions; inspect pruned metadata or set includePruned=true only when dependency/cache internals are actually needed. Prefer start_search when looking for a filename or content across a larger tree.", toolHints{readOnly: true, idempotent: true}), audited(auditStore, "list_directory", t.listDirectory))
	mcp.AddTool(server, tool("move_file", "Move or rename a path", "Use this to rename or move an existing file or directory with the operating-system rename operation. This changes filesystem state. Do not use it for copying; MCPD does not currently expose a dedicated copy tool.", toolHints{destructive: true}), audited(auditStore, "move_file", t.moveFile))
	mcp.AddTool(server, tool("get_file_info", "Inspect file metadata", "Use this when you need metadata for one known file or directory—such as size, permissions, timestamps, detected type, and text line metadata—without reading its contents. Prefer read_file when content is needed and list_directory when inspecting children of a directory.", toolHints{readOnly: true, idempotent: true}), audited(auditStore, "get_file_info", t.getFileInfo))
	mcp.AddTool(server, tool("edit_block", "Edit part of a file", "Use this for precise edits to an existing file. For one change, provide old_string and new_string. For a multi-hunk refactor, provide edits; MCPD validates every exact hunk and overlap before one write, so any validation failure leaves the file unchanged. Prefer this over write_file for localized source/config edits. If no exact match exists, inspect the returned closest-match hint before retrying.", toolHints{destructive: true}), audited(auditStore, "edit_block", t.editBlock))
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
	Path          string `json:"path" jsonschema:"directory path to list"`
	Depth         int    `json:"depth,omitempty" jsonschema:"recursive depth; defaults to 2"`
	MaxEntries    int    `json:"maxEntries,omitempty" jsonschema:"global maximum returned entries; defaults to 1000"`
	IncludePruned bool   `json:"includePruned,omitempty" jsonschema:"recurse into developer-noise directories normally pruned by default"`
}

type MoveFileInput struct {
	Source      string `json:"source" jsonschema:"existing source path"`
	Destination string `json:"destination" jsonschema:"destination path"`
}

type GetFileInfoInput struct {
	Path string `json:"path" jsonschema:"file or directory path"`
}

type EditBlockEditInput struct {
	OldString            string  `json:"old_string" jsonschema:"exact text to replace"`
	NewString            *string `json:"new_string" jsonschema:"replacement text; may be an empty string"`
	ExpectedReplacements int     `json:"expected_replacements,omitempty" jsonschema:"exact occurrence count; defaults to 1"`
}

type EditBlockInput struct {
	FilePath             string               `json:"file_path" jsonschema:"path of file to edit"`
	OldString            string               `json:"old_string,omitempty" jsonschema:"exact text to replace for a single text edit"`
	NewString            *string              `json:"new_string,omitempty" jsonschema:"replacement text for a single edit; may be empty"`
	ExpectedReplacements int                  `json:"expected_replacements,omitempty" jsonschema:"exact occurrence count for a single edit; defaults to 1"`
	Edits                []EditBlockEditInput `json:"edits,omitempty" jsonschema:"atomic multi-hunk exact edits; use instead of old_string/new_string"`
	Range                string               `json:"range,omitempty" jsonschema:"reserved for structured file range edits"`
	Content              any                  `json:"content,omitempty" jsonschema:"reserved replacement content for structured file range edits"`
	Options              map[string]any       `json:"options,omitempty" jsonschema:"format-specific edit options"`
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
	return t.manager.ListDirectoryWithOptions(ctx, fsmgr.DirectoryRequest{Path: in.Path, Depth: in.Depth, MaxEntries: in.MaxEntries, IncludePruned: in.IncludePruned})
}

func (t *FilesystemTools) moveFile(_ context.Context, in MoveFileInput) (fsmgr.MoveResult, error) {
	return t.manager.Move(in.Source, in.Destination)
}

func (t *FilesystemTools) getFileInfo(_ context.Context, in GetFileInfoInput) (fsmgr.FileInfo, error) {
	return t.manager.Info(in.Path)
}

func (t *FilesystemTools) editBlock(ctx context.Context, in EditBlockInput) (fsmgr.EditResult, error) {
	textMode := in.OldString != "" && in.NewString != nil
	batchMode := len(in.Edits) > 0
	structuredMode := in.Range != "" && in.Content != nil
	modes := 0
	for _, enabled := range []bool{textMode, batchMode, structuredMode} {
		if enabled {
			modes++
		}
	}
	if modes != 1 {
		return fsmgr.EditResult{}, errors.New("provide exactly one edit mode: old_string + new_string, edits, or range + content")
	}
	newString := ""
	if in.NewString != nil {
		newString = *in.NewString
	}
	edits := make([]fsmgr.TextEdit, 0, len(in.Edits))
	for i, edit := range in.Edits {
		if edit.OldString == "" || edit.NewString == nil {
			return fsmgr.EditResult{}, fmt.Errorf("edits[%d] requires old_string and new_string", i)
		}
		edits = append(edits, fsmgr.TextEdit{OldString: edit.OldString, NewString: *edit.NewString, ExpectedReplacements: edit.ExpectedReplacements})
	}
	return t.manager.Edit(ctx, fsmgr.EditRequest{
		Path: in.FilePath, OldString: in.OldString, NewString: newString, Edits: edits,
		ExpectedReplacements: in.ExpectedReplacements, Range: in.Range, Content: in.Content, Options: in.Options,
	})
}
