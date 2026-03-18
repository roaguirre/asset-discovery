package nodes

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"asset-discovery/internal/models"
)

func TestVisualizerExporter_ArchivesRunsAndRendersHTML(t *testing.T) {
	htmlPath := filepath.Join(t.TempDir(), "visualizer.html")

	firstRunTime := time.Date(2026, time.March, 17, 22, 50, 0, 0, time.FixedZone("-0300", -3*60*60))
	firstExporter := NewVisualizerExporter(htmlPath, "run-1", models.VisualizerDownloads{
		JSON: "runs/run-1/results.json",
		CSV:  "runs/run-1/results.csv",
	})
	firstExporter.now = func() time.Time { return firstRunTime }

	if _, err := firstExporter.Process(context.Background(), sampleVisualizerContext("seed-1", "enum-1", "asset-1", "api.example.com", firstRunTime)); err != nil {
		t.Fatalf("expected first visualizer export to succeed, got %v", err)
	}

	secondRunTime := firstRunTime.Add(5 * time.Minute)
	secondExporter := NewVisualizerExporter(htmlPath, "run-2", models.VisualizerDownloads{
		JSON: "runs/run-2/results.json",
		XLSX: "runs/run-2/results.xlsx",
	})
	secondExporter.now = func() time.Time { return secondRunTime }

	if _, err := secondExporter.Process(context.Background(), sampleVisualizerContext("seed-2", "enum-2", "asset-2", "app.example.com", secondRunTime)); err != nil {
		t.Fatalf("expected second visualizer export to succeed, got %v", err)
	}

	manifestPath := filepath.Join(strings.TrimSuffix(htmlPath, filepath.Ext(htmlPath)), "manifest.json")
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("expected manifest to exist, got %v", err)
	}

	var manifest models.VisualizerManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatalf("expected manifest JSON to parse, got %v", err)
	}

	if len(manifest.Runs) != 2 {
		t.Fatalf("expected 2 archived runs, got %d", len(manifest.Runs))
	}

	if manifest.Runs[0].ID != "run-2" || manifest.Runs[1].ID != "run-1" {
		t.Fatalf("expected manifest runs ordered newest-first, got %+v", manifest.Runs)
	}

	firstSnapshotPath := filepath.Join(strings.TrimSuffix(htmlPath, filepath.Ext(htmlPath)), "runs", "run-1.json")
	if _, err := os.Stat(firstSnapshotPath); err != nil {
		t.Fatalf("expected first snapshot to exist, got %v", err)
	}

	firstSnapshotData, err := os.ReadFile(firstSnapshotPath)
	if err != nil {
		t.Fatalf("expected first snapshot to be readable, got %v", err)
	}

	var firstSnapshot models.VisualizerRun
	if err := json.Unmarshal(firstSnapshotData, &firstSnapshot); err != nil {
		t.Fatalf("expected first snapshot JSON to parse, got %v", err)
	}

	if len(firstSnapshot.Rows) != 1 {
		t.Fatalf("expected first snapshot to contain 1 row, got %d", len(firstSnapshot.Rows))
	}

	if firstSnapshot.Rows[0].DomainKind != string(models.DomainKindSubdomain) {
		t.Fatalf("expected visualizer row to classify api.example.com as subdomain, got %+v", firstSnapshot.Rows[0])
	}

	if firstSnapshot.Rows[0].ApexDomain != "example.com" {
		t.Fatalf("expected visualizer row apex domain to be example.com, got %+v", firstSnapshot.Rows[0])
	}

	htmlData, err := os.ReadFile(htmlPath)
	if err != nil {
		t.Fatalf("expected visualizer HTML to exist, got %v", err)
	}

	html := string(htmlData)
	for _, needle := range []string{"run-1", "run-2", "api.example.com", "app.example.com", "Domain Kind", "Apex Domain"} {
		if !strings.Contains(html, needle) {
			t.Fatalf("expected rendered HTML to contain %q", needle)
		}
	}
}

func sampleVisualizerContext(seedID, enumerationID, assetID, identifier string, ts time.Time) *models.PipelineContext {
	return &models.PipelineContext{
		Seeds: []models.Seed{
			{ID: seedID, CompanyName: "Example Corp", Domains: []string{"example.com"}},
		},
		Enumerations: []models.Enumeration{
			{
				ID:        enumerationID,
				SeedID:    seedID,
				Status:    "running",
				CreatedAt: ts.Add(-2 * time.Minute),
				UpdatedAt: ts.Add(-1 * time.Minute),
			},
		},
		Assets: []models.Asset{
			{
				ID:            assetID,
				EnumerationID: enumerationID,
				Type:          models.AssetTypeDomain,
				Identifier:    identifier,
				Source:        "crt.sh",
				DiscoveryDate: ts,
				DomainDetails: &models.DomainDetails{
					Records: []models.DNSRecord{
						{Type: "A", Value: "203.0.113.10"},
					},
				},
				EnrichmentData: map[string]interface{}{
					"cidr": "203.0.113.0/24",
				},
			},
		},
	}
}
