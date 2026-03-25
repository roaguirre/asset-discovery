package runservice

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"cloud.google.com/go/storage"

	export "asset-discovery/internal/export"
)

// GCSArtifactStore publishes completed run artifacts into a Cloud Storage
// bucket that the live web app can access through Firebase Storage.
type GCSArtifactStore struct {
	client *storage.Client
	bucket string
	prefix string
}

// NewGCSArtifactStore builds an ArtifactStore that mirrors completed run
// exports into a Cloud Storage bucket for Firebase Storage downloads.
func NewGCSArtifactStore(client *storage.Client, bucket string, prefix string) *GCSArtifactStore {
	return &GCSArtifactStore{
		client: client,
		bucket: strings.TrimSpace(bucket),
		prefix: strings.TrimSpace(prefix),
	}
}

func (s *GCSArtifactStore) Publish(
	ctx context.Context,
	runID string,
	downloads export.Downloads,
) (export.Downloads, error) {
	published := export.Downloads{}

	if downloads.JSON != "" {
		objectName, err := s.uploadFile(ctx, runID, downloads.JSON)
		if err != nil {
			return export.Downloads{}, err
		}
		published.JSON = objectName
	}
	if downloads.CSV != "" {
		objectName, err := s.uploadFile(ctx, runID, downloads.CSV)
		if err != nil {
			return export.Downloads{}, err
		}
		published.CSV = objectName
	}
	if downloads.XLSX != "" {
		objectName, err := s.uploadFile(ctx, runID, downloads.XLSX)
		if err != nil {
			return export.Downloads{}, err
		}
		published.XLSX = objectName
	}

	return published, nil
}

func (s *GCSArtifactStore) uploadFile(ctx context.Context, runID string, localPath string) (string, error) {
	file, err := os.Open(localPath)
	if err != nil {
		return "", fmt.Errorf("open artifact %s: %w", localPath, err)
	}
	defer file.Close()

	objectName := s.objectName(runID, filepath.Base(localPath))
	writer := s.client.Bucket(s.bucket).Object(objectName).NewWriter(ctx)
	writer.ContentType = contentTypeForArtifact(filepath.Ext(localPath))
	if _, err := io.Copy(writer, file); err != nil {
		_ = writer.Close()
		return "", fmt.Errorf("upload artifact %s: %w", localPath, err)
	}
	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("close artifact writer for %s: %w", localPath, err)
	}

	return objectName, nil
}

func (s *GCSArtifactStore) objectName(runID string, filename string) string {
	prefix := strings.TrimSpace(s.prefix)
	if prefix == "" {
		prefix = "runs"
	}
	return path.Join(prefix, runID, filename)
}

func contentTypeForArtifact(extension string) string {
	switch strings.ToLower(strings.TrimSpace(extension)) {
	case ".json":
		return "application/json"
	case ".csv":
		return "text/csv"
	case ".xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	default:
		return "application/octet-stream"
	}
}
