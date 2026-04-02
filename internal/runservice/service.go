package runservice

import (
	"errors"
	"time"

	"asset-discovery/internal/app"
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

// NewService validates the live-run dependencies and returns the narrow
// runservice facade used by HTTP handlers, workers, and tests.
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

// SetDispatcher swaps the optional async execution dispatcher used for newly
// created runs or resumed manual-review runs.
func (s *Service) SetDispatcher(dispatcher Dispatcher) {
	s.dispatcher = dispatcher
}
