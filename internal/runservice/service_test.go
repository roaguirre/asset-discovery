package runservice

import (
	"context"
	"errors"
	"testing"
	"time"

	"asset-discovery/internal/app"
	"asset-discovery/internal/dag"
	"asset-discovery/internal/models"
	"asset-discovery/internal/tracing/telemetry"
)

type scriptedCollector struct{}

func (c *scriptedCollector) Process(_ context.Context, pCtx *models.PipelineContext) (*models.PipelineContext, error) {
	for _, seed := range pCtx.CollectionSeeds() {
		if len(seed.Domains) == 0 {
			continue
		}

		pCtx.AppendAssets(models.Asset{
			ID:            models.NewID("asset"),
			EnumerationID: "enum-" + seed.ID,
			Type:          models.AssetTypeDomain,
			Identifier:    seed.Domains[0],
			Source:        "scripted_collector",
			DiscoveryDate: time.Date(2026, time.March, 25, 12, 0, 0, 0, time.UTC),
			DomainDetails: &models.DomainDetails{},
		})

		if seed.ID != "seed-root" {
			continue
		}

		pCtx.RecordJudgeEvaluation(models.JudgeEvaluation{
			Collector:   "web_hint_collector",
			SeedID:      seed.ID,
			SeedLabel:   "Root Seed",
			SeedDomains: []string{seed.Domains[0]},
			Scenario:    "web ownership hints",
			Outcomes: []models.JudgeCandidateOutcome{
				{
					Root:       "pivot.example.com",
					Collect:    true,
					Confidence: 0.96,
					Kind:       "brand_overlap",
					Reason:     "Homepage links and brand overlap point to the same organization.",
					Support:    []string{"Homepage references pivot.example.com"},
				},
			},
		})

		pCtx.EnqueueSeedCandidate(models.Seed{
			ID:          "seed-pivot",
			CompanyName: "Pivot Example",
			Domains:     []string{"pivot.example.com"},
			Tags:        []string{"web-hint-pivot"},
		}, models.SeedEvidence{
			Source:     "web_hint_collector",
			Kind:       "brand_overlap",
			Value:      "pivot.example.com",
			Confidence: 0.96,
			Reasoned:   true,
		})
	}

	return pCtx, nil
}

func newTestService(t *testing.T, mode RunMode) (*Service, *MemoryCheckpointStore, *MemoryProjectionStore, RunRecord) {
	t.Helper()

	checkpoints := NewMemoryCheckpointStore()
	projection := NewMemoryProjectionStore()
	now := func() time.Time {
		return time.Date(2026, time.March, 25, 12, 0, 0, 0, time.UTC)
	}

	factory := func(runID string) (*app.Pipeline, error) {
		engine := &dag.Engine{
			Collectors: []dag.Collector{&scriptedCollector{}},
		}
		return app.NewPipelineWithEngine(engine, runID, nil, telemetry.Noop()), nil
	}

	service, err := NewService(Config{
		PipelineFactory: factory,
		Checkpoints:     checkpoints,
		Projection:      projection,
		Now:             now,
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	run, err := service.CreateRun(context.Background(), AuthenticatedUser{
		UID:           "uid-1",
		Email:         "reviewer@zerofox.com",
		EmailVerified: true,
	}, CreateRunRequest{
		Mode: mode,
		Seeds: []models.Seed{
			{
				ID:          "seed-root",
				CompanyName: "Root Example",
				Domains:     []string{"root.example.com"},
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}

	return service, checkpoints, projection, run
}

func TestService_ProcessRun_PausesForManualReviewAndResumes(t *testing.T) {
	service, checkpoints, projection, run := newTestService(t, RunModeManual)

	if err := service.ProcessRun(context.Background(), run.ID); err != nil {
		t.Fatalf("ProcessRun(manual initial) error = %v", err)
	}

	snapshot, err := checkpoints.Load(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if snapshot.Run.Status != RunStatusAwaitingReview {
		t.Fatalf("expected awaiting_review status, got %s", snapshot.Run.Status)
	}
	if snapshot.Run.PendingPivotCount != 1 {
		t.Fatalf("expected one pending pivot, got %d", snapshot.Run.PendingPivotCount)
	}
	if len(projection.Pivots[run.ID]) != 1 {
		t.Fatalf("expected one projected pivot, got %d", len(projection.Pivots[run.ID]))
	}

	var pivotID string
	for _, pivot := range projection.Pivots[run.ID] {
		pivotID = pivot.ID
	}

	if _, err := service.DecidePivot(context.Background(), AuthenticatedUser{
		UID:           "uid-1",
		Email:         "reviewer@zerofox.com",
		EmailVerified: true,
	}, run.ID, pivotID, PivotDecisionInputAccepted); err != nil {
		t.Fatalf("DecidePivot() error = %v", err)
	}

	if err := service.ProcessRun(context.Background(), run.ID); err != nil {
		t.Fatalf("ProcessRun(manual resume) error = %v", err)
	}

	snapshot, err = checkpoints.Load(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("Load() after resume error = %v", err)
	}
	if snapshot.Run.Status != RunStatusCompleted {
		t.Fatalf("expected completed status, got %s", snapshot.Run.Status)
	}
	if len(snapshot.Context.Seeds) != 2 {
		t.Fatalf("expected accepted pivot to be added as a seed, got %d seed(s)", len(snapshot.Context.Seeds))
	}
	if len(projection.Assets[run.ID]) == 0 {
		t.Fatalf("expected projected assets to be present")
	}
	if len(projection.Events[run.ID]) == 0 {
		t.Fatalf("expected mutation events to be projected")
	}
}

func TestService_ProcessRun_AutonomousAutoAcceptsPivot(t *testing.T) {
	service, checkpoints, projection, run := newTestService(t, RunModeAutonomous)

	if err := service.ProcessRun(context.Background(), run.ID); err != nil {
		t.Fatalf("ProcessRun(autonomous) error = %v", err)
	}

	snapshot, err := checkpoints.Load(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if snapshot.Run.Status != RunStatusCompleted {
		t.Fatalf("expected completed status, got %s", snapshot.Run.Status)
	}
	if len(snapshot.Context.Seeds) != 2 {
		t.Fatalf("expected auto-accepted pivot to add a seed, got %d seed(s)", len(snapshot.Context.Seeds))
	}

	autoAccepted := false
	for _, pivot := range projection.Pivots[run.ID] {
		if pivot.Status == PivotDecisionAutoAccepted {
			autoAccepted = true
		}
	}
	if !autoAccepted {
		t.Fatalf("expected an auto_accepted pivot to be projected")
	}
}

func TestService_DecidePivotRejectsNonOwner(t *testing.T) {
	service, _, projection, run := newTestService(t, RunModeManual)

	if err := service.ProcessRun(context.Background(), run.ID); err != nil {
		t.Fatalf("ProcessRun() error = %v", err)
	}

	var pivotID string
	for _, pivot := range projection.Pivots[run.ID] {
		pivotID = pivot.ID
	}

	_, err := service.DecidePivot(context.Background(), AuthenticatedUser{
		UID:           "uid-2",
		Email:         "other@zerofox.com",
		EmailVerified: true,
	}, run.ID, pivotID, PivotDecisionInputAccepted)
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected forbidden error, got %v", err)
	}
}

func TestService_DecidePivotMissingPivot(t *testing.T) {
	service, _, _, run := newTestService(t, RunModeManual)

	if err := service.ProcessRun(context.Background(), run.ID); err != nil {
		t.Fatalf("ProcessRun() error = %v", err)
	}

	_, err := service.DecidePivot(context.Background(), AuthenticatedUser{
		UID:           "uid-1",
		Email:         "reviewer@zerofox.com",
		EmailVerified: true,
	}, run.ID, "missing", PivotDecisionInputAccepted)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected not found error, got %v", err)
	}
}

func TestService_DecidePivotRejectsNonPendingPivot(t *testing.T) {
	service, _, projection, run := newTestService(t, RunModeManual)

	if err := service.ProcessRun(context.Background(), run.ID); err != nil {
		t.Fatalf("ProcessRun() error = %v", err)
	}

	var pivotID string
	for _, pivot := range projection.Pivots[run.ID] {
		pivotID = pivot.ID
	}

	if _, err := service.DecidePivot(context.Background(), AuthenticatedUser{
		UID:           "uid-1",
		Email:         "reviewer@zerofox.com",
		EmailVerified: true,
	}, run.ID, pivotID, PivotDecisionInputAccepted); err != nil {
		t.Fatalf("first DecidePivot() error = %v", err)
	}

	_, err := service.DecidePivot(context.Background(), AuthenticatedUser{
		UID:           "uid-1",
		Email:         "reviewer@zerofox.com",
		EmailVerified: true,
	}, run.ID, pivotID, PivotDecisionInputAccepted)
	if err == nil {
		t.Fatal("expected second DecidePivot() to fail")
	}
	if errors.Is(err, ErrForbidden) || errors.Is(err, ErrNotFound) {
		t.Fatalf("expected bad request error, got %v", err)
	}
}
