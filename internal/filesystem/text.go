package filesystem

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
)

func (m *Manager) readTextFile(ctx context.Context, req ReadRequest, size int64, mimeType string) (ReadResult, error) {
	length := req.Length
	if length <= 0 {
		length = m.opts.DefaultReadLines
	}
	f, err := os.Open(req.Path)
	if err != nil {
		return ReadResult{}, fmt.Errorf("open %q: %w", req.Path, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64<<10), m.opts.MaxLineBytes)
	var selected []string
	var tail []string
	requestedTail := 0
	if req.Offset < 0 {
		requestedTail = -req.Offset
		if requestedTail > 0 {
			tail = make([]string, 0, requestedTail)
		}
	}
	total := 0
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return ReadResult{}, err
		}
		line := scanner.Text()
		if req.Offset < 0 {
			if requestedTail > 0 {
				if len(tail) < requestedTail {
					tail = append(tail, line)
				} else {
					copy(tail, tail[1:])
					tail[len(tail)-1] = line
				}
			}
		} else if total >= req.Offset && len(selected) < length {
			selected = append(selected, line)
		}
		total++
	}
	if err := scanner.Err(); err != nil {
		return ReadResult{}, fmt.Errorf("read %q: %w", req.Path, err)
	}

	readFrom := req.Offset
	if req.Offset < 0 {
		selected = tail
		readFrom = total - len(selected)
		if readFrom < 0 {
			readFrom = 0
		}
	}
	remaining := total - (readFrom + len(selected))
	if remaining < 0 {
		remaining = 0
	}
	return ReadResult{
		Path: req.Path, Source: "file", FileType: FileTypeText, MIMEType: mimeType,
		Content: strings.Join(selected, "\n"), Lines: selected, Offset: req.Offset,
		ReadFrom: readFrom, ReadCount: len(selected), TotalLines: total, Remaining: remaining, Size: size,
	}, nil
}

func (m *Manager) Write(_ context.Context, req WriteRequest) (WriteResult, error) {
	if req.Path == "" {
		return WriteResult{}, errors.New("path must not be empty")
	}
	mode := req.Mode
	if mode == "" {
		mode = "rewrite"
	}
	var flags int
	switch mode {
	case "rewrite":
		flags = os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	case "append":
		flags = os.O_CREATE | os.O_WRONLY | os.O_APPEND
	default:
		return WriteResult{}, fmt.Errorf("invalid write mode %q", mode)
	}
	f, err := os.OpenFile(req.Path, flags, 0o666)
	if err != nil {
		return WriteResult{}, fmt.Errorf("open %q for %s: %w", req.Path, mode, err)
	}
	n, writeErr := f.WriteString(req.Content)
	closeErr := f.Close()
	if writeErr != nil {
		return WriteResult{}, fmt.Errorf("write %q: %w", req.Path, writeErr)
	}
	if closeErr != nil {
		return WriteResult{}, fmt.Errorf("close %q: %w", req.Path, closeErr)
	}
	return WriteResult{Path: req.Path, Mode: mode, BytesWritten: n, LineCount: countLines(req.Content)}, nil
}

func splitLines(content string) []string {
	if content == "" {
		return nil
	}
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	lines := strings.Split(content, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func countLines(content string) int { return len(splitLines(content)) }
