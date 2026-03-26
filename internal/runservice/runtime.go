package runservice

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"cloud.google.com/go/storage"
	firebase "firebase.google.com/go/v4"
	"google.golang.org/api/option"

	"asset-discovery/internal/app"
	"asset-discovery/internal/collect"
	"asset-discovery/internal/tracing/telemetry"
)

var defaultAllowedOrigins = []string{
	"http://localhost:5173",
	"http://127.0.0.1:5173",
	"https://asset-discovery-0325-f111.web.app",
	"https://asset-discovery-0325-f111.firebaseapp.com",
}

// Environment captures the process-level configuration for the live run
// server and worker.
type Environment struct {
	ProjectID           string
	ServerAddr          string
	ExportBucket        string
	ExportPrefix        string
	CheckpointBucket    string
	CheckpointPrefix    string
	CheckpointDir       string
	Outputs             []string
	AllowedOrigins      []string
	WorkerJobProjectID  string
	WorkerJobRegion     string
	WorkerJobName       string
	WorkerContainerName string
}

// Runtime bundles the live run service plus the Firebase-backed helpers it
// needs for HTTP serving or background worker execution.
type Runtime struct {
	Service      *Service
	AuthVerifier AuthVerifier
	Executions   ExecutionStore
	firestore    *firestore.Client
	storage      *storage.Client
}

// LoadEnvironment reads the live run environment from process variables.
func LoadEnvironment() (Environment, error) {
	projectID := strings.TrimSpace(os.Getenv("ASSET_DISCOVERY_FIREBASE_PROJECT_ID"))
	if projectID == "" {
		projectID = strings.TrimSpace(os.Getenv("GOOGLE_CLOUD_PROJECT"))
	}
	if projectID == "" {
		return Environment{}, fmt.Errorf("ASSET_DISCOVERY_FIREBASE_PROJECT_ID or GOOGLE_CLOUD_PROJECT is required")
	}

	serverAddr := strings.TrimSpace(os.Getenv("ASSET_DISCOVERY_SERVER_ADDR"))
	if serverAddr == "" {
		if port := strings.TrimSpace(os.Getenv("PORT")); port != "" {
			serverAddr = ":" + port
		} else {
			serverAddr = ":8080"
		}
	}

	allowedOrigins := splitCommaSeparated(os.Getenv("ASSET_DISCOVERY_ALLOWED_ORIGINS"))
	if len(allowedOrigins) == 0 {
		allowedOrigins = DefaultAllowedOrigins()
	}

	env := Environment{
		ProjectID:           projectID,
		ServerAddr:          serverAddr,
		ExportBucket:        strings.TrimSpace(os.Getenv("ASSET_DISCOVERY_EXPORT_GCS_BUCKET")),
		ExportPrefix:        strings.TrimSpace(os.Getenv("ASSET_DISCOVERY_EXPORT_GCS_PREFIX")),
		CheckpointBucket:    strings.TrimSpace(os.Getenv("ASSET_DISCOVERY_CHECKPOINT_GCS_BUCKET")),
		CheckpointPrefix:    strings.TrimSpace(os.Getenv("ASSET_DISCOVERY_CHECKPOINT_GCS_PREFIX")),
		CheckpointDir:       strings.TrimSpace(os.Getenv("ASSET_DISCOVERY_CHECKPOINT_DIR")),
		Outputs:             splitCommaSeparated(os.Getenv("ASSET_DISCOVERY_OUTPUTS")),
		AllowedOrigins:      allowedOrigins,
		WorkerJobProjectID:  strings.TrimSpace(os.Getenv("ASSET_DISCOVERY_WORKER_JOB_PROJECT_ID")),
		WorkerJobRegion:     strings.TrimSpace(os.Getenv("ASSET_DISCOVERY_WORKER_JOB_REGION")),
		WorkerJobName:       strings.TrimSpace(os.Getenv("ASSET_DISCOVERY_WORKER_JOB_NAME")),
		WorkerContainerName: strings.TrimSpace(os.Getenv("ASSET_DISCOVERY_WORKER_CONTAINER_NAME")),
	}
	if env.ExportBucket == "" {
		return Environment{}, fmt.Errorf("ASSET_DISCOVERY_EXPORT_GCS_BUCKET is required")
	}

	return env, nil
}

// DefaultAllowedOrigins returns a copy of the built-in browser origins used by
// local development and the default Firebase Hosting site.
func DefaultAllowedOrigins() []string {
	return append([]string(nil), defaultAllowedOrigins...)
}

// NewRuntime constructs the Firebase-backed service used by both the HTTP
// server and the Cloud Run worker.
func NewRuntime(ctx context.Context, env Environment) (*Runtime, error) {
	options := firebaseOptions()
	firebaseApp, err := firebase.NewApp(ctx, &firebase.Config{ProjectID: env.ProjectID}, options...)
	if err != nil {
		return nil, fmt.Errorf("create firebase app: %w", err)
	}

	firestoreClient, err := firestore.NewClient(ctx, env.ProjectID, options...)
	if err != nil {
		return nil, fmt.Errorf("create firestore client: %w", err)
	}

	storageClient, err := storage.NewClient(ctx, options...)
	if err != nil {
		_ = firestoreClient.Close()
		return nil, fmt.Errorf("create storage client: %w", err)
	}

	authVerifier, err := NewFirebaseVerifierFromApp(ctx, firebaseApp)
	if err != nil {
		_ = storageClient.Close()
		_ = firestoreClient.Close()
		return nil, fmt.Errorf("create firebase verifier: %w", err)
	}

	checkpoints, err := buildCheckpointStore(storageClient, env)
	if err != nil {
		_ = storageClient.Close()
		_ = firestoreClient.Close()
		return nil, err
	}

	artifacts, err := buildArtifactStore(storageClient, env)
	if err != nil {
		_ = storageClient.Close()
		_ = firestoreClient.Close()
		return nil, err
	}

	projection := NewFirestoreProjectionStore(firestoreClient)
	service, err := NewService(Config{
		PipelineFactory: pipelineFactory(env.Outputs),
		Checkpoints:     checkpoints,
		Projection:      projection,
		Artifacts:       artifacts,
		Now:             time.Now,
	})
	if err != nil {
		_ = storageClient.Close()
		_ = firestoreClient.Close()
		return nil, fmt.Errorf("create run service: %w", err)
	}

	return &Runtime{
		Service:      service,
		AuthVerifier: authVerifier,
		Executions:   projection,
		firestore:    firestoreClient,
		storage:      storageClient,
	}, nil
}

// Close releases the Firebase clients owned by the runtime.
func (r *Runtime) Close() error {
	var closeErr error
	if r.storage != nil {
		if err := r.storage.Close(); err != nil {
			closeErr = err
		}
	}
	if r.firestore != nil {
		if err := r.firestore.Close(); err != nil && closeErr == nil {
			closeErr = err
		}
	}
	return closeErr
}

func pipelineFactory(outputs []string) PipelineFactory {
	return func(runID string) (*app.Pipeline, error) {
		return app.NewPipeline(app.Config{
			Outputs:         outputs,
			OutputsChanged:  len(outputs) > 0,
			RunID:           runID,
			Telemetry:       telemetry.NewStdlibProvider(log.Default()),
			DNSVariantSweep: collect.DefaultDNSVariantSweepConfig(),
		})
	}
}

func buildCheckpointStore(storageClient *storage.Client, env Environment) (CheckpointStore, error) {
	if env.CheckpointBucket != "" {
		return NewGCSCheckpointStore(storageClient, env.CheckpointBucket, env.CheckpointPrefix), nil
	}

	root := env.CheckpointDir
	if root == "" {
		root = "checkpoints"
	}
	return NewFileCheckpointStore(root), nil
}

func buildArtifactStore(storageClient *storage.Client, env Environment) (ArtifactStore, error) {
	prefix := strings.Trim(env.ExportPrefix, "/")
	if strings.Contains(prefix, "/") {
		return nil, fmt.Errorf("ASSET_DISCOVERY_EXPORT_GCS_PREFIX must be empty or a single path segment")
	}
	return NewGCSArtifactStore(storageClient, env.ExportBucket, prefix), nil
}

func firebaseOptions() []option.ClientOption {
	credentialsFile := strings.TrimSpace(os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"))
	if credentialsFile == "" {
		credentialsFile = strings.TrimSpace(os.Getenv("ASSET_DISCOVERY_FIREBASE_CREDENTIALS"))
	}
	if credentialsFile == "" {
		return nil
	}
	return []option.ClientOption{option.WithCredentialsFile(credentialsFile)}
}

func splitCommaSeparated(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}

	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		out = append(out, part)
	}
	return out
}
