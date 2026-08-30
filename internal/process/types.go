package process

import "time"

type PTYMode string

const (
	PTYAuto   PTYMode = "auto"
	PTYAlways PTYMode = "always"
	PTYNever  PTYMode = "never"
)

type StartRequest struct {
	Command   string
	Shell     string
	TimeoutMS int
	PTY       PTYMode
}

type StartResult struct {
	PID             int       `json:"pid"`
	Command         string    `json:"command"`
	Shell           string    `json:"shell"`
	PTY             bool      `json:"pty"`
	State           State     `json:"state"`
	StartedAt       time.Time `json:"started_at"`
	ExitCode        *int      `json:"exit_code,omitempty"`
	Output          []string  `json:"output,omitempty"`
	ReadFrom        int       `json:"read_from"`
	ReadCount       int       `json:"read_count"`
	TotalLines      int       `json:"total_lines"`
	Remaining       int       `json:"remaining"`
	EvictedLines    int64     `json:"evicted_lines"`
	WaitedMS        int64     `json:"waited_ms"`
	WaitingForInput bool      `json:"waiting_for_input"`
}

type OutputRequest struct {
	PID       int
	TimeoutMS int
	Offset    int
	Length    int
}

type OutputResult struct {
	PID             int      `json:"pid"`
	State           State    `json:"state"`
	ExitCode        *int     `json:"exit_code,omitempty"`
	Lines           []string `json:"lines"`
	ReadFrom        int      `json:"read_from"`
	ReadCount       int      `json:"read_count"`
	TotalLines      int      `json:"total_lines"`
	Remaining       int      `json:"remaining"`
	EvictedLines    int64    `json:"evicted_lines"`
	WaitingForInput bool     `json:"waiting_for_input"`
	RuntimeMS       int64    `json:"runtime_ms"`
}

type InteractRequest struct {
	PID           int
	Input         string
	TimeoutMS     int
	WaitForPrompt bool
}

type InteractResult struct {
	PID             int      `json:"pid"`
	State           State    `json:"state"`
	ExitCode        *int     `json:"exit_code,omitempty"`
	Lines           []string `json:"lines,omitempty"`
	WaitingForInput bool     `json:"waiting_for_input"`
	RuntimeMS       int64    `json:"runtime_ms"`
}

type SessionInfo struct {
	PID             int       `json:"pid"`
	Command         string    `json:"command"`
	Shell           string    `json:"shell"`
	PTY             bool      `json:"pty"`
	State           State     `json:"state"`
	StartedAt       time.Time `json:"started_at"`
	RuntimeMS       int64     `json:"runtime_ms"`
	ExitCode        *int      `json:"exit_code,omitempty"`
	TotalLines      int       `json:"total_lines"`
	EvictedLines    int64     `json:"evicted_lines"`
	WaitingForInput bool      `json:"waiting_for_input"`
}

type SystemProcess struct {
	PID     int     `json:"pid"`
	CPU     float64 `json:"cpu_percent"`
	Memory  float64 `json:"memory_percent"`
	Command string  `json:"command"`
}

type State string

const (
	StateRunning  State = "running"
	StateWaiting  State = "waiting_for_input"
	StateExited   State = "exited"
	StateFailed   State = "failed"
	StateStopping State = "stopping"
)
