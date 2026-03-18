package main

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestResolveOutputTargets_DefaultsArchiveRuns(t *testing.T) {
	now := time.Date(2026, time.March, 17, 22, 45, 6, 123456789, time.FixedZone("-0300", -3*60*60))

	outputs, runID := resolveOutputTargets(nil, false, now)

	expectedRunID := "20260317T224506.123456789-0300"
	expectedOutputs := []string{
		filepath.Join("exports", "runs", expectedRunID, "results.json"),
		filepath.Join("exports", "runs", expectedRunID, "results.csv"),
		filepath.Join("exports", "runs", expectedRunID, "results.xlsx"),
		defaultVisualizerOutput,
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

	outputs, _ := resolveOutputTargets(requested, true, time.Now())

	if !reflect.DeepEqual(outputs, requested) {
		t.Fatalf("expected explicit outputs %v, got %v", requested, outputs)
	}
}

func TestRelativeOutputPath(t *testing.T) {
	got := relativeOutputPath("exports/visualizer.html", "exports/runs/20260317/results.json")
	want := "runs/20260317/results.json"

	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}
