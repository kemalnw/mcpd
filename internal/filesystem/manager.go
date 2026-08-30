package filesystem

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	defaultReadLines   = 1000
	defaultMaxLine     = 1 << 20
	defaultNestedLimit = 100
	defaultRemoteBytes = 16 << 20
)

type Manager struct {
	opts Options
	http *http.Client
}

func NewManager(opts Options) (*Manager, error) {
	if opts.DefaultReadLines < 0 || opts.MaxLineBytes < 0 || opts.NestedEntryLimit < 0 || opts.HTTPTimeout < 0 || opts.MaxRemoteBytes < 0 {
		return nil, errors.New("invalid filesystem manager limits")
	}
	if opts.DefaultReadLines == 0 {
		opts.DefaultReadLines = defaultReadLines
	}
	if opts.MaxLineBytes == 0 {
		opts.MaxLineBytes = defaultMaxLine
	}
	if opts.NestedEntryLimit == 0 {
		opts.NestedEntryLimit = defaultNestedLimit
	}
	if opts.HTTPTimeout == 0 {
		opts.HTTPTimeout = 15 * time.Second
	}
	if opts.MaxRemoteBytes == 0 {
		opts.MaxRemoteBytes = defaultRemoteBytes
	}
	return &Manager{opts: opts, http: &http.Client{Timeout: opts.HTTPTimeout}}, nil
}

func (m *Manager) Read(ctx context.Context, req ReadRequest) (ReadResult, error) {
	if strings.TrimSpace(req.Path) == "" {
		return ReadResult{}, errors.New("path must not be empty")
	}
	if req.IsURL {
		return m.readURL(ctx, req.Path)
	}
	info, err := os.Stat(req.Path)
	if err != nil {
		return ReadResult{}, fmt.Errorf("stat %q: %w", req.Path, err)
	}
	if info.IsDir() {
		return ReadResult{}, fmt.Errorf("read %q: path is a directory; use list_directory", req.Path)
	}
	fileType, mimeType, err := detectFileType(req.Path)
	if err != nil {
		return ReadResult{}, err
	}
	if fileType != FileTypeText {
		return ReadResult{}, fmt.Errorf("read %q: %w: %s", req.Path, ErrUnsupportedFormat, fileType)
	}
	return m.readTextFile(ctx, req, info.Size(), mimeType)
}

func (m *Manager) ReadMultiple(ctx context.Context, paths []string) []MultiReadResult {
	out := make([]MultiReadResult, 0, len(paths))
	for _, path := range paths {
		result, err := m.Read(ctx, ReadRequest{Path: path})
		item := MultiReadResult{Path: path}
		if err != nil {
			item.Error = err.Error()
		} else {
			item.Result = &result
		}
		out = append(out, item)
	}
	return out
}

func (m *Manager) readURL(ctx context.Context, rawURL string) (ReadResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return ReadResult{}, fmt.Errorf("build URL request: %w", err)
	}
	resp, err := m.http.Do(req)
	if err != nil {
		return ReadResult{}, fmt.Errorf("fetch %q: %w", rawURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ReadResult{}, fmt.Errorf("fetch %q: HTTP %s", rawURL, resp.Status)
	}
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0]))
	if contentType != "" && !strings.HasPrefix(contentType, "text/") && contentType != "application/json" && contentType != "application/xml" && contentType != "application/javascript" {
		return ReadResult{}, fmt.Errorf("fetch %q: %w: content type %s", rawURL, ErrUnsupportedFormat, contentType)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, m.opts.MaxRemoteBytes+1))
	if err != nil {
		return ReadResult{}, fmt.Errorf("read URL response: %w", err)
	}
	if int64(len(body)) > m.opts.MaxRemoteBytes {
		return ReadResult{}, fmt.Errorf("fetch %q: response exceeds %d bytes", rawURL, m.opts.MaxRemoteBytes)
	}
	if bytes.IndexByte(body, 0) >= 0 || !utf8.Valid(body) {
		return ReadResult{}, fmt.Errorf("fetch %q: %w: response is binary", rawURL, ErrUnsupportedFormat)
	}
	content := string(body)
	lines := splitLines(content)
	return ReadResult{Path: rawURL, Source: "url", FileType: FileTypeText, MIMEType: contentType, Content: content, ReadCount: len(lines), TotalLines: len(lines), Size: int64(len(body))}, nil
}

func detectFileType(path string) (string, string, error) {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return FileTypeImage, mime.TypeByExtension(ext), nil
	case ".xlsx", ".xls", ".xlsm":
		return FileTypeExcel, mime.TypeByExtension(ext), nil
	case ".pdf":
		return FileTypePDF, "application/pdf", nil
	case ".docx":
		return FileTypeDOCX, "application/vnd.openxmlformats-officedocument.wordprocessingml.document", nil
	}
	f, err := os.Open(path)
	if err != nil {
		return "", "", fmt.Errorf("open %q: %w", path, err)
	}
	defer f.Close()
	buf := make([]byte, 8192)
	n, err := f.Read(buf)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", "", fmt.Errorf("inspect %q: %w", path, err)
	}
	buf = buf[:n]
	if bytes.IndexByte(buf, 0) >= 0 || !utf8.Valid(buf) {
		return FileTypeBinary, http.DetectContentType(buf), nil
	}
	mimeType := mime.TypeByExtension(ext)
	if mimeType == "" && n > 0 {
		mimeType = http.DetectContentType(buf)
	}
	return FileTypeText, mimeType, nil
}
