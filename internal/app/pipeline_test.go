package app

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"asset-discovery/internal/collect"
	"asset-discovery/internal/tracing/telemetry"
)

func TestResolveOutputTargets_DefaultsToFileExports(t *testing.T) {
	now := time.Date(2026, time.March, 17, 22, 45, 6, 123456789, time.FixedZone("-0300", -3*60*60))

	outputs, runID := ResolveOutputTargets(nil, false, now)

	expectedRunID := "20260317T224506.123456789-0300"
	expectedOutputs := []string{
		filepath.Join("exports", "runs", expectedRunID, "results.json"),
		filepath.Join("exports", "runs", expectedRunID, "results.csv"),
		filepath.Join("exports", "runs", expectedRunID, "results.xlsx"),
	}

	if runID != expectedRunID {
		t.Fatalf("expected run ID %q, got %q", expectedRunID, runID)
	}

	if !reflect.DeepEqual(outputs, expectedOutputs) {
		t.Fatalf("expected outputs %v, got %v", expectedOutputs, outputs)
	}
}

func TestResolveOutputTargets_UsesExplicitOutputs(t *testing.T) {
	requested := []string{"custom/results.json", "custom/results.csv", "custom/results.xlsx"}

	outputs, _ := ResolveOutputTargets(requested, true, time.Now())

	if !reflect.DeepEqual(outputs, requested) {
		t.Fatalf("expected explicit outputs %v, got %v", requested, outputs)
	}
}

func TestNewPipeline_AssemblesRuntimeAndStages(t *testing.T) {
	pipeline, err := NewPipeline(Config{
		OutputsChanged: true,
		Outputs:        []string{"custom/results.json", "custom/results.csv", "custom/results.xlsx"},
		RunID:          "run-123",
		Telemetry:      telemetry.Noop(),
		Now: func() time.Time {
			return time.Date(2026, time.March, 17, 22, 45, 6, 0, time.FixedZone("-0300", -3*60*60))
		},
	})
	if err != nil {
		t.Fatalf("expected pipeline construction to succeed, got %v", err)
	}

	if pipeline.runID != "run-123" {
		t.Fatalf("expected run ID to be preserved, got %q", pipeline.runID)
	}
	if len(pipeline.outputs) != 3 {
		t.Fatalf("expected outputs to be preserved, got %v", pipeline.outputs)
	}
	if len(pipeline.engine.Collectors) != 11 {
		t.Fatalf("expected 11 collectors, got %d", len(pipeline.engine.Collectors))
	}
	if len(pipeline.engine.Enrichers) != 2 {
		t.Fatalf("expected 2 enrichers, got %d", len(pipeline.engine.Enrichers))
	}
	if len(pipeline.engine.Reconsiderers) != 1 {
		t.Fatalf("expected 1 reconsiderer, got %d", len(pipeline.engine.Reconsiderers))
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

	dnsCollector, ok := pipeline.engine.Collectors[0].(*collect.DNSCollector)
	if !ok {
		t.Fatalf("expected first collector to be DNSCollector, got %T", pipeline.engine.Collectors[0])
	}
	if got := dnsCollector.VariantSweepConfig(); got.Mode != collect.DNSVariantSweepModeExhaustive {
		t.Fatalf("expected default DNS variant sweep mode to be exhaustive, got %+v", got)
	}
}

func TestNewPipeline_AppliesDNSVariantSweepOverrides(t *testing.T) {
	pipeline, err := NewPipeline(Config{
		DNSVariantSweep: collect.DNSVariantSweepConfig{
			Mode:           collect.DNSVariantSweepModePrioritized,
			BatchSize:      64,
			Concurrency:    12,
			PrioritizedCap: 512,
		},
	})
	if err != nil {
		t.Fatalf("expected pipeline construction to succeed, got %v", err)
	}

	dnsCollector, ok := pipeline.engine.Collectors[0].(*collect.DNSCollector)
	if !ok {
		t.Fatalf("expected first collector to be DNSCollector, got %T", pipeline.engine.Collectors[0])
	}

	want := collect.DNSVariantSweepConfig{
		Mode:           collect.DNSVariantSweepModePrioritized,
		BatchSize:      64,
		Concurrency:    12,
		PrioritizedCap: 512,
	}
	if got := dnsCollector.VariantSweepConfig(); !reflect.DeepEqual(got, want) {
		t.Fatalf("expected DNS variant sweep config %+v, got %+v", want, got)
	}
}

func TestBuildExporters_RejectsLegacyVisualizerArchiveTargets(t *testing.T) {
	_, err := BuildExporters([]string{"results.json", "visualizer:custom/visualizer"}, "run-123")
	if err == nil {
		t.Fatalf("expected legacy visualizer target to fail")
	}
	if !strings.Contains(err.Error(), "JSON, CSV, or XLSX") {
		t.Fatalf("expected replacement guidance in error, got %v", err)
	}
}

func TestBuildExporters_RejectsLegacyHTMLVisualizerTargets(t *testing.T) {
	_, err := BuildExporters([]string{"results.json", "custom/visualizer.html"}, "run-123")
	if err == nil {
		t.Fatalf("expected legacy HTML visualizer target to fail")
	}
	if !strings.Contains(err.Error(), "JSON, CSV, or XLSX") {
		t.Fatalf("expected replacement guidance in error, got %v", err)
	}
}
