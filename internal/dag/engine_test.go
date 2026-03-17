package dag_test

import (
	"context"
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
	pCtx := &models.PipelineContext{}

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
