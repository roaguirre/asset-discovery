package app

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"asset-discovery/internal/tracing/telemetry"
)

func TestResolveOutputTargets_DefaultsArchiveRuns(t *testing.T) {
	now := time.Date(2026, time.March, 17, 22, 45, 6, 123456789, time.FixedZone("-0300", -3*60*60))

	outputs, runID := ResolveOutputTargets(nil, false, now)

	expectedRunID := "20260317T224506.123456789-0300"
	expectedOutputs := []string{
		filepath.Join("exports", "runs", expectedRunID, "results.json"),
		filepath.Join("exports", "runs", expectedRunID, "results.csv"),
		filepath.Join("exports", "runs", expectedRunID, "results.xlsx"),
		DefaultVisualizerOutput,
	}

	if runID != expectedRunID {
		t.Fatalf("expected run ID %q, got %q", expectedRunID, runID)
	}

	if !reflect.DeepEqual(outputs, expectedOutputs) {
		t.Fatalf("expected outputs %v, got %v", expectedOutputs, outputs)
	}
}

func TestResolveOutputTargets_UsesExplicitOutputs(t *testing.T) {
	requested := []string{"custom/results.json", "custom/visualizer.html"}

	outputs, _ := ResolveOutputTargets(requested, true, time.Now())

	if !reflect.DeepEqual(outputs, requested) {
		t.Fatalf("expected explicit outputs %v, got %v", requested, outputs)
	}
}

func TestRelativeOutputPath(t *testing.T) {
	got := RelativeOutputPath("exports/visualizer.html", "exports/runs/20260317/results.json")
	want := "runs/20260317/results.json"

	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestNewPipeline_AssemblesRuntimeAndStages(t *testing.T) {
	pipeline := NewPipeline(Config{
		OutputsChanged: true,
		Outputs:        []string{"custom/results.json", "custom/results.csv", "custom/visualizer.html"},
		RunID:          "run-123",
		Telemetry:      telemetry.Noop(),
		Now: func() time.Time {
			return time.Date(2026, time.March, 17, 22, 45, 6, 0, time.FixedZone("-0300", -3*60*60))
		},
	})

	if pipeline.runID != "run-123" {
		t.Fatalf("expected run ID to be preserved, got %q", pipeline.runID)
	}
	if len(pipeline.outputs) != 3 {
		t.Fatalf("expected outputs to be preserved, got %v", pipeline.outputs)
	}
	if len(pipeline.engine.Collectors) != 10 {
		t.Fatalf("expected 10 collectors, got %d", len(pipeline.engine.Collectors))
	}
	if len(pipeline.engine.Enrichers) != 2 {
		t.Fatalf("expected 2 enrichers, got %d", len(pipeline.engine.Enrichers))
	}
	if len(pipeline.engine.Filters) != 1 {
		t.Fatalf("expected 1 filter, got %d", len(pipeline.engine.Filters))
	}
	if len(pipeline.engine.Exporters) != 3 {
		t.Fatalf("expected 3 exporters, got %d", len(pipeline.engine.Exporters))
	}
	if pipeline.engine.RunID != "run-123" {
		t.Fatalf("expected engine run ID to be set, got %q", pipeline.engine.RunID)
	}
}
