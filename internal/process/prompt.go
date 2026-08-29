package process

import (
	"path/filepath"
	"regexp"
	"strings"
)

var promptPattern = regexp.MustCompile(`(?m)(>>> |\.\.\. |> |\$ |# |% |mysql> |sqlite> |psql[^\n]*[=#] )$`)

func looksLikePrompt(tail string) bool {
	return promptPattern.MatchString(tail)
}

func resolvePTY(mode PTYMode, command string) (bool, error) {
	switch mode {
	case "", PTYAuto:
		return commandLikelyInteractive(command), nil
	case PTYAlways:
		return true, nil
	case PTYNever:
		return false, nil
	default:
		return false, ErrInvalidPTYMode
	}
}

func commandLikelyInteractive(command string) bool {
	fields := strings.Fields(strings.TrimSpace(command))
	if len(fields) == 0 {
		return false
	}
	if fields[0] == "sudo" && len(fields) > 1 {
		fields = fields[1:]
	}
	name := filepath.Base(fields[0])
	switch name {
	case "ssh", "top", "htop", "vim", "vi", "nvim", "nano", "less", "more", "psql", "mysql", "sqlite3", "redis-cli":
		return true
	case "python", "python3", "node", "irb":
		return len(fields) == 1 || contains(fields[1:], "-i")
	case "bash", "zsh", "sh", "fish":
		return len(fields) == 1
	}
	return false
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
