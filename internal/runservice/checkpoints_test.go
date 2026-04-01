package runservice

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFileCheckpointStore_Load_IgnoresLegacyPipelineErrors(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := NewFileCheckpointStore(root)
	runID := "run-legacy-errors"

	legacySnapshot := `{
  "Run": {
    "id": "run-legacy-errors",
    "owner_uid": "uid-1",
    "owner_email": "reviewer@zerofox.com",
    "mode": "manual",
    "status": "awaiting_review",
    "current_wave": 1,
    "seed_count": 1,
    "enumeration_count": 0,
    "asset_count": 0,
    "pending_pivot_count": 1,
    "created_at": "2026-03-25T13:00:00Z",
    "updated_at": "2026-03-25T13:00:00Z"
  },
  "Context": {
    "Seeds": [
      {
        "id": "seed-1",
        "company_name": "ZeroFox",
        "domains": ["zerofox.com"]
      }
    ],
    "Errors": [
      {}
    ],
    "DNSVariantSweepLabels": ["zerofox"],
    "AISearchExecutedRoots": ["example.com"]
  },
  "scheduler_state": {
    "ai_search_executed_roots": ["example.com"]
  },
  "Progress": {
    "Initialized": true,
    "Wave": 1,
    "Phase": "collection_wave"
  },
  "Pivots": {}
}`

	path := filepath.Join(root, runID+".json")
	if err := os.WriteFile(path, []byte(legacySnapshot), 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	snapshot, err := store.Load(context.Background(), runID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(snapshot.Context.Errors) != 0 {
		t.Fatalf("expected legacy errors to be ignored, got %d error(s)", len(snapshot.Context.Errors))
	}
	if len(snapshot.Context.DNSVariantSweepLabels) != 1 || snapshot.Context.DNSVariantSweepLabels[0] != "zerofox" {
		t.Fatalf("expected other checkpoint fields to survive, got %+v", snapshot.Context.DNSVariantSweepLabels)
	}
	if len(snapshot.Context.AISearchExecutedRoots) != 1 || snapshot.Context.AISearchExecutedRoots[0] != "example.com" {
		t.Fatalf("expected AI search cache to survive, got %+v", snapshot.Context.AISearchExecutedRoots)
	}
	if len(snapshot.SchedulerState.AISearchExecutedRoots) != 1 || snapshot.SchedulerState.AISearchExecutedRoots[0] != "example.com" {
		t.Fatalf("expected scheduler AI search cache to survive, got %+v", snapshot.SchedulerState.AISearchExecutedRoots)
	}
}
