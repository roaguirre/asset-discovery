package runservice

import (
	"context"
	"testing"
	"time"
)

func TestWorker_RunClaimsAndReleasesLease(t *testing.T) {
	t.Parallel()

	artifactStore := &capturingArtifactStore{}
	service, _, projection, run := newTestService(t, RunModeAutonomous, artifactStore)
	now := time.Date(2026, time.March, 25, 12, 0, 0, 0, time.UTC)

	worker, err := NewWorker(service, projection, WorkerConfig{
		LeaseTTL:          5 * time.Minute,
		HeartbeatInterval: time.Minute,
		Now: func() time.Time {
			return now
		},
		LeaseID: func() string {
			return "lease-test"
		},
	})
	if err != nil {
		t.Fatalf("NewWorker() error = %v", err)
	}

	if err := worker.Run(context.Background(), run.ID); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	projected := projection.Runs[run.ID]
	if projected.Status != RunStatusCompleted {
		t.Fatalf("expected completed status, got %s", projected.Status)
	}
	if projected.ExecutionLeaseID != "" {
		t.Fatalf("expected lease to be released, got %q", projected.ExecutionLeaseID)
	}
	if projected.ExecutionHeartbeatAt != nil || projected.ExecutionLeaseUntil != nil {
		t.Fatalf("expected lease timestamps to be cleared, got %+v", projected)
	}
}

func TestWorker_RunSkipsActiveLease(t *testing.T) {
	t.Parallel()

	service, _, projection, run := newTestService(t, RunModeAutonomous, &capturingArtifactStore{})
	now := time.Date(2026, time.March, 25, 12, 0, 0, 0, time.UTC)

	if _, claimed, err := projection.ClaimRunExecution(context.Background(), run.ID, "other-lease", now, time.Hour); err != nil {
		t.Fatalf("ClaimRunExecution() error = %v", err)
	} else if !claimed {
		t.Fatal("expected initial claim to succeed")
	}

	worker, err := NewWorker(service, projection, WorkerConfig{
		LeaseTTL:          5 * time.Minute,
		HeartbeatInterval: time.Minute,
		Now: func() time.Time {
			return now
		},
		LeaseID: func() string {
			return "lease-test"
		},
	})
	if err != nil {
		t.Fatalf("NewWorker() error = %v", err)
	}

	if err := worker.Run(context.Background(), run.ID); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	projected := projection.Runs[run.ID]
	if projected.Status != RunStatusQueued {
		t.Fatalf("expected queued status, got %s", projected.Status)
	}
	if projected.ExecutionLeaseID != "other-lease" {
		t.Fatalf("expected existing lease to be preserved, got %q", projected.ExecutionLeaseID)
	}
}
