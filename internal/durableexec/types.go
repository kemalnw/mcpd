package durableexec

import "time"

const SchemaVersion = 1

type State string

const (
	StateStarting  State = "starting"
	StateRunning   State = "running"
	StateCompleted State = "completed"
	StateFailed    State = "failed"
	StateCanceled  State = "canceled"
	StateOrphaned  State = "orphaned"
)

type StartRequest struct {
	Command        string `json:"command"`
	CWD            string `json:"cwd,omitempty"`
	Shell          string `json:"shell,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

type Job struct {
	SchemaVersion    int        `json:"schema_version"`
	ID               string     `json:"id"`
	State            State      `json:"state"`
	RunnerPID        int        `json:"runner_pid"`
	RunnerStartTicks uint64     `json:"runner_start_ticks"`
	ChildPID         int        `json:"child_pid,omitempty"`
	ChildStartTicks  uint64     `json:"child_start_ticks,omitempty"`
	BootID           string     `json:"boot_id"`
	CommandSHA256    string     `json:"command_sha256"`
	CommandBytes     int        `json:"command_bytes"`
	CWD              string     `json:"cwd,omitempty"`
	Shell            string     `json:"shell"`
	StartedAt        time.Time  `json:"started_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	FinishedAt       *time.Time `json:"finished_at,omitempty"`
	ExitCode         *int       `json:"exit_code,omitempty"`
	Reason           string     `json:"reason,omitempty"`
	LogPath          string     `json:"log_path"`
}

type LogTail struct {
	JobID         string `json:"job_id"`
	Content       string `json:"content,omitempty"`
	BytesReturned int    `json:"bytes_returned"`
	TotalBytes    int64  `json:"total_bytes"`
	StartOffset   int64  `json:"start_offset"`
	Truncated     bool   `json:"truncated,omitempty"`
}

type runnerSpec struct {
	JobID   string `json:"job_id"`
	Command string `json:"command"`
	CWD     string `json:"cwd"`
	Shell   string `json:"shell"`
	LogPath string `json:"log_path"`
}
