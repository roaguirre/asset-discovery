package export

import (
	"context"
	"time"

	exportshared "asset-discovery/internal/export/shared"
	"asset-discovery/internal/export/visualizer"
	"asset-discovery/internal/models"
	"asset-discovery/internal/tracing/telemetry"
)

// VisualizerExporter archives run snapshots and writes a self-contained HTML viewer.
type VisualizerExporter struct {
	filepath  string
	runID     string
	downloads Downloads
	now       func() time.Time
	service   *visualizer.Service
}

func NewVisualizerExporter(filepath, runID string, downloads Downloads) *VisualizerExporter {
	return &VisualizerExporter{
		filepath:  filepath,
		runID:     runID,
		downloads: downloads,
		now:       time.Now,
		service:   visualizer.NewService(nil, nil),
	}
}

func (e *VisualizerExporter) Process(ctx context.Context, pCtx *models.PipelineContext) (*models.PipelineContext, error) {
	telemetry.Infof(ctx, "[Visualizer Exporter] Writing run history to %s...", e.filepath)
	pCtx.EnsureAssetState()

	completedAt := e.now()
	exportshared.MarkEnumerationsCompleted(pCtx, completedAt)

	run := visualizer.BuildRun(e.runID, completedAt, e.downloads, pCtx)
	if err := e.service.Export(e.filepath, run, completedAt); err != nil {
		return pCtx, err
	}

	return pCtx, nil
}

func RefreshVisualizerHTML(path string) error {
	return visualizer.NewService(nil, nil).Refresh(path, time.Now())
}
