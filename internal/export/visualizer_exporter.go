package export

import (
	"context"
	"fmt"
	"time"

	exportshared "asset-discovery/internal/export/shared"
	"asset-discovery/internal/export/visualizer"
	"asset-discovery/internal/models"
	"asset-discovery/internal/tracing/telemetry"
)

// VisualizerExporter archives run snapshots and a manifest for a client-rendered visualizer.
type VisualizerExporter struct {
	dir       string
	runID     string
	downloads Downloads
	now       func() time.Time
	store     visualizer.ArchiveStore
}

func NewVisualizerExporter(dir, runID string, downloads Downloads) *VisualizerExporter {
	return &VisualizerExporter{
		dir:       dir,
		runID:     runID,
		downloads: downloads,
		now:       time.Now,
		store:     visualizer.NewFileArchiveStore(),
	}
}

func (e *VisualizerExporter) Process(ctx context.Context, pCtx *models.PipelineContext) (*models.PipelineContext, error) {
	telemetry.Infof(ctx, "[Visualizer Exporter] Writing run history to %s...", e.dir)
	pCtx.EnsureAssetState()

	completedAt := e.now()
	exportshared.MarkEnumerationsCompleted(pCtx, completedAt)

	run := visualizer.BuildRun(e.runID, completedAt, e.downloads, pCtx)
	if err := e.store.Save(e.dir, run); err != nil {
		return pCtx, fmt.Errorf("failed to write visualizer snapshot: %w", err)
	}

	return pCtx, nil
}
