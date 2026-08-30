package workflow

import "testing"

func TestValidateRunRejectsDuplicateAndInvalidItems(t *testing.T) {
	base := Run{SchemaVersion: SchemaVersion, ID: "run_x", Revision: 1, Title: "run", State: RunRunning}
	base.Items = []WorkItem{{ID: "a", State: ItemReady}, {ID: "a", State: ItemReady}}
	if err := ValidateRun(base); err == nil {
		t.Fatal("duplicate item id accepted")
	}
	base.Items = []WorkItem{{ID: "a", State: ItemState("bogus")}}
	if err := ValidateRun(base); err == nil {
		t.Fatal("invalid item state accepted")
	}
}
