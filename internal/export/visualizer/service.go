package visualizer

import (
	"fmt"
	"time"
)

type PageRenderer interface {
	Render(path string, runs []Run, generatedAt time.Time) error
}

type Service struct {
	store    ArchiveStore
	renderer PageRenderer
}

func NewService(store ArchiveStore, renderer PageRenderer) *Service {
	if store == nil {
		store = NewFileArchiveStore()
	}
	if renderer == nil {
		renderer = NewEmbeddedPageRenderer()
	}

	return &Service{
		store:    store,
		renderer: renderer,
	}
}

func (s *Service) Export(path string, run Run, generatedAt time.Time) error {
	if err := s.store.Save(path, run); err != nil {
		return fmt.Errorf("failed to write visualizer snapshot: %w", err)
	}

	runs, err := s.store.Load(path)
	if err != nil {
		return fmt.Errorf("failed to load visualizer runs: %w", err)
	}

	if err := s.renderer.Render(path, runs, generatedAt); err != nil {
		return fmt.Errorf("failed to render visualizer HTML: %w", err)
	}

	return nil
}

func (s *Service) Refresh(path string, generatedAt time.Time) error {
	runs, err := s.store.Load(path)
	if err != nil {
		return fmt.Errorf("failed to load visualizer runs: %w", err)
	}

	if err := s.renderer.Render(path, runs, generatedAt); err != nil {
		return fmt.Errorf("failed to render visualizer HTML: %w", err)
	}

	return nil
}
