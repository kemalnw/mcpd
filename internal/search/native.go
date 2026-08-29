package search

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

func (m *Manager) runNative(ctx context.Context, s *session) (bool, error) {
	if s.opts.SearchType == TypeFiles {
		return m.runNativeFiles(ctx, s)
	}
	return m.runNativeContent(ctx, s)
}

func (m *Manager) runNativeFiles(ctx context.Context, s *session) (bool, error) {
	incomplete := false
	err := filepath.WalkDir(s.opts.RootPath, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			if errors.Is(walkErr, fs.ErrPermission) {
				incomplete = true
				return nil
			}
			return walkErr
		}
		if path == s.opts.RootPath {
			return nil
		}
		if entry.IsDir() {
			if !s.opts.IncludeHidden && strings.HasPrefix(entry.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !s.opts.IncludeHidden && strings.HasPrefix(entry.Name(), ".") {
			return nil
		}
		rel, err := filepath.Rel(s.opts.RootPath, path)
		if err != nil {
			return err
		}
		if !matchesFilePatterns(rel, s.opts.FilePattern, s.opts.IgnoreCase) || !matchesSearchFilename(rel, s.opts.Pattern, s.opts.IgnoreCase) {
			return nil
		}
		if !s.add(Result{File: path, Type: "file"}) || s.matchLimitReached() {
			return errStopSearch
		}
		if s.opts.EarlyTermination && isExactFilename(s.opts.Pattern) && exactFilenameMatch(path, s.opts.Pattern, s.opts.IgnoreCase) {
			return errStopSearch
		}
		return nil
	})
	if errors.Is(err, errStopSearch) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return incomplete, nil
	}
	return incomplete, err
}

func (m *Manager) runNativeContent(ctx context.Context, s *session) (bool, error) {
	matcher, err := newContentMatcher(s.opts.Pattern, s.opts.LiteralSearch, s.opts.IgnoreCase)
	if err != nil {
		return false, err
	}
	incomplete := false
	walkErr := filepath.WalkDir(s.opts.RootPath, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			if errors.Is(walkErr, fs.ErrPermission) {
				incomplete = true
				return nil
			}
			return walkErr
		}
		if entry.IsDir() {
			if path != s.opts.RootPath && !s.opts.IncludeHidden && strings.HasPrefix(entry.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !s.opts.IncludeHidden && strings.HasPrefix(entry.Name(), ".") {
			return nil
		}
		if !matchesFilePatterns(path, s.opts.FilePattern, s.opts.IgnoreCase) {
			return nil
		}
		if err := searchTextFile(ctx, s, path, matcher); err != nil {
			if errors.Is(err, errStopSearch) {
				return err
			}
			if errors.Is(err, fs.ErrPermission) {
				incomplete = true
				return nil
			}
			// Binary and oversized-line files are skipped by the native fallback.
			return nil
		}
		if s.matchLimitReached() {
			return errStopSearch
		}
		return nil
	})
	if errors.Is(walkErr, errStopSearch) || errors.Is(walkErr, context.Canceled) || errors.Is(walkErr, context.DeadlineExceeded) {
		return incomplete, nil
	}
	return incomplete, walkErr
}

var errStopSearch = errors.New("search complete")

type contentMatcher struct {
	literal    string
	ignoreCase bool
	re         *regexp.Regexp
}

func newContentMatcher(pattern string, literal, ignoreCase bool) (contentMatcher, error) {
	if literal {
		if ignoreCase {
			pattern = strings.ToLower(pattern)
		}
		return contentMatcher{literal: pattern, ignoreCase: ignoreCase}, nil
	}
	if ignoreCase {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return contentMatcher{}, fmt.Errorf("compile search pattern: %w", err)
	}
	return contentMatcher{re: re}, nil
}

func (m contentMatcher) find(line string) (string, bool) {
	if m.re != nil {
		loc := m.re.FindStringIndex(line)
		if loc == nil {
			return "", false
		}
		return line[loc[0]:loc[1]], true
	}
	haystack := line
	if m.ignoreCase {
		haystack = strings.ToLower(haystack)
	}
	idx := strings.Index(haystack, m.literal)
	if idx < 0 {
		return "", false
	}
	return line[idx : idx+len(m.literal)], true
}

func searchTextFile(ctx context.Context, s *session, path string, matcher contentMatcher) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	lines := make([]string, 0, 256)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := scanner.Text()
		if strings.IndexByte(line, 0) >= 0 || !utf8.ValidString(line) {
			return errors.New("binary file")
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		return err
	}

	remaining := s.remainingMatchSlots()
	if remaining == 0 {
		return errStopSearch
	}
	matches := make(map[int]string)
	for i, line := range lines {
		match, ok := matcher.find(line)
		if !ok {
			continue
		}
		matches[i] = match
		remaining--
		if remaining == 0 {
			break
		}
	}
	if len(matches) == 0 {
		return nil
	}

	included := make(map[int]bool)
	for i := range matches {
		start := i - s.opts.ContextLines
		if start < 0 {
			start = 0
		}
		end := i + s.opts.ContextLines
		if end >= len(lines) {
			end = len(lines) - 1
		}
		for n := start; n <= end; n++ {
			included[n] = true
		}
	}
	for i, line := range lines {
		if !included[i] {
			continue
		}
		if match, ok := matches[i]; ok {
			if !s.add(Result{File: path, Line: i + 1, Match: match, Type: "content"}) {
				return errStopSearch
			}
			continue
		}
		if !s.add(Result{File: path, Line: i + 1, Match: line, Type: "content", IsContext: true}) {
			return errStopSearch
		}
	}
	if s.matchLimitReached() {
		return errStopSearch
	}
	return nil
}

func matchesSearchFilename(path, pattern string, ignoreCase bool) bool {
	candidate := filepath.ToSlash(path)
	base := filepath.Base(candidate)
	if isGlobPattern(pattern) || isExactFilename(pattern) {
		return globMatch(pattern, candidate, base, ignoreCase)
	}
	if ignoreCase {
		return strings.Contains(strings.ToLower(candidate), strings.ToLower(pattern))
	}
	return strings.Contains(candidate, pattern)
}

func matchesFilePatterns(path, patterns string, ignoreCase bool) bool {
	parts := splitPatterns(patterns)
	if len(parts) == 0 {
		return true
	}
	candidate := filepath.ToSlash(path)
	base := filepath.Base(candidate)
	for _, pattern := range parts {
		if globMatch(pattern, candidate, base, ignoreCase) {
			return true
		}
	}
	return false
}

func globMatch(pattern, path, base string, ignoreCase bool) bool {
	if ignoreCase {
		pattern, path, base = strings.ToLower(pattern), strings.ToLower(path), strings.ToLower(base)
	}
	if ok, _ := filepath.Match(pattern, base); ok {
		return true
	}
	ok, _ := filepath.Match(filepath.FromSlash(pattern), filepath.FromSlash(path))
	return ok
}
