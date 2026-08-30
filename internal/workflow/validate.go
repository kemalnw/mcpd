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
	return nil
}
