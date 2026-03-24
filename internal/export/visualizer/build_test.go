package visualizer

import (
	"strings"
	"testing"
	"time"

	"asset-discovery/internal/models"
	"asset-discovery/internal/tracing/lineage"
)

func TestBuildRun_PopulatesSourcesEvidenceTraceLinksAndJudgeSummary(t *testing.T) {
	ts := time.Date(2026, time.March, 24, 9, 0, 0, 0, time.FixedZone("-0300", -3*60*60))
	pCtx := &models.PipelineContext{
		Seeds: []models.Seed{
			{ID: "seed-1", CompanyName: "Example Corp", Domains: []string{"example.com"}},
		},
		Enumerations: []models.Enumeration{
			{ID: "enum-1", SeedID: "seed-1", Status: "completed", CreatedAt: ts},
		},
		Assets: []models.Asset{
			{
				ID:            "asset-root",
				EnumerationID: "enum-1",
				Type:          models.AssetTypeDomain,
				Identifier:    "example.com",
				Source:        "rdap_collector",
				DiscoveryDate: ts,
				DomainDetails: &models.DomainDetails{
					RDAP: &models.RDAPData{
						RegistrarName: "Example Registrar",
						Statuses:      []string{"ok"},
					},
				},
			},
			{
				ID:            "asset-sub",
				EnumerationID: "enum-1",
				Type:          models.AssetTypeDomain,
				Identifier:    "api.example.com",
				Source:        "crt.sh",
				DiscoveryDate: ts.Add(10 * time.Second),
				DomainDetails: &models.DomainDetails{
					Records: []models.DNSRecord{{Type: "A", Value: "203.0.113.10"}},
				},
			},
			{
				ID:            "asset-peer",
				EnumerationID: "enum-1",
				Type:          models.AssetTypeDomain,
				Identifier:    "www.example.com",
				Source:        "wayback_collector",
				DiscoveryDate: ts.Add(20 * time.Second),
			},
		},
		JudgeEvaluations: []models.JudgeEvaluation{
			{
				Collector:   "web_hint_collector",
				SeedID:      "seed-1",
				SeedLabel:   "Example Corp",
				SeedDomains: []string{"example.com"},
				Scenario:    "ownership hints",
				Outcomes: []models.JudgeCandidateOutcome{
					{Root: "example-store.com", Collect: true, Explicit: true, Confidence: 0.91},
					{Root: "facebook.com", Collect: false, Explicit: true, Confidence: 0.98},
				},
			},
		},
	}
	pCtx.EnsureAssetState()
	pCtx.AppendAssetObservations(
		models.AssetObservation{
			ID:            "obs-discovery-1",
			Kind:          models.ObservationKindDiscovery,
			AssetID:       "asset-sub",
			EnumerationID: "enum-1",
			Type:          models.AssetTypeDomain,
			Identifier:    "api.example.com",
			Source:        "hackertarget_collector",
			DiscoveryDate: ts.Add(15 * time.Second),
		},
		models.AssetObservation{
			ID:            "obs-enrichment-1",
			Kind:          models.ObservationKindEnrichment,
			AssetID:       "asset-sub",
			EnumerationID: "enum-1",
			Type:          models.AssetTypeDomain,
			Identifier:    "api.example.com",
			Source:        "domain_enricher",
			DiscoveryDate: ts.Add(20 * time.Second),
			DomainDetails: &models.DomainDetails{
				Records: []models.DNSRecord{{Type: "A", Value: "203.0.113.10"}},
			},
			EnrichmentStates: map[string]models.EnrichmentState{
				"domain_enricher": {Status: "completed", UpdatedAt: ts.Add(20 * time.Second)},
			},
		},
	)

	run := BuildRun("run-build", ts.Add(30*time.Second), Downloads{}, pCtx)
	if len(run.Rows) != 3 {
		t.Fatalf("expected 3 rows, got %+v", run.Rows)
	}
	if run.Rows[0].Identifier != "example.com" {
		t.Fatalf("expected registrable domain to sort first, got %+v", run.Rows)
	}

	row := findRowByIdentifier(run.Rows, "api.example.com")
	if row == nil {
		t.Fatalf("expected api.example.com row, got %+v", run.Rows)
	}
	if row.Source != "crt.sh, hackertarget_collector" {
		t.Fatalf("expected discovery sources only on row source, got %+v", row)
	}
	if row.EnrichedBy != "domain_enricher" {
		t.Fatalf("expected enrichment source to be separated, got %+v", row)
	}
	if !hasEvidenceGroup(row.EvidenceGroups, "DNS", "A:203.0.113.10") {
		t.Fatalf("expected DNS evidence group, got %+v", row.EvidenceGroups)
	}
	if !strings.Contains(row.Details, "Resolution resolved") {
		t.Fatalf("expected row details to contain resolution marker, got %q", row.Details)
	}
	if row.TracePath != "#trace/run-build/asset-sub" {
		t.Fatalf("expected trace path to be populated, got %+v", row)
	}

	trace := findTraceByAssetID(run.Traces, "asset-sub")
	if trace == nil {
		t.Fatalf("expected trace for asset-sub, got %+v", run.Traces)
	}
	if len(trace.Related) == 0 || trace.Related[0].Label != "Same Registrable Domain" {
		t.Fatalf("expected same-domain related trace links, got %+v", trace.Related)
	}
	if run.JudgeSummary == nil || run.JudgeSummary.EvaluationCount != 1 || run.JudgeSummary.AcceptedCount != 1 || run.JudgeSummary.DiscardedCount != 1 {
		t.Fatalf("expected judge summary counts, got %+v", run.JudgeSummary)
	}
	if !snapshotHasJudgeCandidate(run.JudgeSummary, "example-store.com", true) || !snapshotHasJudgeCandidate(run.JudgeSummary, "facebook.com", false) {
		t.Fatalf("expected accepted and discarded candidates in judge summary, got %+v", run.JudgeSummary)
	}
}

func findRowByIdentifier(rows []Row, identifier string) *Row {
	for i := range rows {
		if rows[i].Identifier == identifier {
			return &rows[i]
		}
	}
	return nil
}

func findTraceByAssetID(traces []lineage.Trace, assetID string) *lineage.Trace {
	for i := range traces {
		if traces[i].AssetID == assetID {
			return &traces[i]
		}
	}
	return nil
}

func hasEvidenceGroup(groups []EvidenceGroup, title, itemFragment string) bool {
	for _, group := range groups {
		if group.Title != title {
			continue
		}
		for _, item := range group.Items {
			if strings.Contains(item, itemFragment) {
				return true
			}
		}
	}
	return false
}

func snapshotHasJudgeCandidate(summary *lineage.JudgeSummary, root string, accepted bool) bool {
	if summary == nil {
		return false
	}
	for _, group := range summary.Groups {
		candidates := group.Discarded
		if accepted {
			candidates = group.Accepted
		}
		for _, candidate := range candidates {
			if candidate.Root == root {
				return true
			}
		}
	}
	return false
}
