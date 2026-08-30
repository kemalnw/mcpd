package workflow

import "time"

const SchemaVersion = 1

type RunState string

type ItemState string

const (
	RunPlanned   RunState = "planned"
	RunRunning   RunState = "running"
	RunBlocked   RunState = "blocked"
	RunCompleted RunState = "completed"
	RunFailed    RunState = "failed"
	RunCanceled  RunState = "canceled"

	ItemPlanned   ItemState = "planned"
	ItemReady     ItemState = "ready"
	ItemRunning   ItemState = "running"
	ItemBlocked   ItemState = "blocked"
	ItemCompleted ItemState = "completed"
	ItemFailed    ItemState = "failed"
	ItemCanceled  ItemState = "canceled"
)

type WorkItem struct {
	ID        string    `json:"id"`
	Title     string    `json:"title,omitempty"`
	State     ItemState `json:"state"`
	DependsOn []string  `json:"depends_on,omitempty"`
	JobID     string    `json:"job_id,omitempty"`
	PID       int       `json:"pid,omitempty"`
	Branch    string    `json:"branch,omitempty"`
	Worktree  string    `json:"worktree,omitempty"`
	PRURL     string    `json:"pr_url,omitempty"`
	Summary   string    `json:"summary,omitempty"`
}

type Run struct {
	SchemaVersion   int        `json:"schema_version"`
	ID              string     `json:"run_id"`
	Revision        uint64     `json:"revision"`
	Title           string     `json:"title"`
	Objective       string     `json:"objective,omitempty"`
	SuccessCriteria []string   `json:"success_criteria,omitempty"`
	Phase           string     `json:"phase,omitempty"`
	State           RunState   `json:"state"`
	Items           []WorkItem `json:"items,omitempty"`
	NextActions     []string   `json:"next_actions,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}
