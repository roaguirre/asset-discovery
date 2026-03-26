package runservice

import (
	"time"

	"asset-discovery/internal/dag"
	export "asset-discovery/internal/export"
	"asset-discovery/internal/models"
)

type RunMode string

const (
	RunModeAutonomous RunMode = "autonomous"
	RunModeManual     RunMode = "manual"
)

type RunStatus string

const (
	RunStatusQueued         RunStatus = "queued"
	RunStatusRunning        RunStatus = "running"
	RunStatusAwaitingReview RunStatus = "awaiting_review"
	RunStatusCompleted      RunStatus = "completed"
	RunStatusFailed         RunStatus = "failed"
)

type PivotDecisionStatus string

const (
	PivotDecisionPendingReview PivotDecisionStatus = "pending_review"
	PivotDecisionAccepted      PivotDecisionStatus = "accepted"
	PivotDecisionRejected      PivotDecisionStatus = "rejected"
	PivotDecisionAutoAccepted  PivotDecisionStatus = "auto_accepted"
	PivotDecisionAutoRejected  PivotDecisionStatus = "auto_rejected"
)

type PivotDecisionInput string

const (
	PivotDecisionInputAccepted PivotDecisionInput = "accepted"
	PivotDecisionInputRejected PivotDecisionInput = "rejected"
)

type AuthenticatedUser struct {
	UID           string `json:"uid"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Name          string `json:"name,omitempty"`
}

type CreateRunRequest struct {
	Mode  RunMode       `json:"mode"`
	Seeds []models.Seed `json:"seeds"`
}

type RunRecord struct {
	ID                   string           `json:"id" firestore:"id"`
	OwnerUID             string           `json:"owner_uid" firestore:"owner_uid"`
	OwnerEmail           string           `json:"owner_email" firestore:"owner_email"`
	Mode                 RunMode          `json:"mode" firestore:"mode"`
	Status               RunStatus        `json:"status" firestore:"status"`
	CurrentWave          int              `json:"current_wave" firestore:"current_wave"`
	SeedCount            int              `json:"seed_count" firestore:"seed_count"`
	EnumerationCount     int              `json:"enumeration_count" firestore:"enumeration_count"`
	AssetCount           int              `json:"asset_count" firestore:"asset_count"`
	PendingPivotCount    int              `json:"pending_pivot_count" firestore:"pending_pivot_count"`
	JudgeEvaluationCount int              `json:"judge_evaluation_count" firestore:"judge_evaluation_count"`
	JudgeAcceptedCount   int              `json:"judge_accepted_count" firestore:"judge_accepted_count"`
	JudgeDiscardedCount  int              `json:"judge_discarded_count" firestore:"judge_discarded_count"`
	LastError            string           `json:"last_error,omitempty" firestore:"last_error,omitempty"`
	Downloads            export.Downloads `json:"downloads,omitempty" firestore:"downloads,omitempty"`
	CreatedAt            time.Time        `json:"created_at" firestore:"created_at"`
	UpdatedAt            time.Time        `json:"updated_at" firestore:"updated_at"`
	StartedAt            *time.Time       `json:"started_at,omitempty" firestore:"started_at,omitempty"`
	CompletedAt          *time.Time       `json:"completed_at,omitempty" firestore:"completed_at,omitempty"`
}

type SeedRecord struct {
	ID          string      `json:"id" firestore:"id"`
	Source      string      `json:"source" firestore:"source"`
	PivotID     string      `json:"pivot_id,omitempty" firestore:"pivot_id,omitempty"`
	SubmittedAt time.Time   `json:"submitted_at" firestore:"submitted_at"`
	Seed        models.Seed `json:"seed" firestore:"seed"`
}

type PivotRecord struct {
	ID                   string                `json:"id" firestore:"id"`
	Root                 string                `json:"root" firestore:"root"`
	Status               PivotDecisionStatus   `json:"status" firestore:"status"`
	Collector            string                `json:"collector,omitempty" firestore:"collector,omitempty"`
	Scenario             string                `json:"scenario,omitempty" firestore:"scenario,omitempty"`
	SeedID               string                `json:"seed_id,omitempty" firestore:"seed_id,omitempty"`
	SeedLabel            string                `json:"seed_label,omitempty" firestore:"seed_label,omitempty"`
	SeedDomains          []string              `json:"seed_domains,omitempty" firestore:"seed_domains,omitempty"`
	RecommendationKind   string                `json:"recommendation_kind,omitempty" firestore:"recommendation_kind,omitempty"`
	RecommendationReason string                `json:"recommendation_reason,omitempty" firestore:"recommendation_reason,omitempty"`
	RecommendationScore  float64               `json:"recommendation_score,omitempty" firestore:"recommendation_score,omitempty"`
	RecommendationNotes  []string              `json:"recommendation_notes,omitempty" firestore:"recommendation_notes,omitempty"`
	Candidate            models.Seed           `json:"candidate" firestore:"candidate"`
	Evidence             []models.SeedEvidence `json:"evidence,omitempty" firestore:"evidence,omitempty"`
	CreatedAt            time.Time             `json:"created_at" firestore:"created_at"`
	UpdatedAt            time.Time             `json:"updated_at" firestore:"updated_at"`
	DecisionAt           *time.Time            `json:"decision_at,omitempty" firestore:"decision_at,omitempty"`
	DecisionByUID        string                `json:"decision_by_uid,omitempty" firestore:"decision_by_uid,omitempty"`
	DecisionByEmail      string                `json:"decision_by_email,omitempty" firestore:"decision_by_email,omitempty"`
}

type EventRecord struct {
	ID        string                 `json:"id" firestore:"id"`
	Kind      string                 `json:"kind" firestore:"kind"`
	Message   string                 `json:"message" firestore:"message"`
	Metadata  map[string]interface{} `json:"metadata,omitempty" firestore:"metadata,omitempty"`
	CreatedAt time.Time              `json:"created_at" firestore:"created_at"`
}

type PendingPivotState struct {
	ID              string                           `json:"id"`
	Candidate       models.CandidatePromotionRequest `json:"candidate"`
	Status          PivotDecisionStatus              `json:"status"`
	Collector       string                           `json:"collector,omitempty"`
	Scenario        string                           `json:"scenario,omitempty"`
	SeedID          string                           `json:"seed_id,omitempty"`
	SeedLabel       string                           `json:"seed_label,omitempty"`
	SeedDomains     []string                         `json:"seed_domains,omitempty"`
	Kind            string                           `json:"kind,omitempty"`
	Reason          string                           `json:"reason,omitempty"`
	Support         []string                         `json:"support,omitempty"`
	CreatedAt       time.Time                        `json:"created_at"`
	UpdatedAt       time.Time                        `json:"updated_at"`
	DecisionAt      *time.Time                       `json:"decision_at,omitempty"`
	DecisionByUID   string                           `json:"decision_by_uid,omitempty"`
	DecisionByEmail string                           `json:"decision_by_email,omitempty"`
}

// Snapshot captures the persisted run state needed to resume pipeline
// execution and rebuild live projections.
type Snapshot struct {
	Run            RunRecord                    `json:"run"`
	Context        *models.PipelineContext      `json:"context"`
	SchedulerState models.SchedulerState        `json:"scheduler_state"`
	Progress       dag.RunProgress              `json:"progress"`
	Pivots         map[string]PendingPivotState `json:"pivots,omitempty"`
}

func (s *Snapshot) ensureContext() {
	if s.Context == nil {
		s.Context = &models.PipelineContext{}
	}
}
