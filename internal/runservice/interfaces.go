package runservice

import (
	"context"
	"time"

	export "asset-discovery/internal/export"
	"asset-discovery/internal/tracing/lineage"
)

type CheckpointStore interface {
	Save(ctx context.Context, runID string, snapshot Snapshot) error
	Load(ctx context.Context, runID string) (Snapshot, error)
	Delete(ctx context.Context, runID string) error
}

type ProjectionStore interface {
	UpsertRun(ctx context.Context, run RunRecord) error
	UpsertSeed(ctx context.Context, runID string, seed SeedRecord) error
	UpsertPivot(ctx context.Context, runID string, pivot PivotRecord) error
	UpsertJudgeSummary(ctx context.Context, runID string, summary lineage.JudgeSummary) error
	AppendEvent(ctx context.Context, runID string, event EventRecord) error
	UpsertAsset(ctx context.Context, runID string, row AssetRow) error
	SyncTraces(ctx context.Context, runID string, traces []lineage.Trace) error
}

// ArtifactStore publishes the completed run exports that the live web client
// can later download.
type ArtifactStore interface {
	Publish(ctx context.Context, runID string, downloads export.Downloads) (export.Downloads, error)
}

// ExecutionStore coordinates durable worker ownership for a live run.
type ExecutionStore interface {
	ClaimRunExecution(ctx context.Context, runID string, leaseID string, now time.Time, ttl time.Duration) (RunRecord, bool, error)
	HeartbeatRunExecution(ctx context.Context, runID string, leaseID string, now time.Time, ttl time.Duration) error
	ReleaseRunExecution(ctx context.Context, runID string, leaseID string, now time.Time) error
}

type Dispatcher interface {
	Enqueue(ctx context.Context, runID string) error
}

type AuthVerifier interface {
	VerifyIDToken(ctx context.Context, token string) (AuthenticatedUser, error)
}
