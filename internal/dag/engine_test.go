package dag_test

import (
	"context"
	"reflect"
	"testing"

	"asset-discovery/internal/dag"
	"asset-discovery/internal/models"
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
