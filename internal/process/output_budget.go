package process

import "strings"

const maxRequestedResponseBytes = 2 << 20

func effectiveResponseBytes(requested, configured int) (int, error) {
	if requested < 0 {
		return 0, errNegativeMaxBytes
	}
	if requested == 0 {
		return configured, nil
	}
	if requested > maxRequestedResponseBytes {
		requested = maxRequestedResponseBytes
	}
	return requested, nil
}

type lineBudgetResult struct {
	lines         []string
	streams       []StreamLine
	consumedLines int
	bytesReturned int
	truncated     bool
	omittedBytes  int
}

func fitForwardBudget(lines []string, streams []StreamLine, separate bool, maxBytes int) lineBudgetResult {
	out := lineBudgetResult{}
	if maxBytes <= 0 || len(lines) == 0 {
		out.truncated = len(lines) > 0
		return out
	}
	for i, line := range lines {
		cost := len(line) + 1
		if out.bytesReturned+cost <= maxBytes {
			out.lines = append(out.lines, line)
			if separate && i < len(streams) {
				out.streams = append(out.streams, streams[i])
			}
			out.bytesReturned += cost
			out.consumedLines++
			continue
		}
		out.truncated = true
		if out.consumedLines == 0 {
			// Return a bounded preview but do not advance the cursor through a
			// partially delivered retained line. The caller can retry with a larger
			// max_bytes value and recover the full authoritative line.
			budget := maxBytes - len(" [line truncated]") - 1
			if budget < 0 {
				budget = 0
			}
			truncatedLine := truncateUTF8Prefix(line, budget) + " [line truncated]"
			if len(truncatedLine)+1 > maxBytes {
				truncatedLine = truncateUTF8Prefix(truncatedLine, maxBytes)
			}
			out.lines = append(out.lines, truncatedLine)
			if separate && i < len(streams) {
				stream := streams[i]
				stream.Text = truncatedLine
				out.streams = append(out.streams, stream)
			}
			out.bytesReturned = min(maxBytes, len(truncatedLine)+1)
			out.omittedBytes += max(0, len(line)-len(truncatedLine))
		}
		break
	}
	if out.consumedLines < len(lines) {
		out.truncated = true
	}
	return out
}

// fitTailBudget returns the newest complete lines that fit, truncating only the
// newest single line when one line alone exceeds the budget.
func fitTailBudget(lines []string, streams []StreamLine, separate bool, maxLines, maxBytes int) lineBudgetResult {
	if maxLines <= 0 || len(lines) == 0 || maxBytes <= 0 {
		return lineBudgetResult{truncated: len(lines) > 0}
	}
	start := 0
	if len(lines) > maxLines {
		start = len(lines) - maxLines
	}
	used := 0
	chosenStart := len(lines)
	for i := len(lines) - 1; i >= start; i-- {
		cost := len(lines[i]) + 1
		if used+cost > maxBytes {
			break
		}
		used += cost
		chosenStart = i
	}
	if chosenStart == len(lines) {
		line := lines[len(lines)-1]
		marker := "[tail truncated] "
		budget := maxBytes - len(marker)
		if budget < 0 {
			budget = 0
		}
		text := marker + truncateUTF8Suffix(line, budget)
		if len(text) > maxBytes {
			text = truncateUTF8Prefix(text, maxBytes)
		}
		out := lineBudgetResult{lines: []string{text}, consumedLines: 1, bytesReturned: len(text), truncated: true, omittedBytes: max(0, len(line)-len(text))}
		if separate && len(streams) > 0 {
			stream := streams[len(streams)-1]
			stream.Text = text
			out.streams = []StreamLine{stream}
		}
		return out
	}
	out := lineBudgetResult{lines: append([]string(nil), lines[chosenStart:]...), consumedLines: len(lines) - chosenStart, bytesReturned: used, truncated: chosenStart > 0}
	if separate {
		out.streams = append([]StreamLine(nil), streams[chosenStart:]...)
	}
	return out
}

func truncateUTF8Prefix(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	for n > 0 && (s[n]&0xc0) == 0x80 {
		n--
	}
	return s[:n]
}

func truncateUTF8Suffix(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	start := len(s) - n
	for start < len(s) && (s[start]&0xc0) == 0x80 {
		start++
	}
	return s[start:]
}

var errNegativeMaxBytes = &responseBudgetError{"max_bytes must be >= 0"}

type responseBudgetError struct{ message string }

func (e *responseBudgetError) Error() string { return e.message }

func outputBytes(lines []string) int {
	n := 0
	for _, line := range lines {
		n += len(line) + 1
	}
	return n
}

func hasFailureText(state BatchJobState) bool {
	return state == BatchJobFailed || state == BatchJobStartFailed || strings.Contains(string(state), "failed")
}
