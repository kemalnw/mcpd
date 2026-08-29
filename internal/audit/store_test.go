package audit

import (
	"path/filepath"
	"testing"
	"time"
)

func TestRecordUpdatesRecentAndStats(t *testing.T) {
	store, err := Open(true, filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	event := Event{Tool: "start_process", DurationMS: 12, Timestamp: time.Now().UTC()}
	if err := store.Record(event); err != nil {
		t.Fatal(err)
	}

	if got := len(store.Recent(10)); got != 1 {
		t.Fatalf("recent count = %d", got)
	}
	stats := store.Stats()
	if stats.Total != 1 || stats.Tools["start_process"].Calls != 1 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}
