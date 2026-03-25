package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"cloud.google.com/go/storage"
	firebase "firebase.google.com/go/v4"
	"google.golang.org/api/option"

	"asset-discovery/internal/app"
	"asset-discovery/internal/collect"
	"asset-discovery/internal/runservice"
	"asset-discovery/internal/tracing/telemetry"
)

func main() {
	ctx := context.Background()

	projectID := strings.TrimSpace(os.Getenv("ASSET_DISCOVERY_FIREBASE_PROJECT_ID"))
	if projectID == "" {
		projectID = strings.TrimSpace(os.Getenv("GOOGLE_CLOUD_PROJECT"))
	}
	if projectID == "" {
		log.Fatal("ASSET_DISCOVERY_FIREBASE_PROJECT_ID or GOOGLE_CLOUD_PROJECT is required")
	}

	options := firebaseOptions()
	firebaseApp, err := firebase.NewApp(ctx, &firebase.Config{ProjectID: projectID}, options...)
	if err != nil {
		log.Fatalf("create firebase app: %v", err)
	}

	firestoreClient, err := firestore.NewClient(ctx, projectID, options...)
	if err != nil {
		log.Fatalf("create firestore client: %v", err)
	}
	defer firestoreClient.Close()

	storageClient, err := storage.NewClient(ctx, options...)
	if err != nil {
		log.Fatalf("create storage client: %v", err)
	}
	defer storageClient.Close()

	authVerifier, err := runservice.NewFirebaseVerifierFromApp(ctx, firebaseApp)
	if err != nil {
		log.Fatalf("create firebase verifier: %v", err)
	}

	checkpointStore := buildCheckpointStore(storageClient)
	artifactStore := buildArtifactStore(storageClient)
	projection := runservice.NewFirestoreProjectionStore(firestoreClient)

	pipelineFactory := func(runID string) (*app.Pipeline, error) {
		outputs := splitCommaSeparated(os.Getenv("ASSET_DISCOVERY_OUTPUTS"))
		return app.NewPipeline(app.Config{
			Outputs:         outputs,
			OutputsChanged:  len(outputs) > 0,
			RunID:           runID,
			Telemetry:       telemetry.NewStdlibProvider(log.Default()),
			DNSVariantSweep: collect.DefaultDNSVariantSweepConfig(),
		})
	}

	service, err := runservice.NewService(runservice.Config{
		PipelineFactory: pipelineFactory,
		Checkpoints:     checkpointStore,
		Projection:      projection,
		Artifacts:       artifactStore,
		Now:             time.Now,
	})
	if err != nil {
		log.Fatalf("create run service: %v", err)
	}

	dispatcher := runservice.NewInProcessDispatcher(ctx, service.ProcessRun)
	service.SetDispatcher(dispatcher)

	addr := strings.TrimSpace(os.Getenv("ASSET_DISCOVERY_SERVER_ADDR"))
	if addr == "" {
		addr = ":8080"
	}

	server := &http.Server{
		Addr:              addr,
		Handler:           runservice.NewHandler(service, authVerifier),
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("asset-discovery server listening on %s", addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("listen and serve: %v", err)
	}
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

func buildCheckpointStore(storageClient *storage.Client) runservice.CheckpointStore {
	bucket := strings.TrimSpace(os.Getenv("ASSET_DISCOVERY_CHECKPOINT_GCS_BUCKET"))
	if bucket != "" {
		return runservice.NewGCSCheckpointStore(storageClient, bucket, strings.TrimSpace(os.Getenv("ASSET_DISCOVERY_CHECKPOINT_GCS_PREFIX")))
	}

	root := strings.TrimSpace(os.Getenv("ASSET_DISCOVERY_CHECKPOINT_DIR"))
	if root == "" {
		root = "checkpoints"
	}
	return runservice.NewFileCheckpointStore(root)
}

func buildArtifactStore(storageClient *storage.Client) runservice.ArtifactStore {
	bucket := strings.TrimSpace(os.Getenv("ASSET_DISCOVERY_EXPORT_GCS_BUCKET"))
	if bucket == "" {
		log.Fatal("ASSET_DISCOVERY_EXPORT_GCS_BUCKET is required")
	}
	prefix := strings.Trim(strings.TrimSpace(os.Getenv("ASSET_DISCOVERY_EXPORT_GCS_PREFIX")), "/")
	if strings.Contains(prefix, "/") {
		log.Fatal("ASSET_DISCOVERY_EXPORT_GCS_PREFIX must be empty or a single path segment")
	}
	return runservice.NewGCSArtifactStore(storageClient, bucket, prefix)
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
