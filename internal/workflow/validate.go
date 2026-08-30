package workflow

import (
	"errors"
	"fmt"
	"strings"
)

func ValidateRun(run Run) error {
	if run.SchemaVersion != SchemaVersion || strings.TrimSpace(run.ID) == "" || run.Revision == 0 || strings.TrimSpace(run.Title) == "" {
		return errors.New("run identity/schema/title is invalid")
	}
	switch run.State {
	case RunPlanned, RunRunning, RunBlocked, RunCompleted, RunFailed, RunCanceled:
	default:
		return fmt.Errorf("invalid run state %q", run.State)
	}
	seen := make(map[string]struct{}, len(run.Items))
	for _, item := range run.Items {
		if strings.TrimSpace(item.ID) == "" {
			return errors.New("work item id is required")
		}
		if _, ok := seen[item.ID]; ok {
			return fmt.Errorf("duplicate work item id %q", item.ID)
		}
		seen[item.ID] = struct{}{}
		switch item.State {
		case ItemPlanned, ItemReady, ItemRunning, ItemBlocked, ItemCompleted, ItemFailed, ItemCanceled:
		default:
			return fmt.Errorf("work item %q has invalid state %q", item.ID, item.State)
		}
	}
	for _, item := range run.Items {
		for _, dependency := range item.DependsOn {
			if dependency == item.ID {
				return fmt.Errorf("work item %q depends on itself", item.ID)
			}
			if _, ok := seen[dependency]; !ok {
				return fmt.Errorf("work item %q has unknown dependency %q", item.ID, dependency)
			}
		}
	}
	if run.Handoff != nil {
		if err := validateHandoff(*run.Handoff); err != nil {
			return err
		}
	}
	return nil
}

const (
	maxHandoffSummaryBytes = 8 << 10
	maxHandoffListItems    = 20
	maxHandoffItemBytes    = 1024
	maxActiveHandles       = 50
	maxEvidenceReferences  = 50
	maxRecommendations     = 20
)

func validateHandoff(h HandoffCheckpoint) error {
	switch h.Reason {
	case CheckpointPeriodic, CheckpointBeforeWait, CheckpointBeforeSessionEnd, CheckpointManual, CheckpointErrorRecovery:
	default:
		return fmt.Errorf("invalid handoff checkpoint reason %q", h.Reason)
	}
	if h.Generation == 0 {
		return errors.New("handoff generation is required")
	}
	if len(h.Summary) > maxHandoffSummaryBytes {
		return fmt.Errorf("handoff summary exceeds %d bytes", maxHandoffSummaryBytes)
	}
	lists := map[string][]string{
		"blockers": h.Blockers, "active_side_effects": h.ActiveSideEffects,
		"pending_approvals": h.PendingApprovals, "do_not_repeat": h.DoNotRepeat,
		"cleanup_state": h.CleanupState, "next_actions": h.NextActions,
	}
	for name, values := range lists {
		if len(values) > maxHandoffListItems {
			return fmt.Errorf("handoff %s exceeds %d items", name, maxHandoffListItems)
		}
		for _, value := range values {
			if len(value) > maxHandoffItemBytes {
				return fmt.Errorf("handoff %s item exceeds %d bytes", name, maxHandoffItemBytes)
			}
		}
	}
	if len(h.ActiveHandles) > maxActiveHandles {
		return fmt.Errorf("handoff active_handles exceed %d items", maxActiveHandles)
	}
	for _, handle := range h.ActiveHandles {
		if strings.TrimSpace(handle.Kind) == "" || strings.TrimSpace(handle.ID) == "" {
			return errors.New("handoff active handle requires kind and id")
		}
		if len(handle.Kind) > 64 || len(handle.ID) > 512 || len(handle.ItemID) > 128 || len(handle.LastObservedState) > 512 || len(handle.CancelTool) > 128 || len(handle.CancelID) > 512 {
			return errors.New("handoff active handle field is too large")
		}
	}
	if h.Reason == CheckpointBeforeWait {
		safe := false
		for _, handle := range h.ActiveHandles {
			if strings.TrimSpace(handle.LastObservedState) != "" && !handle.NextPollAt.IsZero() && strings.TrimSpace(handle.CancelTool) != "" && (handle.CancelTool == "none" || strings.TrimSpace(handle.CancelID) != "") {
				safe = true
				break
			}
		}
		if !safe {
			return errors.New("before_wait handoff requires last observed state, next poll time, and cancellation path")
		}
	}
	if len(h.Evidence) > maxEvidenceReferences {
		return fmt.Errorf("handoff evidence exceeds %d items", maxEvidenceReferences)
	}
	for _, evidence := range h.Evidence {
		if strings.TrimSpace(evidence.Kind) == "" || strings.TrimSpace(evidence.ID) == "" {
			return errors.New("handoff evidence requires kind and id")
		}
		if len(evidence.Kind) > 64 || len(evidence.ID) > 512 || len(evidence.Summary) > maxHandoffItemBytes {
			return errors.New("handoff evidence field is too large")
		}
	}
	if len(h.Recommendations) > maxRecommendations {
		return fmt.Errorf("handoff recommendations exceed %d items", maxRecommendations)
	}
	for _, rec := range h.Recommendations {
		if strings.TrimSpace(rec.Action) == "" || len(rec.Action) > maxHandoffItemBytes || len(rec.Source) > 512 {
			return errors.New("handoff recommendation action/source is invalid")
		}
		switch rec.Confidence {
		case "", "high", "medium", "low":
		default:
			return fmt.Errorf("invalid recommendation confidence %q", rec.Confidence)
		}
	}
	if h.CheckpointedAt.IsZero() || h.RunRevision == 0 {
		return errors.New("handoff checkpoint timestamp/revision is required")
	}
	return nil
}
