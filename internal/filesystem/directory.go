package filesystem

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

const defaultDirectoryMaxEntries = 1000

var defaultPrunedDirectoryNames = map[string]struct{}{
	"node_modules": {}, "vendor": {}, ".cache": {}, "__pycache__": {}, ".next": {}, ".nuxt": {}, "dist": {}, "build": {}, "target": {},
}

func (m *Manager) ListDirectory(ctx context.Context, root string, depth int) (DirectoryResult, error) {
	return m.ListDirectoryWithOptions(ctx, DirectoryRequest{Path: root, Depth: depth})
}

func (m *Manager) ListDirectoryWithOptions(ctx context.Context, req DirectoryRequest) (DirectoryResult, error) {
	root := req.Path
	depth := req.Depth
	if root == "" {
		return DirectoryResult{}, errors.New("path must not be empty")
	}
	if depth <= 0 {
		depth = 2
	}
	maxEntries := req.MaxEntries
	if maxEntries == 0 {
		maxEntries = defaultDirectoryMaxEntries
	}
	if maxEntries < 1 {
		return DirectoryResult{}, errors.New("max_entries must be at least 1")
	}
	info, err := os.Stat(root)
	if err != nil {
		return DirectoryResult{}, fmt.Errorf("stat directory %q: %w", root, err)
	}
	if !info.IsDir() {
		return DirectoryResult{}, fmt.Errorf("%q is not a directory", root)
	}
	result := DirectoryResult{Root: root, Depth: depth, MaxEntries: maxEntries}
	m.listDirectoryRecursive(ctx, root, "", depth, 1, true, req.IncludePruned, &result)
	return result, nil
}

func (m *Manager) listDirectoryRecursive(ctx context.Context, current, relative string, remainingDepth, currentDepth int, top, includePruned bool, result *DirectoryResult) bool {
	if remainingDepth <= 0 || ctx.Err() != nil || result.Truncated {
		return !result.Truncated
	}
	entries, err := os.ReadDir(current)
	if err != nil {
		return appendDirectoryEntry(result, DirectoryEntry{Path: relative, Type: "denied", Depth: currentDepth, Error: err.Error()})
	}
	visible := entries
	hidden := 0
	if !top && len(entries) > m.opts.NestedEntryLimit {
		visible = entries[:m.opts.NestedEntryLimit]
		hidden = len(entries) - len(visible)
	}
	for _, entry := range visible {
		if ctx.Err() != nil || result.Truncated {
			return false
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
		if !appendDirectoryEntry(result, item) {
			return false
		}
		if entry.IsDir() && remainingDepth > 1 {
			if !includePruned && shouldPruneDirectory(rel, entry.Name()) {
				result.Pruned = append(result.Pruned, rel)
				continue
			}
			if !m.listDirectoryRecursive(ctx, filepath.Join(current, entry.Name()), rel, remainingDepth-1, currentDepth+1, false, includePruned, result) {
				return false
			}
		}
	}
	if hidden > 0 && !result.Truncated {
		label := relative
		if label == "" {
			label = filepath.Base(current)
		}
		appendDirectoryEntry(result, DirectoryEntry{Path: label, Type: "warning", Depth: currentDepth, Hidden: hidden})
	}
	return !result.Truncated
}

func appendDirectoryEntry(result *DirectoryResult, entry DirectoryEntry) bool {
	if len(result.Entries) >= result.MaxEntries {
		result.Truncated = true
		return false
	}
	result.Entries = append(result.Entries, entry)
	return true
}

func shouldPruneDirectory(relative, name string) bool {
	clean := filepath.ToSlash(relative)
	if clean == ".git/objects" || strings.HasPrefix(clean, ".git/objects/") {
		return true
	}
	_, prune := defaultPrunedDirectoryNames[name]
	return prune
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
