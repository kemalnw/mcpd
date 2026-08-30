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
	SchemaVersion    int                `json:"schema_version"`
	ID               string             `json:"run_id"`
	Revision         uint64             `json:"revision"`
	Title            string             `json:"title"`
	Objective        string             `json:"objective,omitempty"`
	SuccessCriteria  []string           `json:"success_criteria,omitempty"`
	Phase            string             `json:"phase,omitempty"`
	State            RunState           `json:"state"`
	Items            []WorkItem         `json:"items,omitempty"`
	NextActions      []string           `json:"next_actions,omitempty"`
	CreatedAt        time.Time          `json:"created_at"`
	UpdatedAt        time.Time          `json:"updated_at"`
	LastCheckpointAt time.Time          `json:"last_checkpoint_at,omitempty"`
	Handoff          *HandoffCheckpoint `json:"handoff,omitempty"`
}

type CheckpointReason string

const (
	CheckpointPeriodic         CheckpointReason = "periodic"
	CheckpointBeforeWait       CheckpointReason = "before_wait"
	CheckpointBeforeSessionEnd CheckpointReason = "before_session_end"
	CheckpointManual           CheckpointReason = "manual"
	CheckpointErrorRecovery    CheckpointReason = "error_recovery"
)

type ActiveHandle struct {
	Kind              string    `json:"kind"`
	ID                string    `json:"id"`
	ItemID            string    `json:"item_id,omitempty"`
	LastObservedState string    `json:"last_observed_state,omitempty"`
	NextPollAt        time.Time `json:"next_poll_at,omitempty"`
	Deadline          time.Time `json:"deadline,omitempty"`
	CancelTool        string    `json:"cancel_tool,omitempty"`
	CancelID          string    `json:"cancel_id,omitempty"`
}

type EvidenceReference struct {
	Kind       string    `json:"kind"`
	ID         string    `json:"id"`
	Summary    string    `json:"summary,omitempty"`
	VerifiedAt time.Time `json:"verified_at,omitempty"`
}

type Recommendation struct {
	Action           string    `json:"action"`
	Source           string    `json:"source,omitempty"`
	Confidence       string    `json:"confidence,omitempty"`
	RevalidateAfter  time.Time `json:"revalidate_after,omitempty"`
	RequiresApproval bool      `json:"requires_approval,omitempty"`
}

type HandoffCheckpoint struct {
	Generation        uint64              `json:"generation"`
	Reason            CheckpointReason    `json:"reason"`
	Summary           string              `json:"summary,omitempty"`
	Blockers          []string            `json:"blockers,omitempty"`
	Evidence          []EvidenceReference `json:"evidence,omitempty"`
	ActiveHandles     []ActiveHandle      `json:"active_handles,omitempty"`
	ActiveSideEffects []string            `json:"active_side_effects,omitempty"`
	PendingApprovals  []string            `json:"pending_approvals,omitempty"`
	DoNotRepeat       []string            `json:"do_not_repeat,omitempty"`
	CleanupState      []string            `json:"cleanup_state,omitempty"`
	Recommendations   []Recommendation    `json:"recommendations,omitempty"`
	NextActions       []string            `json:"next_actions,omitempty"` // compatibility; advisory only
	CheckpointedAt    time.Time           `json:"checkpointed_at"`
	RunRevision       uint64              `json:"run_revision"`
}
