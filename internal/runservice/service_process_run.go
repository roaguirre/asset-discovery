package runservice

import (
	"context"
	"errors"
	"fmt"
	"log"
	"runtime/debug"
	"strings"

	"asset-discovery/internal/app"
	"asset-discovery/internal/dag"
	export "asset-discovery/internal/export"
	"asset-discovery/internal/models"
)

// ProcessRun resumes a persisted run snapshot through the shared pipeline and
// keeps the checkpoint and projection stores synchronized at each boundary.
func (s *Service) ProcessRun(ctx context.Context, runID string) error {
	return (&runProcessor{
		service: s,
		runID:   runID,
	}).process(ctx)
}

type runProcessor struct {
	service        *Service
	runID          string
	snapshot       Snapshot
	snapshotLoaded bool
	broker         *pivotBroker
}

func (p *runProcessor) process(ctx context.Context) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = p.handlePanic(ctx, recovered)
		}
	}()

	if err := p.loadSnapshot(ctx); err != nil {
		return err
	}

	pipeline, localDownloads, err := p.buildPipeline()
	if err != nil {
		return err
	}
	if err := p.markRunning(ctx); err != nil {
		return err
	}

	listener := newProjectionMutationListener(
		ctx,
		p.snapshot.Run,
		p.snapshot.Context,
		p.service.projection,
		p.service.now,
	)
	p.snapshot.Context.SetMutationListener(listener)

	_, err = pipeline.Resume(ctx, p.snapshot.Context, &p.snapshot.Progress, dag.ResumeCallbacks{
		AfterCheckpoint: p.afterCheckpoint,
	})

	if listener.Err() != nil {
		return listener.Err()
	}

	switch {
	case errors.Is(err, dag.ErrExecutionPaused):
		return nil
	case err != nil:
		return p.persistPipelineFailure(ctx, err)
	default:
		return p.completeRun(ctx, localDownloads)
	}
}

func (p *runProcessor) loadSnapshot(ctx context.Context) error {
	snapshot, err := p.service.checkpoints.Load(ctx, p.runID)
	if err != nil {
		return fmt.Errorf("load snapshot: %w", err)
	}

	p.snapshot = snapshot
	p.snapshotLoaded = true
	p.snapshot.ensureContext()
	p.snapshot.Context.RestoreSchedulerState(p.snapshot.SchedulerState)
	p.snapshot.Context.SetCandidatePromotionConfidenceThreshold(
		candidatePromotionConfidenceThreshold(p.snapshot.Run.Mode),
	)
	if p.snapshot.Pivots == nil {
		p.snapshot.Pivots = make(map[string]PendingPivotState)
	}

	p.broker = newPivotBroker(p.snapshot.Run.Mode, p.snapshot.Pivots, p.service.now)
	p.snapshot.Context.SetCandidatePromotionHandler(p.broker)
	return nil
}

func (p *runProcessor) buildPipeline() (*app.Pipeline, export.Downloads, error) {
	pipeline, err := p.service.pipelineFactory(p.runID)
	if err != nil {
		return nil, export.Downloads{}, fmt.Errorf("build pipeline: %w", err)
	}
	return pipeline, buildDownloads(pipeline.Outputs()), nil
}

func (p *runProcessor) markRunning(ctx context.Context) error {
	now := p.service.now()
	p.snapshot.Run.Status = RunStatusRunning
	p.snapshot.Run.UpdatedAt = now
	if p.snapshot.Run.StartedAt == nil {
		p.snapshot.Run.StartedAt = &now
	}
	updateRunCounters(&p.snapshot)

	if err := p.service.projection.UpsertRun(ctx, p.snapshot.Run); err != nil {
		return fmt.Errorf("project running state: %w", err)
	}
	if err := p.service.projection.AppendEvent(ctx, p.runID, EventRecord{
		ID:        models.NewID("event"),
		Kind:      "run_started",
		Message:   fmt.Sprintf("Run %s started in %s mode.", p.runID, p.snapshot.Run.Mode),
		CreatedAt: now,
	}); err != nil {
		return fmt.Errorf("project start event: %w", err)
	}
	return nil
}

func (p *runProcessor) afterCheckpoint(
	ctx context.Context,
	checkpoint dag.Checkpoint,
	progress dag.RunProgress,
	pCtx *models.PipelineContext,
) (bool, error) {
	p.snapshot.Progress = progress
	p.snapshot.SchedulerState = pCtx.SnapshotSchedulerState()
	p.snapshot.Pivots = p.broker.Snapshot()
	if err := hydratePivotsFromJudges(&p.snapshot, pCtx, p.service.now()); err != nil {
		return false, err
	}

	updateRunCounters(&p.snapshot)
	p.snapshot.Run.CurrentWave = progress.Wave
	p.snapshot.Run.UpdatedAt = p.service.now()
	if hasPendingReview(p.snapshot.Pivots) {
		p.snapshot.Run.Status = RunStatusAwaitingReview
	} else {
		p.snapshot.Run.Status = RunStatusRunning
	}

	if err := p.service.checkpoints.Save(ctx, p.runID, p.snapshot); err != nil {
		return false, fmt.Errorf("save checkpoint: %w", err)
	}
	if err := p.service.projectSnapshot(ctx, &p.snapshot); err != nil {
		return false, err
	}
	if err := p.service.projection.AppendEvent(ctx, p.runID, EventRecord{
		ID:        models.NewID("event"),
		Kind:      "checkpoint",
		Message:   fmt.Sprintf("Reached %s at wave %d.", checkpoint, progress.Wave),
		CreatedAt: p.service.now(),
		Metadata: map[string]interface{}{
			"checkpoint": checkpoint,
			"wave":       progress.Wave,
		},
	}); err != nil {
		return false, fmt.Errorf("project checkpoint event: %w", err)
	}
	return hasPendingReview(p.snapshot.Pivots), nil
}

func (p *runProcessor) persistPipelineFailure(ctx context.Context, runErr error) error {
	failedAt := p.service.now()
	p.snapshot.Run.Status = RunStatusFailed
	p.snapshot.Run.LastError = runErr.Error()
	p.snapshot.Run.UpdatedAt = failedAt
	p.snapshot.SchedulerState = p.snapshot.Context.SnapshotSchedulerState()
	p.snapshot.Pivots = p.broker.Snapshot()
	updateRunCounters(&p.snapshot)

	if saveErr := p.service.checkpoints.Save(ctx, p.runID, p.snapshot); saveErr != nil {
		return fmt.Errorf("save failed snapshot: %v (original error: %w)", saveErr, runErr)
	}
	if projectErr := p.service.projectSnapshot(ctx, &p.snapshot); projectErr != nil {
		return fmt.Errorf("project failed run: %v (original error: %w)", projectErr, runErr)
	}
	return runErr
}

func (p *runProcessor) completeRun(ctx context.Context, localDownloads export.Downloads) error {
	publishedDownloads, publishErr := p.service.artifacts.Publish(ctx, p.runID, localDownloads)
	if publishErr != nil {
		return p.persistArtifactFailure(ctx, publishErr)
	}

	completedAt := p.service.now()
	p.snapshot.Run.Downloads = publishedDownloads
	p.snapshot.Run.Status = RunStatusCompleted
	p.snapshot.Run.UpdatedAt = completedAt
	p.snapshot.Run.CompletedAt = &completedAt
	p.snapshot.Run.LastError = ""
	p.snapshot.SchedulerState = p.snapshot.Context.SnapshotSchedulerState()
	p.snapshot.Pivots = p.broker.Snapshot()
	if err := hydratePivotsFromJudges(&p.snapshot, p.snapshot.Context, p.service.now()); err != nil {
		return err
	}
	updateRunCounters(&p.snapshot)

	if err := p.service.checkpoints.Save(ctx, p.runID, p.snapshot); err != nil {
		return fmt.Errorf("save completed snapshot: %w", err)
	}
	if err := p.service.projectSnapshot(ctx, &p.snapshot); err != nil {
		return err
	}
	if err := p.service.projection.AppendEvent(ctx, p.runID, EventRecord{
		ID:        models.NewID("event"),
		Kind:      "artifacts_published",
		Message:   fmt.Sprintf("Published result artifacts for run %s.", p.runID),
		CreatedAt: completedAt,
		Metadata: map[string]interface{}{
			"json": p.snapshot.Run.Downloads.JSON,
			"csv":  p.snapshot.Run.Downloads.CSV,
			"xlsx": p.snapshot.Run.Downloads.XLSX,
		},
	}); err != nil {
		return fmt.Errorf("project artifact publish event: %w", err)
	}
	return p.service.projection.AppendEvent(ctx, p.runID, EventRecord{
		ID:        models.NewID("event"),
		Kind:      "run_completed",
		Message:   fmt.Sprintf("Run %s completed with %d asset(s).", p.runID, p.snapshot.Run.AssetCount),
		CreatedAt: completedAt,
	})
}

func (p *runProcessor) persistArtifactFailure(ctx context.Context, publishErr error) error {
	failedAt := p.service.now()
	p.snapshot.Run.Status = RunStatusFailed
	p.snapshot.Run.LastError = fmt.Sprintf("publish artifacts: %v", publishErr)
	p.snapshot.Run.UpdatedAt = failedAt
	p.snapshot.SchedulerState = p.snapshot.Context.SnapshotSchedulerState()
	p.snapshot.Pivots = p.broker.Snapshot()
	updateRunCounters(&p.snapshot)

	if saveErr := p.service.checkpoints.Save(ctx, p.runID, p.snapshot); saveErr != nil {
		return fmt.Errorf("save artifact failure snapshot: %v (artifact error: %w)", saveErr, publishErr)
	}
	if projectErr := p.service.projectSnapshot(ctx, &p.snapshot); projectErr != nil {
		return fmt.Errorf("project artifact failure: %v (artifact error: %w)", projectErr, publishErr)
	}
	if eventErr := p.service.projection.AppendEvent(ctx, p.runID, EventRecord{
		ID:        models.NewID("event"),
		Kind:      "artifact_publish_failed",
		Message:   fmt.Sprintf("Run %s failed while publishing result artifacts.", p.runID),
		CreatedAt: failedAt,
		Metadata: map[string]interface{}{
			"error": publishErr.Error(),
		},
	}); eventErr != nil {
		return fmt.Errorf("project artifact failure event: %v (artifact error: %w)", eventErr, publishErr)
	}
	return fmt.Errorf("publish artifacts: %w", publishErr)
}

func (p *runProcessor) handlePanic(ctx context.Context, recovered interface{}) error {
	panicErr := fmt.Errorf("panic while processing run %s: %v", p.runID, recovered)
	log.Printf("panic while processing run %s: %v\n%s", p.runID, recovered, debug.Stack())
	if !p.snapshotLoaded {
		return panicErr
	}

	failedAt := p.service.now()
	p.snapshot.Run.Status = RunStatusFailed
	p.snapshot.Run.LastError = panicErr.Error()
	p.snapshot.Run.UpdatedAt = failedAt
	if p.snapshot.Context != nil {
		p.snapshot.SchedulerState = p.snapshot.Context.SnapshotSchedulerState()
	}
	updateRunCounters(&p.snapshot)

	if saveErr := p.service.checkpoints.Save(ctx, p.runID, p.snapshot); saveErr != nil {
		log.Printf("save panic snapshot for %s: %v", p.runID, saveErr)
	}
	if projectErr := p.service.projectSnapshot(ctx, &p.snapshot); projectErr != nil {
		log.Printf("project panic snapshot for %s: %v", p.runID, projectErr)
	}
	return panicErr
}

func (s *Service) projectSnapshot(ctx context.Context, snapshot *Snapshot) error {
	snapshot.ensureContext()
	rows, traces := buildProjectedAssetReadModel(snapshot.Run.ID, snapshot.Context)
	judgeSummary := buildProjectedJudgeSummary(snapshot.Context)
	applyProjectedRunMetrics(&snapshot.Run, snapshot.Context, countPendingPivots(snapshot.Pivots), judgeSummary)

	if err := s.projection.UpsertRun(ctx, snapshot.Run); err != nil {
		return fmt.Errorf("upsert run: %w", err)
	}
	if err := s.projection.UpsertJudgeSummary(ctx, snapshot.Run.ID, judgeSummary); err != nil {
		return fmt.Errorf("upsert judge summary: %w", err)
	}
	for _, row := range rows {
		if err := s.projection.UpsertAsset(ctx, snapshot.Run.ID, row); err != nil {
			return fmt.Errorf("upsert asset %s: %w", row.AssetID, err)
		}
	}
	if err := s.projection.SyncTraces(ctx, snapshot.Run.ID, traces); err != nil {
		return fmt.Errorf("sync traces: %w", err)
	}
	for _, pivot := range sortedPivotStates(snapshot.Pivots) {
		if err := s.projection.UpsertPivot(ctx, snapshot.Run.ID, buildPivotRecord(pivot)); err != nil {
			return fmt.Errorf("upsert pivot %s: %w", pivot.ID, err)
		}
	}
	return nil
}

func buildDownloads(outputs []string) export.Downloads {
	downloads := export.Downloads{}
	for _, output := range outputs {
		switch {
		case strings.HasSuffix(strings.ToLower(output), ".json"):
			downloads.JSON = output
		case strings.HasSuffix(strings.ToLower(output), ".csv"):
			downloads.CSV = output
		case strings.HasSuffix(strings.ToLower(output), ".xlsx"):
			downloads.XLSX = output
		}
	}
	return downloads
}

func updateRunCounters(snapshot *Snapshot) {
	snapshot.ensureContext()
	judgeSummary := buildProjectedJudgeSummary(snapshot.Context)
	applyProjectedRunMetrics(&snapshot.Run, snapshot.Context, countPendingPivots(snapshot.Pivots), judgeSummary)
	snapshot.Run.CurrentWave = snapshot.Progress.Wave
}
