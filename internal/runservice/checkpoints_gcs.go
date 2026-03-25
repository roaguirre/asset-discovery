package runservice

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path"

	"cloud.google.com/go/storage"
)

type GCSCheckpointStore struct {
	client *storage.Client
	bucket string
	prefix string
}

func NewGCSCheckpointStore(client *storage.Client, bucket string, prefix string) *GCSCheckpointStore {
	return &GCSCheckpointStore{
		client: client,
		bucket: bucket,
		prefix: prefix,
	}
}

func (s *GCSCheckpointStore) Save(ctx context.Context, runID string, snapshot Snapshot) error {
	writer := s.client.Bucket(s.bucket).Object(s.objectName(runID)).NewWriter(ctx)
	writer.ContentType = "application/json"
	if err := json.NewEncoder(writer).Encode(snapshot); err != nil {
		_ = writer.Close()
		return fmt.Errorf("encode snapshot: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("close snapshot writer: %w", err)
	}
	return nil
}

func (s *GCSCheckpointStore) Load(ctx context.Context, runID string) (Snapshot, error) {
	reader, err := s.client.Bucket(s.bucket).Object(s.objectName(runID)).NewReader(ctx)
	if err != nil {
		return Snapshot{}, fmt.Errorf("open snapshot reader: %w", err)
	}
	defer reader.Close()

	data, err := io.ReadAll(reader)
	if err != nil {
		return Snapshot{}, fmt.Errorf("read snapshot: %w", err)
	}

	var snapshot Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("decode snapshot: %w", err)
	}
	return snapshot, nil
}

func (s *GCSCheckpointStore) Delete(ctx context.Context, runID string) error {
	if err := s.client.Bucket(s.bucket).Object(s.objectName(runID)).Delete(ctx); err != nil {
		return fmt.Errorf("delete snapshot: %w", err)
	}
	return nil
}

func (s *GCSCheckpointStore) objectName(runID string) string {
	prefix := s.prefix
	if prefix == "" {
		prefix = "checkpoints"
	}
	return path.Join(prefix, runID+".json")
}
