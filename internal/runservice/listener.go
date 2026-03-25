package runservice

import (
	"context"
	"fmt"
	"sync"
	"time"

	"asset-discovery/internal/export/visualizer"
	"asset-discovery/internal/models"
)

type projectionMutationListener struct {
	ctx        context.Context
	run        RunRecord
	pCtx       *models.PipelineContext
	projection ProjectionStore
	now        func() time.Time

	mu  sync.Mutex
	err error
}

func newProjectionMutationListener(
	ctx context.Context,
	run RunRecord,
	pCtx *models.PipelineContext,
	projection ProjectionStore,
	now func() time.Time,
) *projectionMutationListener {
	return &projectionMutationListener{
		ctx:        ctx,
		run:        run,
		pCtx:       pCtx,
		projection: projection,
		now:        now,
	}
}

func (l *projectionMutationListener) OnAssetUpsert(asset models.Asset) {
	snapshot := l.pCtx.SnapshotReadModel()
	row, ok := buildProjectedRow(l.run, &snapshot, asset.ID)
	if !ok {
		return
	}
	if err := l.projection.UpsertAsset(l.ctx, l.run.ID, row); err != nil {
		l.setErr(fmt.Errorf("project asset %s: %w", asset.ID, err))
		return
	}
	if err := l.syncRunProjection(snapshot); err != nil {
		l.setErr(err)
	}
}

func (l *projectionMutationListener) OnObservationAdded(observation models.AssetObservation) {
	if err := l.projection.AppendEvent(l.ctx, l.run.ID, EventRecord{
		ID:        models.NewID("event"),
		Kind:      "observation_added",
		Message:   fmt.Sprintf("Observation %s recorded for %s.", observation.ID, observation.Identifier),
		CreatedAt: l.now(),
		Metadata: map[string]interface{}{
			"observation_id": observation.ID,
			"asset_id":       observation.AssetID,
			"identifier":     observation.Identifier,
			"source":         observation.Source,
		},
	}); err != nil {
		l.setErr(fmt.Errorf("project observation event %s: %w", observation.ID, err))
	}
}

func (l *projectionMutationListener) OnRelationAdded(relation models.AssetRelation) {
	if err := l.projection.AppendEvent(l.ctx, l.run.ID, EventRecord{
		ID:        models.NewID("event"),
		Kind:      "relation_added",
		Message:   fmt.Sprintf("Relation %s linked %s to %s.", relation.ID, relation.FromIdentifier, relation.ToIdentifier),
		CreatedAt: l.now(),
		Metadata: map[string]interface{}{
			"relation_id": relation.ID,
			"kind":        relation.Kind,
			"source":      relation.Source,
		},
	}); err != nil {
		l.setErr(fmt.Errorf("project relation event %s: %w", relation.ID, err))
	}
}

func (l *projectionMutationListener) OnJudgeEvaluationRecorded(evaluation models.JudgeEvaluation) {
	if err := l.projection.AppendEvent(l.ctx, l.run.ID, EventRecord{
		ID:        models.NewID("event"),
		Kind:      "judge_evaluation",
		Message:   fmt.Sprintf("Recorded %d judge outcome(s) from %s.", len(evaluation.Outcomes), evaluation.Collector),
		CreatedAt: l.now(),
		Metadata: map[string]interface{}{
			"collector": evaluation.Collector,
			"seed_id":   evaluation.SeedID,
			"scenario":  evaluation.Scenario,
		},
	}); err != nil {
		l.setErr(fmt.Errorf("project judge event from %s: %w", evaluation.Collector, err))
	}
}

func (l *projectionMutationListener) Err() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.err
}

func (l *projectionMutationListener) setErr(err error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.err == nil {
		l.err = err
	}
}

func buildProjectedRow(run RunRecord, pCtx *models.PipelineContext, assetID string) (visualizer.Row, bool) {
	projected := visualizer.BuildRun(run.ID, run.CreatedAt, run.Downloads, pCtx)
	for _, row := range projected.Rows {
		if row.AssetID == assetID {
			return row, true
		}
	}
	return visualizer.Row{}, false
}

func (l *projectionMutationListener) syncRunProjection(snapshot models.PipelineContext) error {
	l.mu.Lock()
	l.run.AssetCount = len(snapshot.Assets)
	l.run.EnumerationCount = len(snapshot.Enumerations)
	l.run.SeedCount = len(snapshot.Seeds)
	l.run.UpdatedAt = l.now()
	run := l.run
	l.mu.Unlock()

	if err := l.projection.UpsertRun(l.ctx, run); err != nil {
		return fmt.Errorf("project run counts: %w", err)
	}
	return nil
}
