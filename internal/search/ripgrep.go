package search

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

func (m *Manager) runRipgrep(ctx context.Context, s *session) (bool, error) {
	args := buildRipgrepArgs(s.opts)
	cmd := exec.CommandContext(ctx, m.rgPath, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return false, fmt.Errorf("open ripgrep stdout: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return false, fmt.Errorf("start ripgrep: %w", err)
	}

	terminatedEarly := false
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			terminatedEarly = true
			break
		}
		result, ok := parseRipgrepLine(scanner.Text(), s.opts.SearchType)
		if !ok {
			continue
		}
		if s.opts.SearchType == TypeFiles && !matchesFilePatterns(result.File, s.opts.FilePattern, s.opts.IgnoreCase) {
			continue
		}
		if !s.add(result) {
			terminatedEarly = true
			_ = cmd.Process.Signal(syscall.SIGTERM)
			break
		}
		if !result.IsContext && s.matchLimitReached() {
			terminatedEarly = true
			_ = cmd.Process.Signal(syscall.SIGTERM)
			break
		}
		if s.opts.SearchType == TypeFiles && s.opts.EarlyTermination && isExactFilename(s.opts.Pattern) && exactFilenameMatch(result.File, s.opts.Pattern, s.opts.IgnoreCase) {
			terminatedEarly = true
			_ = cmd.Process.Signal(syscall.SIGTERM)
			break
		}
	}
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return false, fmt.Errorf("read ripgrep output: %w", err)
	}

	waitErr := cmd.Wait()
	if ctx.Err() != nil || terminatedEarly {
		return false, nil
	}
	if waitErr == nil {
		return false, nil
	}
	var exitErr *exec.ExitError
	if !errors.As(waitErr, &exitErr) {
		return false, fmt.Errorf("wait for ripgrep: %w", waitErr)
	}
	code := exitErr.ExitCode()
	message := strings.TrimSpace(stderr.String())
	switch code {
	case 1:
		return false, nil // no matches
	case 2:
		return true, nil // partial search, usually permission/access errors
	default:
		if message == "" {
			message = fmt.Sprintf("ripgrep exited with code %d", code)
		}
		return false, errors.New(message)
	}
}

func buildRipgrepArgs(opts Options) []string {
	args := make([]string, 0, 16)
	if opts.SearchType == TypeContent {
		args = append(args, "--json", "--line-number")
		if opts.LiteralSearch {
			args = append(args, "-F")
		}
		if opts.ContextLines > 0 {
			args = append(args, "-C", fmt.Sprint(opts.ContextLines))
		}
		if opts.IgnoreCase {
			args = append(args, "-i")
		}
	} else {
		args = append(args, "--files")
	}
	if opts.IncludeHidden {
		args = append(args, "--hidden")
	}
	if opts.SearchType == TypeContent {
		for _, pattern := range splitPatterns(opts.FilePattern) {
			flag := "--glob"
			if opts.IgnoreCase {
				flag = "--iglob"
			}
			args = append(args, flag, pattern)
		}
	}
	if opts.SearchType == TypeFiles {
		flag := "--glob"
		if opts.IgnoreCase {
			flag = "--iglob"
		}
		pattern := opts.Pattern
		if !isExactFilename(pattern) && !isGlobPattern(pattern) {
			pattern = "*" + pattern + "*"
		}
		args = append(args, flag, pattern, opts.RootPath)
	} else {
		args = append(args, "--", opts.Pattern, opts.RootPath)
	}
	return args
}

type rgEvent struct {
	Type string `json:"type"`
	Data struct {
		Path struct {
			Text string `json:"text"`
		} `json:"path"`
		Lines struct {
			Text string `json:"text"`
		} `json:"lines"`
		LineNumber int `json:"line_number"`
		Submatches []struct {
			Match struct {
				Text string `json:"text"`
			} `json:"match"`
			Start int `json:"start"`
			End   int `json:"end"`
		} `json:"submatches"`
	} `json:"data"`
}

func parseRipgrepLine(line string, searchType Type) (Result, bool) {
	if searchType == TypeFiles {
		line = strings.TrimSpace(line)
		if line == "" {
			return Result{}, false
		}
		return Result{File: line, Type: "file"}, true
	}
	var event rgEvent
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		return Result{}, false
	}
	switch event.Type {
	case "match":
		text := strings.TrimRight(event.Data.Lines.Text, "\r\n")
		match := text
		start, end := 0, len(text)
		if len(event.Data.Submatches) > 0 && event.Data.Submatches[0].Match.Text != "" {
			match = event.Data.Submatches[0].Match.Text
			start = event.Data.Submatches[0].Start
			end = event.Data.Submatches[0].End
		}
		return Result{
			File: event.Data.Path.Text, Line: event.Data.LineNumber, Text: text, Match: match,
			Column: runeColumnAtByte(text, start), EndColumn: runeColumnAtByte(text, end), Type: "content",
		}, true
	case "context":
		return Result{File: event.Data.Path.Text, Line: event.Data.LineNumber, Text: strings.TrimRight(event.Data.Lines.Text, "\r\n"), Type: "content", IsContext: true}, true
	default:
		return Result{}, false
	}
}

func splitPatterns(patterns string) []string {
	if strings.TrimSpace(patterns) == "" {
		return nil
	}
	parts := strings.Split(patterns, "|")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, filepath.ToSlash(p))
		}
	}
	return out
}

func isGlobPattern(pattern string) bool {
	return strings.ContainsAny(pattern, "*?[]{}")
}

func isExactFilename(pattern string) bool {
	return filepath.Ext(pattern) != "" && !isGlobPattern(pattern)
}

func exactFilenameMatch(file, pattern string, ignoreCase bool) bool {
	base := filepath.Base(file)
	if ignoreCase {
		return strings.EqualFold(base, filepath.Base(pattern))
	}
	return base == filepath.Base(pattern)
}
