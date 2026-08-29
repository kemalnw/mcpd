package filesystem

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func (m *Manager) CreateDirectory(path string) error {
	if path == "" {
		return errors.New("path must not be empty")
	}
	if err := os.MkdirAll(path, 0o777); err != nil {
		return fmt.Errorf("create directory %q: %w", path, err)
	}
	return nil
}

func (m *Manager) ListDirectory(ctx context.Context, root string, depth int) (DirectoryResult, error) {
	if root == "" {
		return DirectoryResult{}, errors.New("path must not be empty")
	}
	if depth <= 0 {
		depth = 2
	}
	info, err := os.Stat(root)
	if err != nil {
		return DirectoryResult{}, fmt.Errorf("stat directory %q: %w", root, err)
	}
	if !info.IsDir() {
		return DirectoryResult{}, fmt.Errorf("%q is not a directory", root)
	}
	result := DirectoryResult{Root: root, Depth: depth}
	m.listDirectoryRecursive(ctx, root, "", depth, 1, true, &result)
	return result, nil
}

func (m *Manager) listDirectoryRecursive(ctx context.Context, current, relative string, remainingDepth, currentDepth int, top bool, result *DirectoryResult) {
	if remainingDepth <= 0 || ctx.Err() != nil {
		return
	}
	entries, err := os.ReadDir(current)
	if err != nil {
		result.Entries = append(result.Entries, DirectoryEntry{Path: relative, Type: "denied", Depth: currentDepth, Error: err.Error()})
		return
	}
	visible := entries
	hidden := 0
	if !top && len(entries) > m.opts.NestedEntryLimit {
		visible = entries[:m.opts.NestedEntryLimit]
		hidden = len(entries) - len(visible)
	}
	for _, entry := range visible {
		if ctx.Err() != nil {
			return
		}
		rel := entry.Name()
		if relative != "" {
			rel = filepath.Join(relative, entry.Name())
		}
		item := DirectoryEntry{Path: rel, Type: "file", Depth: currentDepth}
		if entry.IsDir() {
			item.Type = "directory"
		} else if info, err := entry.Info(); err == nil {
			item.Size = info.Size()
		} else {
			item.Error = err.Error()
		}
		result.Entries = append(result.Entries, item)
		if entry.IsDir() && remainingDepth > 1 {
			m.listDirectoryRecursive(ctx, filepath.Join(current, entry.Name()), rel, remainingDepth-1, currentDepth+1, false, result)
		}
	}
	if hidden > 0 {
		label := relative
		if label == "" {
			label = filepath.Base(current)
		}
		result.Entries = append(result.Entries, DirectoryEntry{Path: label, Type: "warning", Depth: currentDepth, Hidden: hidden})
	}
}

func (m *Manager) Move(source, destination string) (MoveResult, error) {
	if source == "" || destination == "" {
		return MoveResult{}, errors.New("source and destination must not be empty")
	}
	if err := os.Rename(source, destination); err != nil {
		return MoveResult{}, fmt.Errorf("move %q to %q: %w", source, destination, err)
	}
	return MoveResult{Source: source, Destination: destination}, nil
}
