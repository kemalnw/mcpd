package filesystem

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

const fuzzyThreshold = 0.70

func (m *Manager) Edit(ctx context.Context, req EditRequest) (EditResult, error) {
	if req.Path == "" {
		return EditResult{}, fmt.Errorf("%w: file_path must not be empty", ErrInvalidEdit)
	}
	if req.Range != "" || req.Content != nil {
		return EditResult{}, fmt.Errorf("%w: range/content editing is reserved for structured file handlers", ErrUnsupportedFormat)
	}
	if len(req.Edits) > 0 {
		if req.OldString != "" {
			return EditResult{}, fmt.Errorf("%w: provide either edits or old_string/new_string, not both", ErrInvalidEdit)
		}
		return m.editMany(ctx, req.Path, req.Edits)
	}
	if req.OldString == "" {
		return EditResult{}, fmt.Errorf("%w: old_string must not be empty", ErrInvalidEdit)
	}
	return m.editOne(ctx, req.Path, TextEdit{OldString: req.OldString, NewString: req.NewString, ExpectedReplacements: req.ExpectedReplacements})
}

func (m *Manager) editOne(ctx context.Context, path string, edit TextEdit) (EditResult, error) {
	if edit.ExpectedReplacements == 0 {
		edit.ExpectedReplacements = 1
	}
	if edit.ExpectedReplacements < 1 {
		return EditResult{}, fmt.Errorf("%w: expected_replacements must be at least 1", ErrInvalidEdit)
	}
	content, err := m.readEditableText(path)
	if err != nil {
		return EditResult{}, err
	}
	count := strings.Count(content, edit.OldString)
	if count > 0 && count != edit.ExpectedReplacements {
		return EditResult{
			Path: path, Applied: false, Replacements: count, ExpectedReplacements: edit.ExpectedReplacements,
			Message: fmt.Sprintf("expected %d occurrences but found %d; make old_string more specific or set expected_replacements to %d", edit.ExpectedReplacements, count, count),
		}, nil
	}
	if count == edit.ExpectedReplacements {
		replaced := strings.ReplaceAll(content, edit.OldString, edit.NewString)
		if err := rewritePreservingLinks(path, []byte(replaced)); err != nil {
			return EditResult{}, err
		}
		return EditResult{
			Path: path, Applied: true, Replacements: count, ExpectedReplacements: edit.ExpectedReplacements,
			Message: fmt.Sprintf("replaced %d occurrence(s)", count),
		}, nil
	}

	closest, similarity, err := closestText(ctx, content, edit.OldString)
	if err != nil {
		return EditResult{}, err
	}
	result := EditResult{
		Path: path, Applied: false, ExpectedReplacements: edit.ExpectedReplacements,
		ClosestMatch: closest, Similarity: similarity, Diff: compactDiff(edit.OldString, closest),
	}
	if similarity >= fuzzyThreshold {
		result.Message = fmt.Sprintf("exact match not found; closest text is %.0f%% similar. Retry using the exact closest_match text", similarity*100)
	} else {
		result.Message = fmt.Sprintf("search content not found; closest text is only %.0f%% similar, below the %.0f%% fuzzy threshold", similarity*100, fuzzyThreshold*100)
	}
	return result, nil
}

type replacementRange struct {
	start int
	end   int
	text  string
	hunk  int
}

func (m *Manager) editMany(ctx context.Context, path string, edits []TextEdit) (EditResult, error) {
	if len(edits) == 0 {
		return EditResult{}, fmt.Errorf("%w: edits must not be empty", ErrInvalidEdit)
	}
	content, err := m.readEditableText(path)
	if err != nil {
		return EditResult{}, err
	}
	results := make([]EditHunkResult, len(edits))
	ranges := make([]replacementRange, 0, len(edits))
	totalExpected := 0
	for i, edit := range edits {
		if err := ctx.Err(); err != nil {
			return EditResult{}, err
		}
		if edit.OldString == "" {
			return EditResult{}, fmt.Errorf("%w: edits[%d].old_string must not be empty", ErrInvalidEdit, i)
		}
		if edit.ExpectedReplacements == 0 {
			edit.ExpectedReplacements = 1
		}
		if edit.ExpectedReplacements < 1 {
			return EditResult{}, fmt.Errorf("%w: edits[%d].expected_replacements must be at least 1", ErrInvalidEdit, i)
		}
		totalExpected += edit.ExpectedReplacements
		occurrences := findOccurrences(content, edit.OldString)
		hunk := EditHunkResult{Index: i, Replacements: len(occurrences), ExpectedReplacements: edit.ExpectedReplacements}
		if len(occurrences) != edit.ExpectedReplacements {
			if len(occurrences) == 0 {
				closest, similarity, err := closestText(ctx, content, edit.OldString)
				if err != nil {
					return EditResult{}, err
				}
				hunk.ClosestMatch = closest
				hunk.Similarity = similarity
				hunk.Diff = compactDiff(edit.OldString, closest)
				hunk.Message = fmt.Sprintf("exact match not found; closest text is %.0f%% similar", similarity*100)
			} else {
				hunk.Message = fmt.Sprintf("expected %d occurrences but found %d", edit.ExpectedReplacements, len(occurrences))
			}
			results[i] = hunk
			return EditResult{Path: path, Applied: false, Replacements: 0, ExpectedReplacements: totalExpected, Edits: results, Message: fmt.Sprintf("edit %d validation failed; file unchanged", i)}, nil
		}
		hunk.Message = fmt.Sprintf("validated %d occurrence(s)", len(occurrences))
		results[i] = hunk
		for _, start := range occurrences {
			ranges = append(ranges, replacementRange{start: start, end: start + len(edit.OldString), text: edit.NewString, hunk: i})
		}
	}

	sort.Slice(ranges, func(i, j int) bool {
		if ranges[i].start == ranges[j].start {
			return ranges[i].end < ranges[j].end
		}
		return ranges[i].start < ranges[j].start
	})
	for i := 1; i < len(ranges); i++ {
		if ranges[i].start < ranges[i-1].end {
			left, right := ranges[i-1].hunk, ranges[i].hunk
			results[left].Message = fmt.Sprintf("overlaps edit %d", right)
			results[right].Message = fmt.Sprintf("overlaps edit %d", left)
			return EditResult{Path: path, Applied: false, ExpectedReplacements: totalExpected, Edits: results, Message: fmt.Sprintf("edits %d and %d overlap; file unchanged", left, right)}, nil
		}
	}

	replaced := content
	for i := len(ranges) - 1; i >= 0; i-- {
		r := ranges[i]
		replaced = replaced[:r.start] + r.text + replaced[r.end:]
	}
	if err := rewritePreservingLinks(path, []byte(replaced)); err != nil {
		return EditResult{}, err
	}
	for i := range results {
		results[i].Applied = true
		results[i].Message = fmt.Sprintf("replaced %d occurrence(s)", results[i].Replacements)
	}
	return EditResult{Path: path, Applied: true, Replacements: len(ranges), ExpectedReplacements: totalExpected, Edits: results, Message: fmt.Sprintf("applied %d edit hunk(s) with %d replacement(s)", len(edits), len(ranges))}, nil
}

func (m *Manager) readEditableText(path string) (string, error) {
	fileType, _, err := detectFileType(path)
	if err != nil {
		return "", err
	}
	if fileType != FileTypeText {
		return "", fmt.Errorf("edit %q: %w: %s", path, ErrUnsupportedFormat, fileType)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %q for edit: %w", path, err)
	}
	return string(data), nil
}

func findOccurrences(content, needle string) []int {
	if needle == "" {
		return nil
	}
	var out []int
	for offset := 0; offset <= len(content)-len(needle); {
		i := strings.Index(content[offset:], needle)
		if i < 0 {
			break
		}
		start := offset + i
		out = append(out, start)
		offset = start + len(needle)
	}
	return out
}

func rewritePreservingLinks(path string, data []byte) error {
	lstat, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("lstat %q before edit: %w", path, err)
	}
	target := path
	if lstat.Mode()&os.ModeSymlink != 0 {
		target, err = filepath.EvalSymlinks(path)
		if err != nil {
			return fmt.Errorf("resolve symlink %q: %w", path, err)
		}
	}
	info, err := os.Stat(target)
	if err != nil {
		return fmt.Errorf("stat %q before edit: %w", target, err)
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Nlink > 1 {
		return rewriteInPlace(target, data, info.Mode().Perm())
	}
	return atomicRewrite(target, data)
}

func rewriteInPlace(path string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("open %q for in-place edit: %w", path, err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %q in place: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("sync %q: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %q: %w", path, err)
	}
	return nil
}

func atomicRewrite(path string, data []byte) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat %q before edit: %w", path, err)
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".mcpd-edit-*")
	if err != nil {
		return fmt.Errorf("create edit temp file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}
	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		cleanup()
		return fmt.Errorf("preserve file permissions: %w", err)
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		_ = tmp.Chown(int(stat.Uid), int(stat.Gid))
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("write edit temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("sync edit temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close edit temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("replace %q atomically: %w", path, err)
	}
	if dirHandle, err := os.Open(dir); err == nil {
		_ = dirHandle.Sync()
		_ = dirHandle.Close()
	}
	return nil
}

func closestText(ctx context.Context, content, query string) (string, float64, error) {
	if query == "" || content == "" {
		return "", 0, nil
	}
	contentLines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	queryLines := strings.Split(strings.ReplaceAll(query, "\r\n", "\n"), "\n")
	baseWidth := len(queryLines)
	widths := []int{baseWidth}
	if baseWidth > 1 {
		widths = append(widths, baseWidth-1)
	}
	widths = append(widths, baseWidth+1)

	best := ""
	bestScore := -1.0
	checks := 0
	for _, width := range widths {
		if width <= 0 || width > len(contentLines) {
			continue
		}
		for start := 0; start+width <= len(contentLines); start++ {
			checks++
			if checks%128 == 0 {
				if err := ctx.Err(); err != nil {
					return "", 0, err
				}
			}
			candidate := strings.Join(contentLines[start:start+width], "\n")
			score := similarityRatio(query, candidate)
			if score > bestScore {
				best, bestScore = candidate, score
				if score == 1 {
					return best, bestScore, nil
				}
			}
		}
	}
	if bestScore < 0 {
		return "", 0, nil
	}
	return best, bestScore, nil
}

func similarityRatio(a, b string) float64 {
	ar, br := []rune(a), []rune(b)
	maxLen := len(ar)
	if len(br) > maxLen {
		maxLen = len(br)
	}
	if maxLen == 0 {
		return 1
	}
	d := levenshtein(ar, br)
	return 1 - float64(d)/float64(maxLen)
}

func levenshtein(a, b []rune) int {
	if len(a) > len(b) {
		a, b = b, a
	}
	prev := make([]int, len(a)+1)
	curr := make([]int, len(a)+1)
	for i := range prev {
		prev[i] = i
	}
	for j, rb := range b {
		curr[0] = j + 1
		for i, ra := range a {
			cost := 0
			if ra != rb {
				cost = 1
			}
			curr[i+1] = min3(curr[i]+1, prev[i+1]+1, prev[i]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(a)]
}

func min3(a, b, c int) int {
	if a < b {
		if a < c {
			return a
		}
		return c
	}
	if b < c {
		return b
	}
	return c
}

func compactDiff(expected, actual string) string {
	if expected == actual {
		return expected
	}
	er, ar := []rune(expected), []rune(actual)
	prefix := 0
	for prefix < len(er) && prefix < len(ar) && er[prefix] == ar[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(er)-prefix && suffix < len(ar)-prefix && er[len(er)-1-suffix] == ar[len(ar)-1-suffix] {
		suffix++
	}
	expectedMid := string(er[prefix : len(er)-suffix])
	actualMid := string(ar[prefix : len(ar)-suffix])
	end := ""
	if suffix > 0 {
		end = string(er[len(er)-suffix:])
	}
	return string(er[:prefix]) + "{-" + expectedMid + "-}{+" + actualMid + "+}" + end
}
