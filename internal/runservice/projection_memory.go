package runservice

import (
	"context"
	"fmt"
	"sync"
	"time"

	"asset-discovery/internal/tracing/lineage"
)

type MemoryProjectionStore struct {
	mu             sync.Mutex
	Runs           map[string]RunRecord
	Seeds          map[string]map[string]SeedRecord
	Pivots         map[string]map[string]PivotRecord
	JudgeSummaries map[string]lineage.JudgeSummary
	Events         map[string][]EventRecord
	Assets         map[string]map[string]AssetRow
	Traces         map[string]map[string]lineage.Trace
}

func NewMemoryProjectionStore() *MemoryProjectionStore {
	return &MemoryProjectionStore{
		Runs:           make(map[string]RunRecord),
		Seeds:          make(map[string]map[string]SeedRecord),
		Pivots:         make(map[string]map[string]PivotRecord),
		JudgeSummaries: make(map[string]lineage.JudgeSummary),
		Events:         make(map[string][]EventRecord),
		Assets:         make(map[string]map[string]AssetRow),
		Traces:         make(map[string]map[string]lineage.Trace),
	}
}

func (s *MemoryProjectionStore) UpsertRun(_ context.Context, run RunRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if existing, ok := s.Runs[run.ID]; ok {
		run = preserveExecutionLease(existing, run)
	}
	s.Runs[run.ID] = run
	return nil
}

func (s *MemoryProjectionStore) UpsertSeed(_ context.Context, runID string, seed SeedRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.Seeds[runID] == nil {
		s.Seeds[runID] = make(map[string]SeedRecord)
	}
	s.Seeds[runID][seed.ID] = seed
	return nil
}

func (s *MemoryProjectionStore) UpsertPivot(_ context.Context, runID string, pivot PivotRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.Pivots[runID] == nil {
		s.Pivots[runID] = make(map[string]PivotRecord)
	}
	s.Pivots[runID][pivot.ID] = pivot
	return nil
}

func (s *MemoryProjectionStore) UpsertJudgeSummary(
	_ context.Context,
	runID string,
	summary lineage.JudgeSummary,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.JudgeSummaries[runID] = summary
	return nil
}

func (s *MemoryProjectionStore) AppendEvent(_ context.Context, runID string, event EventRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.Events[runID] = append(s.Events[runID], event)
	return nil
}

func (s *MemoryProjectionStore) UpsertAsset(_ context.Context, runID string, row AssetRow) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.Assets[runID] == nil {
		s.Assets[runID] = make(map[string]AssetRow)
	}
	s.Assets[runID][row.AssetID] = row
	return nil
}

func (s *MemoryProjectionStore) SyncTraces(_ context.Context, runID string, traces []lineage.Trace) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	projected := make(map[string]lineage.Trace, len(traces))
	for _, trace := range traces {
		projected[trace.AssetID] = trace
	}
	s.Traces[runID] = projected
	return nil
}

func (s *MemoryProjectionStore) ClaimRunExecution(
	_ context.Context,
	runID string,
	leaseID string,
	now time.Time,
	ttl time.Duration,
) (RunRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	run, ok := s.Runs[runID]
	if !ok {
		return RunRecord{}, false, fmt.Errorf("run %q not found", runID)
	}
	if leaseIsActive(run, now) && run.ExecutionLeaseID != leaseID {
		return run, false, nil
	}

	heartbeatAt := now
	leaseUntil := now.Add(ttl)
	run.ExecutionLeaseID = leaseID
	run.ExecutionHeartbeatAt = &heartbeatAt
	run.ExecutionLeaseUntil = &leaseUntil
	s.Runs[runID] = run
	return run, true, nil
}

func (s *MemoryProjectionStore) HeartbeatRunExecution(
	_ context.Context,
	runID string,
	leaseID string,
	now time.Time,
	ttl time.Duration,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	run, ok := s.Runs[runID]
	if !ok {
		return fmt.Errorf("run %q not found", runID)
	}
	if run.ExecutionLeaseID != leaseID {
		return fmt.Errorf("run %q lease mismatch", runID)
	}

	heartbeatAt := now
	leaseUntil := now.Add(ttl)
	run.ExecutionHeartbeatAt = &heartbeatAt
	run.ExecutionLeaseUntil = &leaseUntil
	s.Runs[runID] = run
	return nil
}

func (s *MemoryProjectionStore) ReleaseRunExecution(
	_ context.Context,
	runID string,
	leaseID string,
	_ time.Time,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	run, ok := s.Runs[runID]
	if !ok {
		return fmt.Errorf("run %q not found", runID)
	}
	if run.ExecutionLeaseID != "" && run.ExecutionLeaseID != leaseID {
		return fmt.Errorf("run %q lease mismatch", runID)
	}

	run.ExecutionLeaseID = ""
	run.ExecutionHeartbeatAt = nil
	run.ExecutionLeaseUntil = nil
	s.Runs[runID] = run
	return nil
}
