package runservice

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"asset-discovery/internal/app"
	"asset-discovery/internal/models"
)

// CreateRun validates a new run request, persists the initial snapshot, and
// projects the submitted seeds into the live read model.
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
		OwnerEmail:        normalizeEmail(user.Email),
		Mode:              mode,
		Status:            RunStatusQueued,
		CurrentWave:       0,
		SeedCount:         len(seeds),
		CreatedAt:         now,
		UpdatedAt:         now,
		PendingPivotCount: 0,
	}

	pCtx := &models.PipelineContext{Seeds: append([]models.Seed(nil), seeds...)}
	pCtx.SetCandidatePromotionConfidenceThreshold(candidatePromotionConfidenceThreshold(mode))

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
