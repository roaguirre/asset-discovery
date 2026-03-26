package main

import (
	"context"
	"log"
	"os"
	"strings"
	"time"

	"asset-discovery/internal/runservice"
)

func main() {
	ctx := context.Background()

	runID := strings.TrimSpace(os.Getenv("ASSET_DISCOVERY_RUN_ID"))
	if runID == "" {
		log.Fatal("ASSET_DISCOVERY_RUN_ID is required")
	}

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

	worker, err := runservice.NewWorker(runtime.Service, runtime.Executions, runservice.WorkerConfig{
		LeaseTTL:          readDurationEnv("ASSET_DISCOVERY_WORKER_LEASE_TTL", 15*time.Minute),
		HeartbeatInterval: readDurationEnv("ASSET_DISCOVERY_WORKER_HEARTBEAT_INTERVAL", time.Minute),
	})
	if err != nil {
		log.Fatalf("create worker: %v", err)
	}

	if err := worker.Run(ctx, runID); err != nil {
		log.Fatalf("process run %s: %v", runID, err)
	}
}

func readDurationEnv(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}

	duration, err := time.ParseDuration(value)
	if err != nil {
		log.Fatalf("parse %s: %v", key, err)
	}
	return duration
}
