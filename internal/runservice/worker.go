package runservice

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"asset-discovery/internal/models"
)

// WorkerConfig configures durable execution for a single live run.
type WorkerConfig struct {
	LeaseTTL          time.Duration
	HeartbeatInterval time.Duration
	Now               func() time.Time
	LeaseID           func() string
}

// Worker claims a live run, keeps the execution lease fresh, and processes the
// run until completion or pause.
type Worker struct {
	service           *Service
	executions        ExecutionStore
	leaseTTL          time.Duration
	heartbeatInterval time.Duration
	now               func() time.Time
	leaseID           func() string
}

// NewWorker constructs a worker that can safely process one queued run at a
// time while refreshing the run's execution lease.
func NewWorker(service *Service, executions ExecutionStore, cfg WorkerConfig) (*Worker, error) {
	if service == nil {
		return nil, errors.New("service is required")
	}
	if executions == nil {
		return nil, errors.New("execution store is required")
	}

	leaseTTL := cfg.LeaseTTL
	if leaseTTL <= 0 {
		leaseTTL = 15 * time.Minute
	}

	heartbeatInterval := cfg.HeartbeatInterval
	if heartbeatInterval <= 0 {
		heartbeatInterval = time.Minute
	}
	if heartbeatInterval >= leaseTTL {
		return nil, errors.New("heartbeat interval must be shorter than the lease TTL")
	}

	nowFn := cfg.Now
	if nowFn == nil {
		nowFn = time.Now
	}

	leaseIDFn := cfg.LeaseID
	if leaseIDFn == nil {
		leaseIDFn = func() string {
			return models.NewID("lease")
		}
	}

	return &Worker{
		service:           service,
		executions:        executions,
		leaseTTL:          leaseTTL,
		heartbeatInterval: heartbeatInterval,
		now:               nowFn,
		leaseID:           leaseIDFn,
	}, nil
}

// Run claims the requested run and processes it. If another worker already owns
// an active lease for the run, Run returns without doing duplicate work.
func (w *Worker) Run(ctx context.Context, runID string) error {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return errors.New("run ID is required")
	}

	leaseID := w.leaseID()
	_, claimed, err := w.executions.ClaimRunExecution(ctx, runID, leaseID, w.now(), w.leaseTTL)
	if err != nil {
		return fmt.Errorf("claim run execution: %w", err)
	}
	if !claimed {
		return nil
	}

	leaseCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	heartbeatErrs := make(chan error, 1)
	go w.heartbeatLoop(leaseCtx, runID, leaseID, heartbeatErrs, cancel)

	processErr := w.service.ProcessRun(leaseCtx, runID)
	cancel()

	heartbeatErr := <-heartbeatErrs

	releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer releaseCancel()
	releaseErr := w.executions.ReleaseRunExecution(releaseCtx, runID, leaseID, w.now())

	return errors.Join(processErr, heartbeatErr, releaseErr)
}

func (w *Worker) heartbeatLoop(
	ctx context.Context,
	runID string,
	leaseID string,
	errs chan<- error,
	cancel context.CancelFunc,
) {
	ticker := time.NewTicker(w.heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			errs <- nil
			return
		case <-ticker.C:
			if err := w.executions.HeartbeatRunExecution(ctx, runID, leaseID, w.now(), w.leaseTTL); err != nil {
				cancel()
				errs <- fmt.Errorf("heartbeat run execution: %w", err)
				return
			}
		}
	}
}

func preserveExecutionLease(existing RunRecord, incoming RunRecord) RunRecord {
	if incoming.ExecutionLeaseID != "" {
		return incoming
	}
	if existing.ExecutionLeaseID == "" {
		return incoming
	}

	incoming.ExecutionLeaseID = existing.ExecutionLeaseID
	incoming.ExecutionHeartbeatAt = existing.ExecutionHeartbeatAt
	incoming.ExecutionLeaseUntil = existing.ExecutionLeaseUntil
	return incoming
}

func leaseIsActive(run RunRecord, now time.Time) bool {
	return run.ExecutionLeaseID != "" &&
		run.ExecutionLeaseUntil != nil &&
		run.ExecutionLeaseUntil.After(now)
}
