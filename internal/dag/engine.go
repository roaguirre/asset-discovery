package dag

import (
	"context"
	"reflect"
	"sync"

	"asset-discovery/internal/models"
	"asset-discovery/internal/tracing/telemetry"
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
	RunID      string
	Telemetry  telemetry.Provider
}

// Allow initial seeds plus two discovered frontiers so roots found in the first
// follow-up wave still get a collection pass of their own.
const maxSeedExpansionDepth = 2

// Run executes the DAG synchronously for local E2E testing.
func (e *Engine) Run(ctx context.Context, pCtx *models.PipelineContext) (*models.PipelineContext, error) {
	provider := telemetry.OrNoop(e.Telemetry)
	ctx = telemetry.WithProvider(ctx, provider)

	ctx, runSpan := telemetry.Start(ctx, "dag.run", telemetry.String("run_id", e.RunID))
	defer runSpan.End()

	// The engine may execute multiple collection waves, but the stage order remains acyclic:
	// frontier -> collectors -> enrichers -> next frontier.
	pCtx.InitializeSeedFrontier(maxSeedExpansionDepth)
	wave := 0

	for {
		// 1. Run Collectors (Concurrently)
		if frontier := pCtx.CollectionSeeds(); len(frontier) > 0 {
			waveCtx, waveSpan := telemetry.Start(
				ctx,
				"dag.wave",
				telemetry.String("run_id", e.RunID),
				telemetry.Int("wave", wave),
				telemetry.Int("frontier_size", len(frontier)),
			)
			var wg sync.WaitGroup
			for _, c := range e.Collectors {
				wg.Add(1)
				go func(col Collector) {
					defer wg.Done()
					err := e.processNode(waveCtx, "collect", wave, col, pCtx)
					if err != nil {
						pCtx.Lock()
						pCtx.Errors = append(pCtx.Errors, err)
						pCtx.Unlock()
					}
				}(c)
			}
			wg.Wait()
			waveSpan.End()
		}

		// 2. Run Enrichers
		for _, en := range e.Enrichers {
			err := e.processNode(ctx, "enrich", wave, en, pCtx)
			if err != nil {
				return pCtx, err
			}
		}

		if !pCtx.AdvanceSeedFrontier() {
			break
		}
		wave++
	}

	// 3. Run Filters
	for _, f := range e.Filters {
		err := e.processNode(ctx, "filter", wave, f, pCtx)
		if err != nil {
			return pCtx, err
		}
	}

	// 4. Run Exporters
	for _, ex := range e.Exporters {
		err := e.processNode(ctx, "export", wave, ex, pCtx)
		if err != nil {
			return pCtx, err
		}
	}

	return pCtx, nil
}

func (e *Engine) processNode(ctx context.Context, stage string, wave int, node Node, pCtx *models.PipelineContext) error {
	nodeName := nodeTypeName(node)
	nodeCtx, span := telemetry.Start(
		ctx,
		"dag.node",
		telemetry.String("run_id", e.RunID),
		telemetry.String("stage", stage),
		telemetry.String("node", nodeName),
		telemetry.Int("wave", wave),
	)

	_, err := node.Process(nodeCtx, pCtx)
	span.End(telemetry.Err(err))
	return err
}

func nodeTypeName(node Node) string {
	if node == nil {
		return "unknown"
	}

	typ := reflect.TypeOf(node)
	for typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}
	if typ.Name() == "" {
		return "unknown"
	}
	return typ.Name()
}
