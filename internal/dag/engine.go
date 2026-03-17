package dag

import (
	"context"
	"sync"

	"asset-discovery/internal/models"
)

// Node represents a stage in the DAG pipeline.
type Node interface {
	// Process takes a PipelineContext, mutates it, and returns it.
	// It also returns an error if the node fails entirely.
	Process(ctx context.Context, pCtx *models.PipelineContext) (*models.PipelineContext, error)
}

// Collector gathers raw data from OSINT sources and APIs.
type Collector interface {
	Node
}

// Enricher augments the collected domains with DNS, IP, and provider data.
type Enricher interface {
	Node
}

// Filter removes false positives, dead domains, or out-of-scope assets.
type Filter interface {
	Node
}

// Exporter formats the final dataset for consumption.
type Exporter interface {
	Node
}

// Engine Orchestrates the Pipeline
type Engine struct {
	Collectors []Collector
	Enrichers  []Enricher
	Filters    []Filter
	Exporters  []Exporter
}

// Run executes the DAG synchronously for local E2E testing.
func (e *Engine) Run(ctx context.Context, pCtx *models.PipelineContext) (*models.PipelineContext, error) {
	// Controlled Recursion Loop (Max Depth: 2)
	// We run Collectors -> Enrichers iteratively to handle natively discovered PTR/Subdomains
	for pCtx.Depth < 2 {
		pCtx.HasNewSeeds = false // Reset per iteration

		// 1. Run Collectors (Concurrently)
		var wg sync.WaitGroup
		for _, c := range e.Collectors {
			wg.Add(1)
			go func(col Collector) {
				defer wg.Done()
				_, err := col.Process(ctx, pCtx)
				if err != nil {
					pCtx.Lock()
					pCtx.Errors = append(pCtx.Errors, err)
					pCtx.Unlock()
				}
			}(c)
		}
		wg.Wait()

		// 2. Run Enrichers
		for _, en := range e.Enrichers {
			var err error
			pCtx, err = en.Process(ctx, pCtx)
			if err != nil {
				return pCtx, err
			}
		}

		// If no enrichers generated new seeds, we are done collecting. Break out contextually.
		if !pCtx.HasNewSeeds {
			break
		}

		pCtx.Depth++
	}

	// 3. Run Filters
	for _, f := range e.Filters {
		var err error
		pCtx, err = f.Process(ctx, pCtx)
		if err != nil {
			return pCtx, err
		}
	}

	// 4. Run Exporters
	for _, ex := range e.Exporters {
		var err error
		pCtx, err = ex.Process(ctx, pCtx)
		if err != nil {
			return pCtx, err
		}
	}

	return pCtx, nil
}
