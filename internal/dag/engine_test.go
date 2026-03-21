package dag_test

import (
	"context"
	"reflect"
	"testing"

	"asset-discovery/internal/dag"
	"asset-discovery/internal/models"
	"asset-discovery/internal/tracing/telemetry"
)

type mockNode struct {
	called bool
}

func (m *mockNode) Process(ctx context.Context, pCtx *models.PipelineContext) (*models.PipelineContext, error) {
	m.called = true
	pCtx.Assets = append(pCtx.Assets, models.Asset{Identifier: "test.com"})
	return pCtx, nil
}

func TestEngine_Run(t *testing.T) {
	collector := &mockNode{}
	enricher := &mockNode{}
	filter := &mockNode{}
	exporter := &mockNode{}

	engine := &dag.Engine{
		Collectors: []dag.Collector{collector},
		Enrichers:  []dag.Enricher{enricher},
		Filters:    []dag.Filter{filter},
		Exporters:  []dag.Exporter{exporter},
	}

	ctx := context.Background()
	pCtx := &models.PipelineContext{
		Seeds: []models.Seed{
			{
				ID:          "seed-1",
				CompanyName: "example.com",
				Domains:     []string{"example.com"},
			},
		},
	}

	resultCtx, err := engine.Run(ctx, pCtx)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if !collector.called {
		t.Errorf("expected collector to be called")
	}
	if !enricher.called {
		t.Errorf("expected enricher to be called")
	}
	if !filter.called {
		t.Errorf("expected filter to be called")
	}
	if !exporter.called {
		t.Errorf("expected exporter to be called")
	}

	if len(resultCtx.Assets) != 4 {
		t.Errorf("expected 4 assets to be added (1 per node), got %d", len(resultCtx.Assets))
	}
}

type frontierCollector struct {
	seedsPerCall []int
}

func (c *frontierCollector) Process(ctx context.Context, pCtx *models.PipelineContext) (*models.PipelineContext, error) {
	c.seedsPerCall = append(c.seedsPerCall, len(pCtx.CollectionSeeds()))
	return pCtx, nil
}

type seedSchedulingEnricher struct {
	callCount int
}

func (e *seedSchedulingEnricher) Process(ctx context.Context, pCtx *models.PipelineContext) (*models.PipelineContext, error) {
	e.callCount++
	if e.callCount == 1 {
		pCtx.EnqueueSeed(models.Seed{
			ID:          "seed-2",
			CompanyName: "ptr.example.com",
			Domains:     []string{"ptr.example.com"},
		})
	}

	return pCtx, nil
}

func TestEngine_Run_UsesFrontierForFollowUpCollection(t *testing.T) {
	collector := &frontierCollector{}
	enricher := &seedSchedulingEnricher{}

	engine := &dag.Engine{
		Collectors: []dag.Collector{collector},
		Enrichers:  []dag.Enricher{enricher},
	}

	ctx := context.Background()
	pCtx := &models.PipelineContext{
		Seeds: []models.Seed{
			{
				ID:          "seed-1",
				CompanyName: "example.com",
				Domains:     []string{"example.com"},
			},
		},
	}

	resultCtx, err := engine.Run(ctx, pCtx)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	expectedSeedsPerCall := []int{1, 1}
	if !reflect.DeepEqual(collector.seedsPerCall, expectedSeedsPerCall) {
		t.Fatalf("expected collector frontier sizes %v, got %v", expectedSeedsPerCall, collector.seedsPerCall)
	}

	if len(resultCtx.Seeds) != 2 {
		t.Fatalf("expected discovered seed to be registered once, got %d seeds", len(resultCtx.Seeds))
	}
}

type chainedSeedSchedulingEnricher struct {
	callCount int
}

func (e *chainedSeedSchedulingEnricher) Process(ctx context.Context, pCtx *models.PipelineContext) (*models.PipelineContext, error) {
	e.callCount++

	switch e.callCount {
	case 1:
		pCtx.EnqueueSeed(models.Seed{
			ID:          "seed-2",
			CompanyName: "ptr.example.com",
			Domains:     []string{"ptr.example.com"},
		})
	case 2:
		pCtx.EnqueueSeed(models.Seed{
			ID:          "seed-3",
			CompanyName: "example-store.com",
			Domains:     []string{"example-store.com"},
		})
	}

	return pCtx, nil
}

func TestEngine_Run_CollectsLateDiscoveredFollowUpSeed(t *testing.T) {
	collector := &frontierCollector{}
	enricher := &chainedSeedSchedulingEnricher{}

	engine := &dag.Engine{
		Collectors: []dag.Collector{collector},
		Enrichers:  []dag.Enricher{enricher},
	}

	ctx := context.Background()
	pCtx := &models.PipelineContext{
		Seeds: []models.Seed{
			{
				ID:          "seed-1",
				CompanyName: "example.com",
				Domains:     []string{"example.com"},
			},
		},
	}

	resultCtx, err := engine.Run(ctx, pCtx)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	expectedSeedsPerCall := []int{1, 1, 1}
	if !reflect.DeepEqual(collector.seedsPerCall, expectedSeedsPerCall) {
		t.Fatalf("expected collector frontier sizes %v, got %v", expectedSeedsPerCall, collector.seedsPerCall)
	}

	if len(resultCtx.Seeds) != 3 {
		t.Fatalf("expected chained discovered seeds to be registered, got %d seeds", len(resultCtx.Seeds))
	}
}

type extraFrontierEnricher struct {
	callCount int
}

func (e *extraFrontierEnricher) Process(ctx context.Context, pCtx *models.PipelineContext) (*models.PipelineContext, error) {
	e.callCount++
	if e.callCount == 2 {
		pCtx.EnqueueSeed(models.Seed{
			ID:          "seed-3",
			CompanyName: "extra.example.net",
			Domains:     []string{"extra.example.net"},
		})
	}
	return pCtx, nil
}

type promotingReconsiderer struct {
	callCount int
}

func (r *promotingReconsiderer) Process(ctx context.Context, pCtx *models.PipelineContext) (*models.PipelineContext, error) {
	r.callCount++
	if r.callCount == 1 {
		pCtx.EnqueueSeed(models.Seed{
			ID:          "seed-2",
			CompanyName: "promoted.example.com",
			Domains:     []string{"promoted.example.com"},
		})
	}
	return pCtx, nil
}

func TestEngine_Run_ReconsiderationPromotesOneBoundedExtraFrontier(t *testing.T) {
	collector := &frontierCollector{}
	enricher := &extraFrontierEnricher{}
	reconsiderer := &promotingReconsiderer{}

	engine := &dag.Engine{
		Collectors:    []dag.Collector{collector},
		Enrichers:     []dag.Enricher{enricher},
		Reconsiderers: []dag.Reconsiderer{reconsiderer},
	}

	resultCtx, err := engine.Run(context.Background(), &models.PipelineContext{
		Seeds: []models.Seed{
			{
				ID:          "seed-1",
				CompanyName: "example.com",
				Domains:     []string{"example.com"},
			},
		},
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	expectedSeedsPerCall := []int{1, 1}
	if !reflect.DeepEqual(collector.seedsPerCall, expectedSeedsPerCall) {
		t.Fatalf("expected collector frontier sizes %v, got %v", expectedSeedsPerCall, collector.seedsPerCall)
	}
	if reconsiderer.callCount != 1 {
		t.Fatalf("expected reconsiderer to run once, got %d calls", reconsiderer.callCount)
	}
	if len(resultCtx.Seeds) != 3 {
		t.Fatalf("expected extra-frontier discovery to be registered without a third wave, got %d seeds", len(resultCtx.Seeds))
	}
}

func TestEngine_Run_DoesNotReconsiderAgainAfterExtraFrontier(t *testing.T) {
	collector := &frontierCollector{}
	enricher := &extraFrontierEnricher{}
	reconsiderer := &promotingReconsiderer{}

	engine := &dag.Engine{
		Collectors:    []dag.Collector{collector},
		Enrichers:     []dag.Enricher{enricher},
		Reconsiderers: []dag.Reconsiderer{reconsiderer},
	}

	if _, err := engine.Run(context.Background(), &models.PipelineContext{
		Seeds: []models.Seed{
			{
				ID:          "seed-1",
				CompanyName: "example.com",
				Domains:     []string{"example.com"},
			},
		},
	}); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if reconsiderer.callCount != 1 {
		t.Fatalf("expected reconsiderer to run exactly once, got %d calls", reconsiderer.callCount)
	}
	if !reflect.DeepEqual(collector.seedsPerCall, []int{1, 1}) {
		t.Fatalf("expected no third collector wave after the extra frontier, got %v", collector.seedsPerCall)
	}
}

type ipProducingEnricher struct{}

func (e *ipProducingEnricher) Process(ctx context.Context, pCtx *models.PipelineContext) (*models.PipelineContext, error) {
	pCtx.Assets = append(pCtx.Assets, models.Asset{
		ID:            "ip-1",
		EnumerationID: "enum-1",
		Type:          models.AssetTypeIP,
		Identifier:    "203.0.113.10",
		Source:        "domain_enricher",
		IPDetails:     &models.IPDetails{},
	})
	return pCtx, nil
}

type ipObservingEnricher struct {
	observed bool
}

func (e *ipObservingEnricher) Process(ctx context.Context, pCtx *models.PipelineContext) (*models.PipelineContext, error) {
	for _, asset := range pCtx.Assets {
		if asset.Type == models.AssetTypeIP && asset.Identifier == "203.0.113.10" {
			e.observed = true
			break
		}
	}
	return pCtx, nil
}

func TestEngine_Run_LaterEnricherSeesAssetsAddedByEarlierEnricher(t *testing.T) {
	observer := &ipObservingEnricher{}

	engine := &dag.Engine{
		Enrichers: []dag.Enricher{
			&ipProducingEnricher{},
			observer,
		},
	}

	_, err := engine.Run(context.Background(), &models.PipelineContext{
		Seeds: []models.Seed{
			{
				ID:          "seed-1",
				CompanyName: "example.com",
				Domains:     []string{"example.com"},
			},
		},
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if !observer.observed {
		t.Fatalf("expected later enricher to observe IP assets appended by the earlier enricher")
	}
}

type recordedSpan struct {
	name  string
	attrs map[string]interface{}
}

type recordingTelemetry struct {
	spans []recordedSpan
}

type recordingSpan struct{}

func (r *recordingTelemetry) Start(ctx context.Context, name string, attrs ...telemetry.Attr) (context.Context, telemetry.Span) {
	values := make(map[string]interface{}, len(attrs))
	for _, attr := range attrs {
		values[attr.Key] = attr.Value
	}
	r.spans = append(r.spans, recordedSpan{name: name, attrs: values})
	return ctx, recordingSpan{}
}

func (r *recordingTelemetry) Log(ctx context.Context, level telemetry.Level, message string, attrs ...telemetry.Attr) {
}

func (recordingSpan) End(attrs ...telemetry.Attr) {}

func TestEngine_Run_EmitsTelemetryForWavesAndNodes(t *testing.T) {
	collector := &frontierCollector{}
	enricher := &seedSchedulingEnricher{}
	telemetryRecorder := &recordingTelemetry{}

	engine := &dag.Engine{
		Collectors: []dag.Collector{collector},
		Enrichers:  []dag.Enricher{enricher},
		RunID:      "run-telemetry",
		Telemetry:  telemetryRecorder,
	}

	pCtx := &models.PipelineContext{
		Seeds: []models.Seed{
			{
				ID:          "seed-1",
				CompanyName: "example.com",
				Domains:     []string{"example.com"},
			},
		},
	}

	if _, err := engine.Run(context.Background(), pCtx); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	var waveSpans []recordedSpan
	var nodeSpans []recordedSpan
	for _, span := range telemetryRecorder.spans {
		switch span.name {
		case "dag.wave":
			waveSpans = append(waveSpans, span)
		case "dag.node":
			nodeSpans = append(nodeSpans, span)
		}
	}

	if len(waveSpans) != 2 {
		t.Fatalf("expected 2 wave spans, got %d", len(waveSpans))
	}
	if len(nodeSpans) != 4 {
		t.Fatalf("expected 4 node spans, got %d", len(nodeSpans))
	}

	for _, span := range waveSpans {
		if span.attrs["run_id"] != "run-telemetry" {
			t.Fatalf("expected wave span to include run_id, got %+v", span.attrs)
		}
		if _, ok := span.attrs["wave"]; !ok {
			t.Fatalf("expected wave span to include wave attr, got %+v", span.attrs)
		}
	}

	for _, span := range nodeSpans {
		if span.attrs["run_id"] != "run-telemetry" {
			t.Fatalf("expected node span to include run_id, got %+v", span.attrs)
		}
		if _, ok := span.attrs["stage"]; !ok {
			t.Fatalf("expected node span to include stage attr, got %+v", span.attrs)
		}
		if _, ok := span.attrs["node"]; !ok {
			t.Fatalf("expected node span to include node attr, got %+v", span.attrs)
		}
		if _, ok := span.attrs["wave"]; !ok {
			t.Fatalf("expected node span to include wave attr, got %+v", span.attrs)
		}
	}
}
