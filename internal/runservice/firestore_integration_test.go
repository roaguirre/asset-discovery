package runservice

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/firestore"

	"asset-discovery/internal/app"
	"asset-discovery/internal/dag"
	"asset-discovery/internal/export/visualizer"
	"asset-discovery/internal/models"
	"asset-discovery/internal/tracing/lineage"
	"asset-discovery/internal/tracing/telemetry"
)

func newEmulatorFirestoreClient(t *testing.T) (*firestore.Client, context.Context) {
	t.Helper()

	if strings.TrimSpace(os.Getenv("FIRESTORE_EMULATOR_HOST")) == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}

	ctx := context.Background()
	projectID := fmt.Sprintf("demo-asset-discovery-%d", time.Now().UnixNano())
	client, err := firestore.NewClient(ctx, projectID)
	if err != nil {
		t.Fatalf("firestore.NewClient() error = %v", err)
	}
	return client, ctx
}

func TestFirestoreProjectionStore_EmulatorRoundTrip(t *testing.T) {
	client, ctx := newEmulatorFirestoreClient(t)
	defer client.Close()

	store := NewFirestoreProjectionStore(client)
	now := time.Date(2026, time.March, 25, 12, 0, 0, 0, time.UTC)

	run := RunRecord{
		ID:                "run-firestore",
		OwnerUID:          "uid-owner",
		OwnerEmail:        "analyst@zerofox.com",
		Mode:              RunModeManual,
		Status:            RunStatusRunning,
		CurrentWave:       2,
		SeedCount:         1,
		EnumerationCount:  1,
		AssetCount:        1,
		PendingPivotCount: 1,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	seed := SeedRecord{
		ID:          "seed-1",
		Source:      "submitted",
		SubmittedAt: now,
		Seed: models.Seed{
			ID:          "seed-1",
			CompanyName: "Example Corp",
			Domains:     []string{"example.com"},
		},
	}
	pivot := PivotRecord{
		ID:                   "pivot-1",
		Root:                 "pivot.example.com",
		Status:               PivotDecisionPendingReview,
		Collector:            "web_hint_collector",
		RecommendationKind:   "brand_overlap",
		RecommendationReason: "Shared branding",
		RecommendationScore:  0.97,
		Candidate: models.Seed{
			ID:          "seed-pivot",
			CompanyName: "Pivot Example",
			Domains:     []string{"pivot.example.com"},
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	event := EventRecord{
		ID:        "event-1",
		Kind:      "asset_upserted",
		Message:   "Asset projected.",
		CreatedAt: now,
	}
	row := visualizer.Row{
		AssetID:       "asset-1",
		Identifier:    "example.com",
		AssetType:     "domain",
		Source:        "scripted_collector",
		EnumerationID: "enum-1",
		SeedID:        "seed-1",
		Status:        "completed",
		DiscoveryDate: now,
	}
	trace := lineage.Trace{
		AssetID:       "asset-1",
		Identifier:    "example.com",
		AssetType:     "domain",
		Source:        "scripted_collector",
		EnumerationID: "enum-1",
		SeedID:        "seed-1",
		RootNodeID:    "node-1",
		Nodes: []lineage.TraceNode{
			{ID: "node-1", Kind: "asset", Label: "example.com"},
		},
	}

	if err := store.UpsertRun(ctx, run); err != nil {
		t.Fatalf("UpsertRun() error = %v", err)
	}
	if err := store.UpsertSeed(ctx, run.ID, seed); err != nil {
		t.Fatalf("UpsertSeed() error = %v", err)
	}
	if err := store.UpsertPivot(ctx, run.ID, pivot); err != nil {
		t.Fatalf("UpsertPivot() error = %v", err)
	}
	if err := store.AppendEvent(ctx, run.ID, event); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	if err := store.UpsertAsset(ctx, run.ID, row); err != nil {
		t.Fatalf("UpsertAsset() error = %v", err)
	}
	if err := store.SyncTraces(ctx, run.ID, []lineage.Trace{trace}); err != nil {
		t.Fatalf("SyncTraces() error = %v", err)
	}

	runDoc, err := client.Collection("runs").Doc(run.ID).Get(ctx)
	if err != nil {
		t.Fatalf("Get(run) error = %v", err)
	}

	var gotRun RunRecord
	if err := runDoc.DataTo(&gotRun); err != nil {
		t.Fatalf("DataTo(run) error = %v", err)
	}
	if gotRun.OwnerUID != run.OwnerUID || gotRun.Status != run.Status {
		t.Fatalf("unexpected run projection: %+v", gotRun)
	}

	assetDoc, err := client.Collection("runs").Doc(run.ID).Collection("assets").Doc(row.AssetID).Get(ctx)
	if err != nil {
		t.Fatalf("Get(asset) error = %v", err)
	}

	assetData := assetDoc.Data()
	if got := assetData["identifier"]; got != row.Identifier {
		t.Fatalf("expected asset identifier %q, got %#v", row.Identifier, got)
	}
	if _, exists := assetData["Identifier"]; exists {
		t.Fatalf("expected asset projection to use snake_case keys, got %+v", assetData)
	}

	traceDoc, err := client.Collection("runs").Doc(run.ID).Collection("traces").Doc(trace.AssetID).Get(ctx)
	if err != nil {
		t.Fatalf("Get(trace) error = %v", err)
	}

	traceData := traceDoc.Data()
	if got := traceData["root_node_id"]; got != trace.RootNodeID {
		t.Fatalf("expected trace root %q, got %#v", trace.RootNodeID, got)
	}
	if _, exists := traceData["RootNodeID"]; exists {
		t.Fatalf("expected trace projection to use snake_case keys, got %+v", traceData)
	}
}

func TestFirestoreProjectionStore_SyncTracesChunksLargeSets(t *testing.T) {
	client, ctx := newEmulatorFirestoreClient(t)
	defer client.Close()

	store := NewFirestoreProjectionStore(client)
	runID := "run-large-traces"
	traces := make([]lineage.Trace, 0, 620)
	for i := 0; i < 620; i++ {
		assetID := fmt.Sprintf("asset-%03d", i)
		traces = append(traces, lineage.Trace{
			AssetID:       assetID,
			Identifier:    fmt.Sprintf("asset-%03d.example.com", i),
			AssetType:     "domain",
			RootNodeID:    "node-" + assetID,
			Source:        "scripted_collector",
			EnumerationID: "enum-" + assetID,
			SeedID:        "seed-root",
			Nodes: []lineage.TraceNode{
				{ID: "node-" + assetID, Kind: "asset", Label: assetID},
			},
		})
	}

	if err := store.SyncTraces(ctx, runID, traces); err != nil {
		t.Fatalf("SyncTraces(initial) error = %v", err)
	}

	docs, err := client.Collection("runs").Doc(runID).Collection("traces").Documents(ctx).GetAll()
	if err != nil {
		t.Fatalf("GetAll(initial traces) error = %v", err)
	}
	if len(docs) != len(traces) {
		t.Fatalf("expected %d traces, got %d", len(traces), len(docs))
	}

	if err := store.SyncTraces(ctx, runID, traces[:275]); err != nil {
		t.Fatalf("SyncTraces(delete pass) error = %v", err)
	}

	docs, err = client.Collection("runs").Doc(runID).Collection("traces").Documents(ctx).GetAll()
	if err != nil {
		t.Fatalf("GetAll(reduced traces) error = %v", err)
	}
	if len(docs) != 275 {
		t.Fatalf("expected 275 traces after resync, got %d", len(docs))
	}
}

func TestService_ManualRun_ProjectsToFirestoreEmulator(t *testing.T) {
	client, ctx := newEmulatorFirestoreClient(t)
	defer client.Close()

	checkpoints := NewMemoryCheckpointStore()
	projection := NewFirestoreProjectionStore(client)
	now := func() time.Time {
		return time.Date(2026, time.March, 25, 12, 0, 0, 0, time.UTC)
	}
	factory := func(runID string) (*app.Pipeline, error) {
		engine := &dag.Engine{
			Collectors: []dag.Collector{&scriptedCollector{}},
		}
		return app.NewPipelineWithEngine(engine, runID, nil, telemetry.Noop()), nil
	}

	service, err := NewService(Config{
		PipelineFactory: factory,
		Checkpoints:     checkpoints,
		Projection:      projection,
		Now:             now,
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	run, err := service.CreateRun(ctx, AuthenticatedUser{
		UID:           "uid-1",
		Email:         "reviewer@zerofox.com",
		EmailVerified: true,
	}, CreateRunRequest{
		Mode: RunModeManual,
		Seeds: []models.Seed{
			{
				ID:          "seed-root",
				CompanyName: "Root Example",
				Domains:     []string{"root.example.com"},
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}

	runDoc, err := client.Collection("runs").Doc(run.ID).Get(ctx)
	if err != nil {
		t.Fatalf("Get(created run) error = %v", err)
	}
	var queued RunRecord
	if err := runDoc.DataTo(&queued); err != nil {
		t.Fatalf("DataTo(created run) error = %v", err)
	}
	if queued.Status != RunStatusQueued {
		t.Fatalf("expected queued status after create, got %s", queued.Status)
	}

	seedDocs, err := client.Collection("runs").Doc(run.ID).Collection("seeds").Documents(ctx).GetAll()
	if err != nil {
		t.Fatalf("GetAll(seeds after create) error = %v", err)
	}
	if len(seedDocs) != 1 {
		t.Fatalf("expected one submitted seed, got %d", len(seedDocs))
	}

	if err := service.ProcessRun(ctx, run.ID); err != nil {
		t.Fatalf("ProcessRun(initial) error = %v", err)
	}

	runDoc, err = client.Collection("runs").Doc(run.ID).Get(ctx)
	if err != nil {
		t.Fatalf("Get(paused run) error = %v", err)
	}
	var paused RunRecord
	if err := runDoc.DataTo(&paused); err != nil {
		t.Fatalf("DataTo(paused run) error = %v", err)
	}
	if paused.Status != RunStatusAwaitingReview {
		t.Fatalf("expected awaiting_review status, got %s", paused.Status)
	}
	if paused.PendingPivotCount != 1 {
		t.Fatalf("expected one pending pivot, got %d", paused.PendingPivotCount)
	}

	pivotDocs, err := client.Collection("runs").Doc(run.ID).Collection("pivots").Documents(ctx).GetAll()
	if err != nil {
		t.Fatalf("GetAll(pivots) error = %v", err)
	}
	if len(pivotDocs) != 1 {
		t.Fatalf("expected one pivot, got %d", len(pivotDocs))
	}

	var pending PivotRecord
	if err := pivotDocs[0].DataTo(&pending); err != nil {
		t.Fatalf("DataTo(pivot) error = %v", err)
	}
	if pending.Status != PivotDecisionPendingReview {
		t.Fatalf("expected pending_review pivot, got %s", pending.Status)
	}

	if _, err := service.DecidePivot(ctx, AuthenticatedUser{
		UID:           "uid-1",
		Email:         "reviewer@zerofox.com",
		EmailVerified: true,
	}, run.ID, pending.ID, PivotDecisionInputAccepted); err != nil {
		t.Fatalf("DecidePivot() error = %v", err)
	}

	if err := service.ProcessRun(ctx, run.ID); err != nil {
		t.Fatalf("ProcessRun(resume) error = %v", err)
	}

	runDoc, err = client.Collection("runs").Doc(run.ID).Get(ctx)
	if err != nil {
		t.Fatalf("Get(completed run) error = %v", err)
	}
	var completed RunRecord
	if err := runDoc.DataTo(&completed); err != nil {
		t.Fatalf("DataTo(completed run) error = %v", err)
	}
	if completed.Status != RunStatusCompleted {
		t.Fatalf("expected completed status, got %s", completed.Status)
	}

	assetDocs, err := client.Collection("runs").Doc(run.ID).Collection("assets").Documents(ctx).GetAll()
	if err != nil {
		t.Fatalf("GetAll(assets) error = %v", err)
	}
	if len(assetDocs) < 2 {
		t.Fatalf("expected at least two assets after accepting the pivot, got %d", len(assetDocs))
	}

	seedDocs, err = client.Collection("runs").Doc(run.ID).Collection("seeds").Documents(ctx).GetAll()
	if err != nil {
		t.Fatalf("GetAll(seeds after decision) error = %v", err)
	}
	if len(seedDocs) < 2 {
		t.Fatalf("expected accepted pivot to add a second seed, got %d", len(seedDocs))
	}

	eventDocs, err := client.Collection("runs").Doc(run.ID).Collection("events").Documents(ctx).GetAll()
	if err != nil {
		t.Fatalf("GetAll(events) error = %v", err)
	}
	if len(eventDocs) == 0 {
		t.Fatalf("expected projected events to exist")
	}
}
