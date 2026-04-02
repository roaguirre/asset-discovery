package runservice

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"asset-discovery/internal/discovery"
	"asset-discovery/internal/models"
	"asset-discovery/internal/ownership"
)

// DecidePivot records a manual review decision for a pending pivot and, when
// the final blocking review is cleared, re-enqueues the run for execution.
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
	pivot.DecisionByEmail = normalizeEmail(user.Email)
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

// Root returns the canonical registrable root used to group manual-review
// pivots for equivalent discovered domains.
func (p PendingPivotState) Root() string {
	root := promotionRoot(p.Candidate)
	if root != "" {
		return root
	}
	return strings.TrimSpace(strings.ToLower(p.SeedLabel))
}
