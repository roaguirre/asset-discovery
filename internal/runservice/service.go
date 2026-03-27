package runservice

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"asset-discovery/internal/app"
	"asset-discovery/internal/dag"
	"asset-discovery/internal/discovery"
	export "asset-discovery/internal/export"
	"asset-discovery/internal/models"
	"asset-discovery/internal/ownership"
)

type PipelineFactory func(runID string) (*app.Pipeline, error)

type Config struct {
	PipelineFactory PipelineFactory
	Checkpoints     CheckpointStore
	Projection      ProjectionStore
	Artifacts       ArtifactStore
	Dispatcher      Dispatcher
	Now             func() time.Time
}

type Service struct {
	pipelineFactory PipelineFactory
	checkpoints     CheckpointStore
	projection      ProjectionStore
	artifacts       ArtifactStore
	dispatcher      Dispatcher
	now             func() time.Time
}

func NewService(cfg Config) (*Service, error) {
	if cfg.PipelineFactory == nil {
		return nil, errors.New("pipeline factory is required")
	}
	if cfg.Checkpoints == nil {
		return nil, errors.New("checkpoint store is required")
	}
	if cfg.Projection == nil {
		return nil, errors.New("projection store is required")
	}
	if cfg.Artifacts == nil {
		return nil, errors.New("artifact store is required")
	}

	nowFn := cfg.Now
	if nowFn == nil {
		nowFn = time.Now
	}

	return &Service{
		pipelineFactory: cfg.PipelineFactory,
		checkpoints:     cfg.Checkpoints,
		projection:      cfg.Projection,
		artifacts:       cfg.Artifacts,
		dispatcher:      cfg.Dispatcher,
		now:             nowFn,
	}, nil
}

func (s *Service) SetDispatcher(dispatcher Dispatcher) {
	s.dispatcher = dispatcher
}

func (s *Service) CreateRun(ctx context.Context, user AuthenticatedUser, request CreateRunRequest) (RunRecord, error) {
	mode := request.Mode
	if mode == "" {
		mode = RunModeAutonomous
	}
	if mode != RunModeAutonomous && mode != RunModeManual {
		return RunRecord{}, fmt.Errorf("unsupported run mode %q", request.Mode)
	}

	seeds := normalizeSeeds(request.Seeds)
	if len(seeds) == 0 {
		return RunRecord{}, errors.New("at least one seed is required")
	}

	now := s.now()
	runID := app.BuildRunID(now)

	if _, err := s.pipelineFactory(runID); err != nil {
		return RunRecord{}, fmt.Errorf("build pipeline: %w", err)
	}

	run := RunRecord{
		ID:                runID,
		OwnerUID:          strings.TrimSpace(user.UID),
		OwnerEmail:        strings.TrimSpace(strings.ToLower(user.Email)),
		Mode:              mode,
		Status:            RunStatusQueued,
		CurrentWave:       0,
		SeedCount:         len(seeds),
		CreatedAt:         now,
		UpdatedAt:         now,
		PendingPivotCount: 0,
	}

	pCtx := &models.PipelineContext{Seeds: append([]models.Seed(nil), seeds...)}
	pCtx.SetCandidatePromotionConfidenceThreshold(
		candidatePromotionConfidenceThreshold(mode),
	)

	snapshot := Snapshot{
		Run:     run,
		Context: pCtx,
		Pivots:  make(map[string]PendingPivotState),
	}

	if err := s.checkpoints.Save(ctx, runID, snapshot); err != nil {
		return RunRecord{}, fmt.Errorf("save snapshot: %w", err)
	}

	if err := s.projection.UpsertRun(ctx, run); err != nil {
		return RunRecord{}, fmt.Errorf("project run: %w", err)
	}
	if err := s.projection.UpsertJudgeSummary(ctx, runID, buildProjectedJudgeSummary(snapshot.Context)); err != nil {
		return RunRecord{}, fmt.Errorf("project judge summary: %w", err)
	}

	for _, seed := range seeds {
		if err := s.projection.UpsertSeed(ctx, runID, SeedRecord{
			ID:          seed.ID,
			Source:      "submitted",
			SubmittedAt: now,
			Seed:        seed,
		}); err != nil {
			return RunRecord{}, fmt.Errorf("project seed %s: %w", seed.ID, err)
		}
	}

	if err := s.projection.AppendEvent(ctx, runID, EventRecord{
		ID:        models.NewID("event"),
		Kind:      "run_created",
		Message:   fmt.Sprintf("Created %s run with %d seed(s).", mode, len(seeds)),
		CreatedAt: now,
	}); err != nil {
		return RunRecord{}, fmt.Errorf("project create event: %w", err)
	}

	if s.dispatcher != nil {
		if err := s.dispatcher.Enqueue(ctx, runID); err != nil {
			return RunRecord{}, fmt.Errorf("enqueue run: %w", err)
		}
	}

	return run, nil
}

func (s *Service) DecidePivot(ctx context.Context, user AuthenticatedUser, runID string, pivotID string, decision PivotDecisionInput) (PivotRecord, error) {
	snapshot, err := s.checkpoints.Load(ctx, runID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return PivotRecord{}, newNotFoundError(fmt.Sprintf("run %q not found", runID))
		}
		return PivotRecord{}, fmt.Errorf("load snapshot: %w", err)
	}
	if !userOwnsRun(user, snapshot.Run) {
		return PivotRecord{}, newForbiddenError("run access is forbidden")
	}
	snapshot.ensureContext()
	snapshot.Context.RestoreSchedulerState(snapshot.SchedulerState)

	root, pivot, ok := findPivotByID(snapshot.Pivots, pivotID)
	if !ok {
		return PivotRecord{}, newNotFoundError(fmt.Sprintf("pivot %q not found", pivotID))
	}
	if pivot.Status != PivotDecisionPendingReview {
		return PivotRecord{}, fmt.Errorf("pivot %q is not pending review", pivotID)
	}

	now := s.now()
	switch decision {
	case PivotDecisionInputAccepted:
		pivot.Status = PivotDecisionAccepted
		snapshot.Context.EnqueueSeed(pivot.Candidate.Seed)
		if err := s.projection.UpsertSeed(ctx, runID, SeedRecord{
			ID:          pivot.Candidate.Seed.ID,
			Source:      "pivot",
			PivotID:     pivot.ID,
			SubmittedAt: now,
			Seed:        pivot.Candidate.Seed,
		}); err != nil {
			return PivotRecord{}, fmt.Errorf("project accepted seed: %w", err)
		}
	case PivotDecisionInputRejected:
		pivot.Status = PivotDecisionRejected
	default:
		return PivotRecord{}, fmt.Errorf("unsupported pivot decision %q", decision)
	}

	pivot.UpdatedAt = now
	pivot.DecisionAt = &now
	pivot.DecisionByUID = strings.TrimSpace(user.UID)
	pivot.DecisionByEmail = strings.TrimSpace(strings.ToLower(user.Email))
	snapshot.Pivots[root] = pivot
	snapshot.SchedulerState = snapshot.Context.SnapshotSchedulerState()
	updateRunCounters(&snapshot)
	if hasPendingReview(snapshot.Pivots) {
		snapshot.Run.Status = RunStatusAwaitingReview
	} else if snapshot.Run.Status == RunStatusAwaitingReview {
		snapshot.Run.Status = RunStatusQueued
	}
	snapshot.Run.UpdatedAt = now

	if err := s.checkpoints.Save(ctx, runID, snapshot); err != nil {
		return PivotRecord{}, fmt.Errorf("save snapshot: %w", err)
	}
	if err := s.projection.UpsertRun(ctx, snapshot.Run); err != nil {
		return PivotRecord{}, fmt.Errorf("project run: %w", err)
	}
	pivotRecord := buildPivotRecord(pivot)
	if err := s.projection.UpsertPivot(ctx, runID, pivotRecord); err != nil {
		return PivotRecord{}, fmt.Errorf("project pivot: %w", err)
	}
	if err := s.projection.AppendEvent(ctx, runID, EventRecord{
		ID:        models.NewID("event"),
		Kind:      "pivot_decision",
		Message:   fmt.Sprintf("Pivot %s was %s by %s.", pivot.Root(), pivot.Status, user.Email),
		CreatedAt: now,
		Metadata: map[string]interface{}{
			"pivot_id": pivot.ID,
			"root":     pivot.Root(),
			"status":   pivot.Status,
		},
	}); err != nil {
		return PivotRecord{}, fmt.Errorf("project pivot event: %w", err)
	}

	if !hasPendingReview(snapshot.Pivots) && s.dispatcher != nil {
		if err := s.dispatcher.Enqueue(ctx, runID); err != nil {
			return PivotRecord{}, fmt.Errorf("enqueue resumed run: %w", err)
		}
	}

	return pivotRecord, nil
}

func userOwnsRun(user AuthenticatedUser, run RunRecord) bool {
	ownerUID := strings.TrimSpace(run.OwnerUID)
	userUID := strings.TrimSpace(user.UID)
	if ownerUID != "" && userUID != "" {
		return ownerUID == userUID
	}

	return normalizeEmail(run.OwnerEmail) != "" && normalizeEmail(run.OwnerEmail) == normalizeEmail(user.Email)
}

func normalizeEmail(email string) string {
	return strings.TrimSpace(strings.ToLower(email))
}

func (s *Service) ProcessRun(ctx context.Context, runID string) (err error) {
	var snapshot Snapshot
	snapshotLoaded := false

	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("panic while processing run %s: %v", runID, recovered)
			log.Printf("panic while processing run %s: %v\n%s", runID, recovered, debug.Stack())
			if !snapshotLoaded {
				return
			}

			failedAt := s.now()
			snapshot.Run.Status = RunStatusFailed
			snapshot.Run.LastError = err.Error()
			snapshot.Run.UpdatedAt = failedAt
			snapshot.SchedulerState = snapshot.Context.SnapshotSchedulerState()
			updateRunCounters(&snapshot)

			if saveErr := s.checkpoints.Save(ctx, runID, snapshot); saveErr != nil {
				log.Printf("save panic snapshot for %s: %v", runID, saveErr)
			}
			if projectErr := s.projectSnapshot(ctx, &snapshot); projectErr != nil {
				log.Printf("project panic snapshot for %s: %v", runID, projectErr)
			}
		}
	}()

	snapshot, err = s.checkpoints.Load(ctx, runID)
	if err != nil {
		return fmt.Errorf("load snapshot: %w", err)
	}
	snapshotLoaded = true
	snapshot.ensureContext()
	snapshot.Context.RestoreSchedulerState(snapshot.SchedulerState)
	snapshot.Context.SetCandidatePromotionConfidenceThreshold(
		candidatePromotionConfidenceThreshold(snapshot.Run.Mode),
	)
	if snapshot.Pivots == nil {
		snapshot.Pivots = make(map[string]PendingPivotState)
	}

	pipeline, err := s.pipelineFactory(runID)
	if err != nil {
		return fmt.Errorf("build pipeline: %w", err)
	}
	localDownloads := buildDownloads(pipeline.Outputs())

	broker := newPivotBroker(snapshot.Run.Mode, snapshot.Pivots, s.now)
	snapshot.Context.SetCandidatePromotionHandler(broker)

	now := s.now()
	snapshot.Run.Status = RunStatusRunning
	snapshot.Run.UpdatedAt = now
	if snapshot.Run.StartedAt == nil {
		snapshot.Run.StartedAt = &now
	}
	updateRunCounters(&snapshot)

	if err := s.projection.UpsertRun(ctx, snapshot.Run); err != nil {
		return fmt.Errorf("project running state: %w", err)
	}
	if err := s.projection.AppendEvent(ctx, runID, EventRecord{
		ID:        models.NewID("event"),
		Kind:      "run_started",
		Message:   fmt.Sprintf("Run %s started in %s mode.", runID, snapshot.Run.Mode),
		CreatedAt: now,
	}); err != nil {
		return fmt.Errorf("project start event: %w", err)
	}

	// Build the mutation listener only after the live run document reflects the
	// running transition so streamed asset updates cannot re-project a stale
	// queued status.
	listener := newProjectionMutationListener(
		ctx,
		snapshot.Run,
		snapshot.Context,
		s.projection,
		s.now,
	)
	snapshot.Context.SetMutationListener(listener)

	_, err = pipeline.Resume(ctx, snapshot.Context, &snapshot.Progress, dag.ResumeCallbacks{
		AfterCheckpoint: func(ctx context.Context, checkpoint dag.Checkpoint, progress dag.RunProgress, pCtx *models.PipelineContext) (bool, error) {
			snapshot.Progress = progress
			snapshot.SchedulerState = pCtx.SnapshotSchedulerState()
			snapshot.Pivots = broker.Snapshot()
			if err := hydratePivotsFromJudges(&snapshot, pCtx, s.now()); err != nil {
				return false, err
			}

			updateRunCounters(&snapshot)
			snapshot.Run.CurrentWave = progress.Wave
			snapshot.Run.UpdatedAt = s.now()
			if hasPendingReview(snapshot.Pivots) {
				snapshot.Run.Status = RunStatusAwaitingReview
			} else {
				snapshot.Run.Status = RunStatusRunning
			}

			if err := s.checkpoints.Save(ctx, runID, snapshot); err != nil {
				return false, fmt.Errorf("save checkpoint: %w", err)
			}
			if err := s.projectSnapshot(ctx, &snapshot); err != nil {
				return false, err
			}
			if err := s.projection.AppendEvent(ctx, runID, EventRecord{
				ID:        models.NewID("event"),
				Kind:      "checkpoint",
				Message:   fmt.Sprintf("Reached %s at wave %d.", checkpoint, progress.Wave),
				CreatedAt: s.now(),
				Metadata: map[string]interface{}{
					"checkpoint": checkpoint,
					"wave":       progress.Wave,
				},
			}); err != nil {
				return false, fmt.Errorf("project checkpoint event: %w", err)
			}
			return hasPendingReview(snapshot.Pivots), nil
		},
	})

	if listener.Err() != nil {
		return listener.Err()
	}

	switch {
	case errors.Is(err, dag.ErrExecutionPaused):
		return nil
	case err != nil:
		failedAt := s.now()
		snapshot.Run.Status = RunStatusFailed
		snapshot.Run.LastError = err.Error()
		snapshot.Run.UpdatedAt = failedAt
		updateRunCounters(&snapshot)
		snapshot.SchedulerState = snapshot.Context.SnapshotSchedulerState()
		snapshot.Pivots = broker.Snapshot()
		if saveErr := s.checkpoints.Save(ctx, runID, snapshot); saveErr != nil {
			return fmt.Errorf("save failed snapshot: %v (original error: %w)", saveErr, err)
		}
		if projectErr := s.projectSnapshot(ctx, &snapshot); projectErr != nil {
			return fmt.Errorf("project failed run: %v (original error: %w)", projectErr, err)
		}
		return err
	default:
		publishedDownloads, publishErr := s.artifacts.Publish(ctx, runID, localDownloads)
		if publishErr != nil {
			failedAt := s.now()
			snapshot.Run.Status = RunStatusFailed
			snapshot.Run.LastError = fmt.Sprintf("publish artifacts: %v", publishErr)
			snapshot.Run.UpdatedAt = failedAt
			snapshot.SchedulerState = snapshot.Context.SnapshotSchedulerState()
			snapshot.Pivots = broker.Snapshot()
			updateRunCounters(&snapshot)
			if saveErr := s.checkpoints.Save(ctx, runID, snapshot); saveErr != nil {
				return fmt.Errorf("save artifact failure snapshot: %v (artifact error: %w)", saveErr, publishErr)
			}
			if projectErr := s.projectSnapshot(ctx, &snapshot); projectErr != nil {
				return fmt.Errorf("project artifact failure: %v (artifact error: %w)", projectErr, publishErr)
			}
			if eventErr := s.projection.AppendEvent(ctx, runID, EventRecord{
				ID:        models.NewID("event"),
				Kind:      "artifact_publish_failed",
				Message:   fmt.Sprintf("Run %s failed while publishing result artifacts.", runID),
				CreatedAt: failedAt,
				Metadata: map[string]interface{}{
					"error": publishErr.Error(),
				},
			}); eventErr != nil {
				return fmt.Errorf("project artifact failure event: %v (artifact error: %w)", eventErr, publishErr)
			}
			return fmt.Errorf("publish artifacts: %w", publishErr)
		}

		completedAt := s.now()
		snapshot.Run.Downloads = publishedDownloads
		snapshot.Run.Status = RunStatusCompleted
		snapshot.Run.UpdatedAt = completedAt
		snapshot.Run.CompletedAt = &completedAt
		snapshot.Run.LastError = ""
		snapshot.SchedulerState = snapshot.Context.SnapshotSchedulerState()
		snapshot.Pivots = broker.Snapshot()
		if err := hydratePivotsFromJudges(&snapshot, snapshot.Context, s.now()); err != nil {
			return err
		}
		updateRunCounters(&snapshot)
		if err := s.checkpoints.Save(ctx, runID, snapshot); err != nil {
			return fmt.Errorf("save completed snapshot: %w", err)
		}
		if err := s.projectSnapshot(ctx, &snapshot); err != nil {
			return err
		}
		if err := s.projection.AppendEvent(ctx, runID, EventRecord{
			ID:        models.NewID("event"),
			Kind:      "artifacts_published",
			Message:   fmt.Sprintf("Published result artifacts for run %s.", runID),
			CreatedAt: completedAt,
			Metadata: map[string]interface{}{
				"json": snapshot.Run.Downloads.JSON,
				"csv":  snapshot.Run.Downloads.CSV,
				"xlsx": snapshot.Run.Downloads.XLSX,
			},
		}); err != nil {
			return fmt.Errorf("project artifact publish event: %w", err)
		}
		return s.projection.AppendEvent(ctx, runID, EventRecord{
			ID:        models.NewID("event"),
			Kind:      "run_completed",
			Message:   fmt.Sprintf("Run %s completed with %d asset(s).", runID, snapshot.Run.AssetCount),
			CreatedAt: completedAt,
		})
	}
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

func normalizeSeeds(seeds []models.Seed) []models.Seed {
	out := make([]models.Seed, 0, len(seeds))
	for _, seed := range seeds {
		if len(seed.Domains) == 0 && strings.TrimSpace(seed.CompanyName) == "" {
			continue
		}
		if strings.TrimSpace(seed.ID) == "" {
			seed.ID = models.NewID("seed")
		}
		out = append(out, seed)
	}
	return out
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

func countPendingPivots(pivots map[string]PendingPivotState) int {
	count := 0
	for _, pivot := range pivots {
		if pivot.Status == PivotDecisionPendingReview {
			count++
		}
	}
	return count
}

func hasPendingReview(pivots map[string]PendingPivotState) bool {
	return countPendingPivots(pivots) > 0
}

func findPivotByID(pivots map[string]PendingPivotState, pivotID string) (string, PendingPivotState, bool) {
	for root, pivot := range pivots {
		if pivot.ID == pivotID {
			return root, pivot, true
		}
	}
	return "", PendingPivotState{}, false
}

func sortedPivotStates(pivots map[string]PendingPivotState) []PendingPivotState {
	values := make([]PendingPivotState, 0, len(pivots))
	for _, pivot := range pivots {
		values = append(values, pivot)
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].CreatedAt.Equal(values[j].CreatedAt) {
			return values[i].ID < values[j].ID
		}
		return values[i].CreatedAt.Before(values[j].CreatedAt)
	})
	return values
}

type pivotBroker struct {
	mode   RunMode
	now    func() time.Time
	mu     sync.Mutex
	pivots map[string]PendingPivotState
}

func newPivotBroker(mode RunMode, existing map[string]PendingPivotState, now func() time.Time) *pivotBroker {
	cloned := make(map[string]PendingPivotState, len(existing))
	for key, value := range existing {
		cloned[key] = value
	}
	return &pivotBroker{
		mode:   mode,
		now:    now,
		pivots: cloned,
	}
}

func (b *pivotBroker) HandleCandidatePromotion(candidate models.CandidatePromotionRequest) models.CandidatePromotionDecision {
	b.mu.Lock()
	defer b.mu.Unlock()

	root := promotionRoot(candidate)
	now := b.now()

	if existing, ok := b.pivots[root]; ok {
		existing.Candidate = candidate
		existing.UpdatedAt = now
		b.pivots[root] = existing
		switch existing.Status {
		case PivotDecisionAccepted, PivotDecisionAutoAccepted:
			return models.CandidatePromotionAccepted
		case PivotDecisionRejected, PivotDecisionAutoRejected:
			return models.CandidatePromotionRejected
		default:
			return models.CandidatePromotionPendingReview
		}
	}

	state := PendingPivotState{
		ID:        models.NewID("pivot"),
		Candidate: candidate,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if b.mode == RunModeManual {
		state.Status = PivotDecisionPendingReview
		b.pivots[root] = state
		return models.CandidatePromotionPendingReview
	}

	state.Status = PivotDecisionAutoAccepted
	b.pivots[root] = state
	return models.CandidatePromotionAccepted
}

func (b *pivotBroker) Snapshot() map[string]PendingPivotState {
	b.mu.Lock()
	defer b.mu.Unlock()

	out := make(map[string]PendingPivotState, len(b.pivots))
	for key, value := range b.pivots {
		out[key] = value
	}
	return out
}

func promotionRoot(candidate models.CandidatePromotionRequest) string {
	if root := discovery.RegistrableDomain(candidate.Key); root != "" {
		return root
	}
	for _, domain := range candidate.Seed.Domains {
		if root := discovery.RegistrableDomain(domain); root != "" {
			return root
		}
	}
	return strings.TrimSpace(strings.ToLower(candidate.Seed.ID))
}

func candidatePromotionConfidenceThreshold(mode RunMode) float64 {
	if mode == RunModeManual {
		return ownership.ManualReviewConfidenceThreshold
	}
	return ownership.DefaultHighConfidenceThreshold
}

const manualAutoRejectionConfidenceThreshold = 0.80

func autoRejectedDiscardStatus(mode RunMode, confidence float64) PivotDecisionStatus {
	if mode == RunModeManual && !ownership.IsConfidenceAtLeast(confidence, manualAutoRejectionConfidenceThreshold) {
		return PivotDecisionPendingReview
	}
	return PivotDecisionAutoRejected
}

func hydratePivotsFromJudges(snapshot *Snapshot, pCtx *models.PipelineContext, now time.Time) error {
	if snapshot.Pivots == nil {
		snapshot.Pivots = make(map[string]PendingPivotState)
	}

	for _, evaluation := range pCtx.JudgeEvaluations {
		for _, outcome := range evaluation.Outcomes {
			root := discovery.RegistrableDomain(outcome.Root)
			if root == "" {
				continue
			}

			pivot, exists := snapshot.Pivots[root]
			if !exists {
				if outcome.Collect {
					continue
				}
				pivot = PendingPivotState{
					ID: models.NewID("pivot"),
					Candidate: models.CandidatePromotionRequest{
						Key: root,
						Seed: models.Seed{
							ID:          root,
							CompanyName: root,
							Domains:     []string{root},
						},
						Confidence: outcome.Confidence,
						Reasoned:   true,
					},
					Status:    autoRejectedDiscardStatus(snapshot.Run.Mode, outcome.Confidence),
					CreatedAt: now,
				}
			}

			pivot.UpdatedAt = now
			pivot.Collector = evaluation.Collector
			pivot.Scenario = evaluation.Scenario
			pivot.SeedID = evaluation.SeedID
			pivot.SeedLabel = evaluation.SeedLabel
			pivot.SeedDomains = append([]string(nil), evaluation.SeedDomains...)
			pivot.Kind = outcome.Kind
			pivot.Reason = outcome.Reason
			pivot.Support = append([]string(nil), outcome.Support...)
			snapshot.Pivots[root] = pivot
		}
	}

	return nil
}

func buildPivotRecord(pivot PendingPivotState) PivotRecord {
	return PivotRecord{
		ID:                   pivot.ID,
		Root:                 pivot.Root(),
		Status:               pivot.Status,
		Collector:            pivot.Collector,
		Scenario:             pivot.Scenario,
		SeedID:               pivot.SeedID,
		SeedLabel:            pivot.SeedLabel,
		SeedDomains:          append([]string(nil), pivot.SeedDomains...),
		RecommendationKind:   pivot.Kind,
		RecommendationReason: pivot.Reason,
		RecommendationScore:  pivot.Candidate.Confidence,
		RecommendationNotes:  append([]string(nil), pivot.Support...),
		Candidate:            pivot.Candidate.Seed,
		Evidence:             append([]models.SeedEvidence(nil), pivot.Candidate.Evidence...),
		CreatedAt:            pivot.CreatedAt,
		UpdatedAt:            pivot.UpdatedAt,
		DecisionAt:           pivot.DecisionAt,
		DecisionByUID:        pivot.DecisionByUID,
		DecisionByEmail:      pivot.DecisionByEmail,
	}
}

func (p PendingPivotState) Root() string {
	root := promotionRoot(p.Candidate)
	if root != "" {
		return root
	}
	return strings.TrimSpace(strings.ToLower(p.SeedLabel))
}
