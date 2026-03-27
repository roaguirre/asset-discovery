package runservice

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"asset-discovery/internal/app"
	"asset-discovery/internal/dag"
	export "asset-discovery/internal/export"
	"asset-discovery/internal/models"
	"asset-discovery/internal/ownership"
	"asset-discovery/internal/tracing/telemetry"
)

type scriptedCollector struct{}

type capturingArtifactStore struct {
	fail      error
	published export.Downloads
	calls     []export.Downloads
}

// statusTrackingProjectionStore records the run statuses projected over time so
// tests can catch stale live-state regressions during streaming updates.
type statusTrackingProjectionStore struct {
	*MemoryProjectionStore

	mu       sync.Mutex
	statuses map[string][]RunStatus
}

func newStatusTrackingProjectionStore() *statusTrackingProjectionStore {
	return &statusTrackingProjectionStore{
		MemoryProjectionStore: NewMemoryProjectionStore(),
		statuses:              make(map[string][]RunStatus),
	}
}

func (s *statusTrackingProjectionStore) UpsertRun(ctx context.Context, run RunRecord) error {
	s.mu.Lock()
	s.statuses[run.ID] = append(s.statuses[run.ID], run.Status)
	s.mu.Unlock()

	return s.MemoryProjectionStore.UpsertRun(ctx, run)
}

func (s *capturingArtifactStore) Publish(
	_ context.Context,
	_ string,
	downloads export.Downloads,
) (export.Downloads, error) {
	s.calls = append(s.calls, downloads)
	if s.fail != nil {
		return export.Downloads{}, s.fail
	}
	if s.published.JSON != "" || s.published.CSV != "" || s.published.XLSX != "" {
		return s.published, nil
	}
	return downloads, nil
}

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

func newTestService(
	t *testing.T,
	mode RunMode,
	store *capturingArtifactStore,
) (*Service, *MemoryCheckpointStore, *MemoryProjectionStore, RunRecord) {
	projection := NewMemoryProjectionStore()
	service, checkpoints, run := newTestServiceWithProjection(t, mode, store, projection)
	return service, checkpoints, projection, run
}

func newTestServiceWithProjection(
	t *testing.T,
	mode RunMode,
	store *capturingArtifactStore,
	projection ProjectionStore,
) (*Service, *MemoryCheckpointStore, RunRecord) {
	t.Helper()

	checkpoints := NewMemoryCheckpointStore()
	outputRoot := t.TempDir()
	now := func() time.Time {
		return time.Date(2026, time.March, 25, 12, 0, 0, 0, time.UTC)
	}
	if store == nil {
		store = &capturingArtifactStore{}
	}

	factory := func(runID string) (*app.Pipeline, error) {
		engine := &dag.Engine{
			Collectors: []dag.Collector{&scriptedCollector{}},
			Exporters: []dag.Exporter{
				export.NewJSONExporter(filepath.Join(outputRoot, runID, "results.json")),
				export.NewCSVExporter(filepath.Join(outputRoot, runID, "results.csv")),
				export.NewXLSXExporter(filepath.Join(outputRoot, runID, "results.xlsx")),
			},
		}
		outputs := []string{
			filepath.Join(outputRoot, runID, "results.json"),
			filepath.Join(outputRoot, runID, "results.csv"),
			filepath.Join(outputRoot, runID, "results.xlsx"),
		}
		return app.NewPipelineWithEngine(engine, runID, outputs, telemetry.Noop()), nil
	}

	service, err := NewService(Config{
		PipelineFactory: factory,
		Checkpoints:     checkpoints,
		Projection:      projection,
		Artifacts:       store,
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

	return service, checkpoints, run
}

func TestService_ProcessRun_DoesNotReprojectQueuedStatusAfterRunningStarts(t *testing.T) {
	projection := newStatusTrackingProjectionStore()
	service, _, run := newTestServiceWithProjection(
		t,
		RunModeManual,
		nil,
		projection,
	)

	if err := service.ProcessRun(context.Background(), run.ID); err != nil {
		t.Fatalf("ProcessRun() error = %v", err)
	}

	statuses := projection.statuses[run.ID]
	if len(statuses) < 2 {
		t.Fatalf("expected multiple projected statuses, got %+v", statuses)
	}

	runningIndex := -1
	for index, status := range statuses {
		if status == RunStatusRunning {
			runningIndex = index
			break
		}
	}
	if runningIndex == -1 {
		t.Fatalf("expected running status in projected sequence, got %+v", statuses)
	}

	for _, status := range statuses[runningIndex+1:] {
		if status == RunStatusQueued {
			t.Fatalf("queued status was re-projected after running started: %+v", statuses)
		}
	}
}

func TestCandidatePromotionConfidenceThreshold_UsesModeSpecificThresholds(t *testing.T) {
	testCases := []struct {
		name string
		mode RunMode
		want float64
	}{
		{
			name: "manual",
			mode: RunModeManual,
			want: ownership.ManualReviewConfidenceThreshold,
		},
		{
			name: "autonomous",
			mode: RunModeAutonomous,
			want: ownership.DefaultHighConfidenceThreshold,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := candidatePromotionConfidenceThreshold(testCase.mode); got != testCase.want {
				t.Fatalf("candidatePromotionConfidenceThreshold(%q) = %v, want %v", testCase.mode, got, testCase.want)
			}
		})
	}
}

func TestService_ProcessRun_PausesForManualReviewAndResumes(t *testing.T) {
	artifactStore := &capturingArtifactStore{
		published: export.Downloads{
			JSON: "runs/run-manual/results.json",
			CSV:  "runs/run-manual/results.csv",
			XLSX: "runs/run-manual/results.xlsx",
		},
	}
	service, checkpoints, projection, run := newTestService(t, RunModeManual, artifactStore)

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
	if snapshot.Run.Downloads != (export.Downloads{}) {
		t.Fatalf("expected downloads to stay empty before completion, got %+v", snapshot.Run.Downloads)
	}
	if snapshot.Run.JudgeEvaluationCount != 1 || snapshot.Run.JudgeAcceptedCount != 1 || snapshot.Run.JudgeDiscardedCount != 0 {
		t.Fatalf("expected judge counters to be tracked in the snapshot, got %+v", snapshot.Run)
	}
	if len(projection.Pivots[run.ID]) != 1 {
		t.Fatalf("expected one projected pivot, got %d", len(projection.Pivots[run.ID]))
	}
	if summary := projection.JudgeSummaries[run.ID]; summary.EvaluationCount != 1 || summary.AcceptedCount != 1 || summary.DiscardedCount != 0 {
		t.Fatalf("expected projected judge summary to be available, got %+v", summary)
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
	if snapshot.Run.Downloads != artifactStore.published {
		t.Fatalf("expected published downloads in snapshot, got %+v", snapshot.Run.Downloads)
	}
	if len(snapshot.Context.Seeds) != 2 {
		t.Fatalf("expected accepted pivot to be added as a seed, got %d seed(s)", len(snapshot.Context.Seeds))
	}
	if len(projection.Assets[run.ID]) == 0 {
		t.Fatalf("expected projected assets to be present")
	}
	if summary := projection.JudgeSummaries[run.ID]; summary.EvaluationCount != 1 || summary.AcceptedCount != 1 || summary.DiscardedCount != 0 {
		t.Fatalf("expected projected judge summary to survive resume, got %+v", summary)
	}
	if len(projection.Events[run.ID]) == 0 {
		t.Fatalf("expected mutation events to be projected")
	}
	if len(artifactStore.calls) != 1 {
		t.Fatalf("expected one artifact publish call, got %d", len(artifactStore.calls))
	}
}

func TestService_ProcessRun_AutonomousAutoAcceptsPivot(t *testing.T) {
	artifactStore := &capturingArtifactStore{
		published: export.Downloads{
			JSON: "runs/run-auto/results.json",
			CSV:  "runs/run-auto/results.csv",
			XLSX: "runs/run-auto/results.xlsx",
		},
	}
	service, checkpoints, projection, run := newTestService(t, RunModeAutonomous, artifactStore)

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
	if snapshot.Run.Downloads != artifactStore.published {
		t.Fatalf("expected published downloads, got %+v", snapshot.Run.Downloads)
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
	service, _, projection, run := newTestService(t, RunModeManual, &capturingArtifactStore{})

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
	service, _, _, run := newTestService(t, RunModeManual, &capturingArtifactStore{})

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
	service, _, projection, run := newTestService(t, RunModeManual, &capturingArtifactStore{})

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

func TestService_ProcessRun_FailsWhenArtifactPublishFails(t *testing.T) {
	service, checkpoints, projection, run := newTestService(t, RunModeAutonomous, &capturingArtifactStore{
		fail: errors.New("bucket write failed"),
	})

	err := service.ProcessRun(context.Background(), run.ID)
	if err == nil {
		t.Fatal("expected artifact publish failure to be returned")
	}
	if got := err.Error(); got == "" || !strings.Contains(got, "publish artifacts") {
		t.Fatalf("expected artifact publish error, got %v", err)
	}

	snapshot, loadErr := checkpoints.Load(context.Background(), run.ID)
	if loadErr != nil {
		t.Fatalf("Load() error = %v", loadErr)
	}
	if snapshot.Run.Status != RunStatusFailed {
		t.Fatalf("expected failed status, got %s", snapshot.Run.Status)
	}
	if snapshot.Run.LastError == "" {
		t.Fatal("expected artifact failure to populate last_error")
	}
	if snapshot.Run.Downloads != (export.Downloads{}) {
		t.Fatalf("expected downloads to stay empty after artifact failure, got %+v", snapshot.Run.Downloads)
	}
	foundFailureEvent := false
	for _, event := range projection.Events[run.ID] {
		if event.Kind == "artifact_publish_failed" {
			foundFailureEvent = true
		}
	}
	if !foundFailureEvent {
		t.Fatal("expected artifact publish failure event to be projected")
	}
}
