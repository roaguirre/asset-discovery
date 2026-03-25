package visualizer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileArchiveStore_SaveLoadAndSkipMissingSnapshots(t *testing.T) {
	visualizerDir := filepath.Join(t.TempDir(), "visualizer")
	store := NewFileArchiveStore()
	ts := time.Date(2026, time.March, 24, 10, 0, 0, 0, time.FixedZone("-0300", -3*60*60))

	run1 := Run{
		RunSummary: RunSummary{
			ID:         "run-1",
			Label:      "run-1",
			CreatedAt:  ts,
			AssetCount: 1,
		},
		Rows: []Row{{AssetID: "asset-1", Identifier: "example.com"}},
	}
	run2 := Run{
		RunSummary: RunSummary{
			ID:         "run-2",
			Label:      "run-2",
			CreatedAt:  ts.Add(5 * time.Minute),
			AssetCount: 1,
		},
		Rows: []Row{{AssetID: "asset-2", Identifier: "api.example.com"}},
	}

	if err := store.Save(visualizerDir, run1); err != nil {
		t.Fatalf("expected first save to succeed, got %v", err)
	}
	if err := store.Save(visualizerDir, run2); err != nil {
		t.Fatalf("expected second save to succeed, got %v", err)
	}

	manifestPath := filepath.Join(visualizerDir, "manifest.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("expected manifest to exist, got %v", err)
	}

	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("expected manifest to parse, got %v", err)
	}
	if len(manifest.Runs) != 2 || manifest.Runs[0].ID != "run-2" || manifest.Runs[1].ID != "run-1" {
		t.Fatalf("expected newest-first manifest ordering, got %+v", manifest.Runs)
	}
	if manifest.Runs[0].DataPath != "runs/run-2.json" {
		t.Fatalf("expected relative data path in manifest, got %+v", manifest.Runs[0])
	}

	if err := os.Remove(filepath.Join(visualizerDir, "runs", "run-2.json")); err != nil {
		t.Fatalf("expected to remove newest snapshot, got %v", err)
	}

	runs, err := store.Load(visualizerDir)
	if err != nil {
		t.Fatalf("expected load to skip missing snapshots, got %v", err)
	}
	if len(runs) != 1 || runs[0].ID != "run-1" {
		t.Fatalf("expected load to return only existing snapshots, got %+v", runs)
	}
}
