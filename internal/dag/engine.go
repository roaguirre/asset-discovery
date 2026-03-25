package dag

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"runtime/debug"
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

// Reconsiderer re-evaluates discarded candidates after the normal collection
// frontier has been exhausted.
type Reconsiderer interface {
	Node
}

// Exporter formats the final dataset for consumption.
type Exporter interface {
	Node
}

// Engine Orchestrates the Pipeline
type Engine struct {
	Collectors    []Collector
	Enrichers     []Enricher
	Reconsiderers []Reconsiderer
	Filters       []Filter
	Exporters     []Exporter
	RunID         string
	Telemetry     telemetry.Provider
}

type RunPhase string

const (
	RunPhaseCollectionWave   RunPhase = "collection_wave"
	RunPhaseAdvanceFrontier  RunPhase = "advance_frontier"
	RunPhaseReconsideration  RunPhase = "reconsideration"
	RunPhaseAdvanceExtraWave RunPhase = "advance_extra_wave"
	RunPhaseExtraWave        RunPhase = "extra_wave"
	RunPhaseFiltering        RunPhase = "filtering"
	RunPhaseExporting        RunPhase = "exporting"
	RunPhaseCompleted        RunPhase = "completed"
)

type Checkpoint string

const (
	CheckpointAfterCollectionWave Checkpoint = "after_collection_wave"
	CheckpointAfterReconsider     Checkpoint = "after_reconsideration"
	CheckpointAfterExtraWave      Checkpoint = "after_extra_wave"
)

type RunProgress struct {
	Initialized bool     `json:"initialized"`
	Wave        int      `json:"wave"`
	Phase       RunPhase `json:"phase"`
}

type ResumeCallbacks struct {
	AfterCheckpoint func(ctx context.Context, checkpoint Checkpoint, progress RunProgress, pCtx *models.PipelineContext) (pause bool, err error)
}

var ErrExecutionPaused = errors.New("dag execution paused")

// Allow initial seeds plus two discovered frontiers so roots found in the first
// follow-up wave still get a collection pass of their own.
const maxSeedExpansionDepth = 2

// Run executes the DAG synchronously for local E2E testing.
func (e *Engine) Run(ctx context.Context, pCtx *models.PipelineContext) (*models.PipelineContext, error) {
	progress := RunProgress{}
	return e.Resume(ctx, pCtx, &progress, ResumeCallbacks{})
}

func (e *Engine) Resume(ctx context.Context, pCtx *models.PipelineContext, progress *RunProgress, callbacks ResumeCallbacks) (*models.PipelineContext, error) {
	provider := telemetry.OrNoop(e.Telemetry)
	ctx = telemetry.WithProvider(ctx, provider)

	ctx, runSpan := telemetry.Start(ctx, "dag.run", telemetry.String("run_id", e.RunID))
	defer runSpan.End()

	if progress == nil {
		progress = &RunProgress{}
	}

	if !progress.Initialized {
		// The engine may execute multiple collection waves, but the stage order remains acyclic:
		// frontier -> collectors -> enrichers -> next frontier.
		pCtx.InitializeSeedFrontier(maxSeedExpansionDepth)
		progress.Initialized = true
		progress.Wave = 0
		progress.Phase = RunPhaseCollectionWave
	}

	for {
		switch progress.Phase {
		case RunPhaseCollectionWave:
			if frontier := pCtx.CollectionSeeds(); len(frontier) > 0 {
				if err := e.runWave(ctx, pCtx, progress.Wave); err != nil {
					return pCtx, err
				}
			}
			progress.Phase = RunPhaseAdvanceFrontier
			if paused, err := runCheckpointCallback(ctx, callbacks, CheckpointAfterCollectionWave, *progress, pCtx); err != nil {
				return pCtx, err
			} else if paused {
				return pCtx, ErrExecutionPaused
			}
		case RunPhaseAdvanceFrontier:
			if pCtx.AdvanceSeedFrontier() {
				progress.Wave++
				progress.Phase = RunPhaseCollectionWave
				continue
			}
			progress.Phase = RunPhaseReconsideration
		case RunPhaseReconsideration:
			if len(e.Reconsiderers) > 0 {
				pCtx.ReserveExtraCollectionWave()
				for _, reconsiderer := range e.Reconsiderers {
					err := e.processNode(ctx, "reconsider", progress.Wave, reconsiderer, pCtx)
					if err != nil {
						return pCtx, err
					}
				}
			}
			progress.Phase = RunPhaseAdvanceExtraWave
			if paused, err := runCheckpointCallback(ctx, callbacks, CheckpointAfterReconsider, *progress, pCtx); err != nil {
				return pCtx, err
			} else if paused {
				return pCtx, ErrExecutionPaused
			}
		case RunPhaseAdvanceExtraWave:
			if pCtx.AdvanceSeedFrontier() {
				progress.Wave++
				progress.Phase = RunPhaseExtraWave
				continue
			}
			progress.Phase = RunPhaseFiltering
		case RunPhaseExtraWave:
			if frontier := pCtx.CollectionSeeds(); len(frontier) > 0 {
				if err := e.runWave(ctx, pCtx, progress.Wave); err != nil {
					return pCtx, err
				}
			}
			progress.Phase = RunPhaseFiltering
			if paused, err := runCheckpointCallback(ctx, callbacks, CheckpointAfterExtraWave, *progress, pCtx); err != nil {
				return pCtx, err
			} else if paused {
				return pCtx, ErrExecutionPaused
			}
		case RunPhaseFiltering:
			for _, f := range e.Filters {
				err := e.processNode(ctx, "filter", progress.Wave, f, pCtx)
				if err != nil {
					return pCtx, err
				}
			}
			progress.Phase = RunPhaseExporting
		case RunPhaseExporting:
			for _, ex := range e.Exporters {
				err := e.processNode(ctx, "export", progress.Wave, ex, pCtx)
				if err != nil {
					return pCtx, err
				}
			}
			progress.Phase = RunPhaseCompleted
		case RunPhaseCompleted:
			return pCtx, nil
		default:
			progress.Phase = RunPhaseCompleted
			return pCtx, nil
		}
	}
}

func runCheckpointCallback(ctx context.Context, callbacks ResumeCallbacks, checkpoint Checkpoint, progress RunProgress, pCtx *models.PipelineContext) (bool, error) {
	if callbacks.AfterCheckpoint == nil {
		return false, nil
	}
	return callbacks.AfterCheckpoint(ctx, checkpoint, progress, pCtx)
}

func (e *Engine) runWave(ctx context.Context, pCtx *models.PipelineContext, wave int) error {
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
				defer func() {
					if recovered := recover(); recovered != nil {
						err := fmt.Errorf("collector %s panicked: %v", nodeTypeName(col), recovered)
						telemetry.Errorf(waveCtx, "%v\n%s", err, debug.Stack())
						pCtx.Lock()
						pCtx.Errors = append(pCtx.Errors, err)
						pCtx.Unlock()
					}
				}()
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

	for _, en := range e.Enrichers {
		err := e.processNode(ctx, "enrich", wave, en, pCtx)
		if err != nil {
			return err
		}
	}

	return nil
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
