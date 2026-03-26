package runservice

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type FileCheckpointStore struct {
	root string
}

func NewFileCheckpointStore(root string) *FileCheckpointStore {
	return &FileCheckpointStore{root: root}
}

func (s *FileCheckpointStore) Save(_ context.Context, runID string, snapshot Snapshot) error {
	if err := os.MkdirAll(s.root, 0755); err != nil {
		return fmt.Errorf("create checkpoint directory: %w", err)
	}

	path := filepath.Join(s.root, runID+".json")
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal snapshot: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write snapshot: %w", err)
	}
	return nil
}

func (s *FileCheckpointStore) Load(_ context.Context, runID string) (Snapshot, error) {
	path := filepath.Join(s.root, runID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return Snapshot{}, fmt.Errorf("read snapshot: %w", err)
	}

	var snapshot Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("unmarshal snapshot: %w", err)
	}
	snapshot.ensureContext()
	return snapshot, nil
}

func (s *FileCheckpointStore) Delete(_ context.Context, runID string) error {
	path := filepath.Join(s.root, runID+".json")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete snapshot: %w", err)
	}
	return nil
}

type MemoryCheckpointStore struct {
	mu        sync.Mutex
	snapshots map[string][]byte
}

func NewMemoryCheckpointStore() *MemoryCheckpointStore {
	return &MemoryCheckpointStore{
		snapshots: make(map[string][]byte),
	}
}

func (s *MemoryCheckpointStore) Save(_ context.Context, runID string, snapshot Snapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("marshal snapshot: %w", err)
	}

	s.snapshots[runID] = append([]byte(nil), data...)
	return nil
}

func (s *MemoryCheckpointStore) Load(_ context.Context, runID string) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, ok := s.snapshots[runID]
	if !ok {
		return Snapshot{}, os.ErrNotExist
	}
	var snapshot Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("unmarshal snapshot: %w", err)
	}
	snapshot.ensureContext()
	return snapshot, nil
}

func (s *MemoryCheckpointStore) Delete(_ context.Context, runID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.snapshots, runID)
	return nil
}
