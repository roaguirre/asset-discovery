package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"asset-discovery/internal/runservice"
)

func main() {
	ctx := context.Background()

	env, err := runservice.LoadEnvironment()
	if err != nil {
		log.Fatal(err)
	}

	runtime, err := runservice.NewRuntime(ctx, env)
	if err != nil {
		log.Fatalf("create runtime: %v", err)
	}
	defer func() {
		if closeErr := runtime.Close(); closeErr != nil {
			log.Printf("close runtime: %v", closeErr)
		}
	}()

	dispatcher, err := buildDispatcher(ctx, env, runtime.Service.ProcessRun)
	if err != nil {
		log.Fatalf("create dispatcher: %v", err)
	}
	runtime.Service.SetDispatcher(dispatcher)

	server := &http.Server{
		Addr:              env.ServerAddr,
		Handler:           runservice.NewHandler(runtime.Service, runtime.AuthVerifier, env.AllowedOrigins),
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("asset-discovery server listening on %s", env.ServerAddr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("listen and serve: %v", err)
	}
}

func buildDispatcher(
	ctx context.Context,
	env runservice.Environment,
	processor runservice.RunProcessor,
) (runservice.Dispatcher, error) {
	if env.WorkerJobName == "" && env.WorkerJobRegion == "" {
		return runservice.NewInProcessDispatcher(ctx, processor), nil
	}
	if env.WorkerJobName == "" || env.WorkerJobRegion == "" {
		return nil, fmt.Errorf("ASSET_DISCOVERY_WORKER_JOB_NAME and ASSET_DISCOVERY_WORKER_JOB_REGION must both be set")
	}

	projectID := env.WorkerJobProjectID
	if projectID == "" {
		projectID = env.ProjectID
	}

	return runservice.NewCloudRunJobDispatcher(
		ctx,
		projectID,
		env.WorkerJobRegion,
		env.WorkerJobName,
		env.WorkerContainerName,
	)
}
