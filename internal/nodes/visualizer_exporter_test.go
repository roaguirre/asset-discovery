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

	if len(firstSnapshot.Rows) != 2 {
		t.Fatalf("expected first snapshot to contain 2 rows, got %d", len(firstSnapshot.Rows))
	}

	if firstSnapshot.Rows[0].DomainKind != string(models.DomainKindSubdomain) {
		t.Fatalf("expected visualizer row to classify api.example.com as subdomain, got %+v", firstSnapshot.Rows[0])
	}

	if firstSnapshot.Rows[0].RegistrableDomain != "example.com" {
		t.Fatalf("expected visualizer row registrable domain to be example.com, got %+v", firstSnapshot.Rows[0])
	}

	if firstSnapshot.Rows[0].TracePath != "#trace/run-1/asset-1" {
		t.Fatalf("expected visualizer row trace path to be populated, got %+v", firstSnapshot.Rows[0])
	}

	if len(firstSnapshot.Traces) != 2 {
		t.Fatalf("expected first snapshot to contain 2 traces, got %d", len(firstSnapshot.Traces))
	}

	firstTrace := findTraceByAssetID(firstSnapshot.Traces, "asset-1")
	if firstTrace == nil {
		t.Fatalf("expected trace for asset-1 to be present, got %+v", firstSnapshot.Traces)
	}

	if len(firstTrace.Sections) == 0 {
		t.Fatalf("expected trace sections to be populated, got %+v", firstTrace)
	}

	if len(firstTrace.Related) == 0 || firstTrace.Related[0].AssetID != "asset-1-related" {
		t.Fatalf("expected trace to link to the related result, got %+v", firstTrace.Related)
	}

	htmlData, err := os.ReadFile(htmlPath)
	if err != nil {
		t.Fatalf("expected visualizer HTML to exist, got %v", err)
	}

	html := string(htmlData)
	for _, needle := range []string{
		"run-1",
		"run-2",
		"api.example.com",
		"app.example.com",
		"Domain Kind",
		"Registrable Domain",
		"source-filter-options",
		"splitSources",
		"source-pill",
		`sources: []`,
		`state.sources.every((source) => rowSources.includes(source))`,
		"sourceDescriptions = Object.freeze",
		"Certificate Transparency results from crt.sh",
		`id="app-tooltip"`,
		"data-tooltip=",
		"showTooltip(",
		"trace-view-button",
		"Result Trace",
		"Open Trace",
		"Same Registrable Domain",
		"#trace/run-1/asset-1",
		"data-trace-link",
	} {
		if !strings.Contains(html, needle) {
			t.Fatalf("expected rendered HTML to contain %q", needle)
		}
	}
}

func TestVisualizerExporter_TracePreservesMergedContributorLineage(t *testing.T) {
	htmlPath := filepath.Join(t.TempDir(), "visualizer.html")
	ts := time.Date(2026, time.March, 18, 10, 0, 0, 0, time.FixedZone("-0300", -3*60*60))

	pCtx := sampleMergedVisualizerContext(ts)
	filter := NewMergeFilter()
	if _, err := filter.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected merge filter to succeed, got %v", err)
	}

	if len(pCtx.Assets) != 1 {
		t.Fatalf("expected merged context to collapse to 1 asset, got %d", len(pCtx.Assets))
	}

	exporter := NewVisualizerExporter(htmlPath, "run-merged", models.VisualizerDownloads{})
	exporter.now = func() time.Time { return ts.Add(5 * time.Minute) }

	if _, err := exporter.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected visualizer export to succeed, got %v", err)
	}

	snapshotPath := filepath.Join(strings.TrimSuffix(htmlPath, filepath.Ext(htmlPath)), "runs", "run-merged.json")
	snapshotData, err := os.ReadFile(snapshotPath)
	if err != nil {
		t.Fatalf("expected merged snapshot to be readable, got %v", err)
	}

	var snapshot models.VisualizerRun
	if err := json.Unmarshal(snapshotData, &snapshot); err != nil {
		t.Fatalf("expected merged snapshot JSON to parse, got %v", err)
	}

	if len(snapshot.Rows) != 1 {
		t.Fatalf("expected merged snapshot to contain 1 row, got %d", len(snapshot.Rows))
	}

	if snapshot.Rows[0].EnumerationID != "enum-1, enum-2" {
		t.Fatalf("expected merged row to retain both enumerations, got %+v", snapshot.Rows[0])
	}

	if snapshot.Rows[0].SeedID != "seed-1, seed-2" {
		t.Fatalf("expected merged row to retain both seeds, got %+v", snapshot.Rows[0])
	}

	trace := findTraceByAssetID(snapshot.Traces, "merged-asset-1")
	if trace == nil {
		t.Fatalf("expected merged trace to exist, got %+v", snapshot.Traces)
	}

	if len(trace.Contributors) != 2 {
		t.Fatalf("expected merged trace to preserve 2 contributors, got %+v", trace.Contributors)
	}

	if trace.EnumerationID != "enum-1, enum-2" || trace.SeedID != "seed-1, seed-2" {
		t.Fatalf("expected merged trace summary to retain both enumerations and seeds, got %+v", trace)
	}

	if !hasTraceContributor(trace.Contributors, "merged-asset-2", "enum-2", "seed-2", "wayback_collector") {
		t.Fatalf("expected merged trace to retain second contributor lineage, got %+v", trace.Contributors)
	}

	if !traceSectionContains(trace.Sections, "Contributor Provenance", "enumeration enum-2") {
		t.Fatalf("expected contributor provenance section to include enum-2, got %+v", trace.Sections)
	}

	if !traceSectionContains(trace.Sections, "Seed Context", "Evidence: ownership_judge | ownership_judged | example.com | confidence 0.93 | reasoned") {
		t.Fatalf("expected merged trace to include seed evidence for contributor seeds, got %+v", trace.Sections)
	}
}

func sampleVisualizerContext(seedID, enumerationID, assetID, identifier string, ts time.Time) *models.PipelineContext {
	return &models.PipelineContext{
		Seeds: []models.Seed{
			{
				ID:          seedID,
				CompanyName: "Example Corp",
				Domains:     []string{"example.com"},
				Tags:        []string{"production"},
				Evidence: []models.SeedEvidence{
					{Source: "manual", Kind: "company_name", Value: "Example Corp"},
				},
			},
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
					RDAP: &models.RDAPData{
						RegistrarName: "Example Registrar",
						RegistrantOrg: "Example Corp",
						NameServers:   []string{"ns1.example.com"},
					},
				},
				EnrichmentData: map[string]interface{}{
					"cidr": "203.0.113.0/24",
				},
			},
			{
				ID:            assetID + "-related",
				EnumerationID: enumerationID,
				Type:          models.AssetTypeDomain,
				Identifier:    "www.example.com",
				Source:        "wayback_collector",
				DiscoveryDate: ts.Add(-1 * time.Minute),
				DomainDetails: &models.DomainDetails{
					Records: []models.DNSRecord{
						{Type: "CNAME", Value: "edge.example.net"},
					},
				},
			},
		},
	}
}

func sampleMergedVisualizerContext(ts time.Time) *models.PipelineContext {
	return &models.PipelineContext{
		Seeds: []models.Seed{
			{
				ID:          "seed-1",
				CompanyName: "Example Corp",
				Domains:     []string{"example.com"},
				Evidence: []models.SeedEvidence{
					{Source: "manual", Kind: "company_name", Value: "Example Corp"},
				},
			},
			{
				ID:          "seed-2",
				CompanyName: "Example Subsidiary",
				Domains:     []string{"example.com"},
				Tags:        []string{"subsidiary"},
				Evidence: []models.SeedEvidence{
					{Source: "ownership_judge", Kind: "ownership_judged", Value: "example.com", Confidence: 0.93, Reasoned: true},
				},
			},
		},
		Enumerations: []models.Enumeration{
			{
				ID:        "enum-1",
				SeedID:    "seed-1",
				Status:    "running",
				CreatedAt: ts.Add(-4 * time.Minute),
				UpdatedAt: ts.Add(-3 * time.Minute),
			},
			{
				ID:        "enum-2",
				SeedID:    "seed-2",
				Status:    "running",
				CreatedAt: ts.Add(-2 * time.Minute),
				UpdatedAt: ts.Add(-1 * time.Minute),
			},
		},
		Assets: []models.Asset{
			{
				ID:            "merged-asset-1",
				EnumerationID: "enum-1",
				Type:          models.AssetTypeDomain,
				Identifier:    "api.example.com",
				Source:        "crt.sh",
				DiscoveryDate: ts.Add(-2 * time.Minute),
				DomainDetails: &models.DomainDetails{
					Records: []models.DNSRecord{
						{Type: "A", Value: "203.0.113.10"},
					},
				},
			},
			{
				ID:            "merged-asset-2",
				EnumerationID: "enum-2",
				Type:          models.AssetTypeDomain,
				Identifier:    "api.example.com",
				Source:        "wayback_collector",
				DiscoveryDate: ts.Add(-1 * time.Minute),
				DomainDetails: &models.DomainDetails{
					Records: []models.DNSRecord{
						{Type: "CNAME", Value: "edge.example.net"},
					},
				},
			},
		},
	}
}

func findTraceByAssetID(traces []models.VisualizerTrace, assetID string) *models.VisualizerTrace {
	for i := range traces {
		if traces[i].AssetID == assetID {
			return &traces[i]
		}
	}
	return nil
}

func hasTraceContributor(contributors []models.VisualizerTraceContributor, assetID, enumerationID, seedID, source string) bool {
	for _, contributor := range contributors {
		if contributor.AssetID == assetID && contributor.EnumerationID == enumerationID && contributor.SeedID == seedID && contributor.Source == source {
			return true
		}
	}
	return false
}

func traceSectionContains(sections []models.VisualizerTraceSection, title, fragment string) bool {
	for _, section := range sections {
		if section.Title != title {
			continue
		}
		for _, item := range section.Items {
			if strings.Contains(item, fragment) {
				return true
			}
		}
	}
	return false
}
