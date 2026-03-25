package visualizer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"asset-discovery/internal/models"
)

func buildContractArchiveFixture(t *testing.T) ([]byte, []byte) {
	t.Helper()

	ts := time.Date(2026, time.March, 24, 14, 15, 0, 0, time.UTC)
	pCtx := &models.PipelineContext{
		Seeds: []models.Seed{{
			ID:          "seed-1",
			CompanyName: "Example Corp",
			Domains:     []string{"example.com"},
			Tags:        []string{"production"},
			Evidence: []models.SeedEvidence{{
				Source: "manual",
				Kind:   "company_name",
				Value:  "Example Corp",
			}},
		}},
		Enumerations: []models.Enumeration{{
			ID:        "enum-1",
			SeedID:    "seed-1",
			Status:    "completed",
			CreatedAt: ts.Add(-2 * time.Minute),
			UpdatedAt: ts.Add(-1 * time.Minute),
			StartedAt: ts.Add(-2 * time.Minute),
			EndedAt:   ts,
		}},
		Assets: []models.Asset{
			{
				ID:              "asset-root",
				EnumerationID:   "enum-1",
				Type:            models.AssetTypeDomain,
				Identifier:      "example.com",
				Source:          "rdap_collector",
				DiscoveryDate:   ts,
				OwnershipState:  models.OwnershipStateOwned,
				InclusionReason: "Supported by discovery observations.",
				DomainDetails: &models.DomainDetails{
					RDAP: &models.RDAPData{
						RegistrarName: "Example Registrar",
						RegistrantOrg: "Example Corp",
						Statuses:      []string{"ok"},
					},
				},
			},
			{
				ID:              "asset-sub",
				EnumerationID:   "enum-1",
				Type:            models.AssetTypeDomain,
				Identifier:      "api.example.com",
				Source:          "crt.sh",
				DiscoveryDate:   ts.Add(5 * time.Second),
				OwnershipState:  models.OwnershipStateOwned,
				InclusionReason: "Supported by discovery observations.",
				DomainDetails: &models.DomainDetails{
					Records: []models.DNSRecord{{Type: "A", Value: "203.0.113.10"}},
				},
			},
			{
				ID:              "asset-ip",
				EnumerationID:   "enum-1",
				Type:            models.AssetTypeIP,
				Identifier:      "203.0.113.10",
				Source:          "domain_enricher",
				DiscoveryDate:   ts.Add(10 * time.Second),
				OwnershipState:  models.OwnershipStateAssociatedInfrastructure,
				InclusionReason: "Connected to an owned domain by DNS.",
				IPDetails: &models.IPDetails{
					ASN:          64500,
					Organization: "Example Hosting",
					PTR:          "edge.example.net",
				},
			},
		},
		JudgeEvaluations: []models.JudgeEvaluation{{
			Collector:   "web_hint_collector",
			SeedID:      "seed-1",
			SeedLabel:   "Example Corp",
			SeedDomains: []string{"example.com"},
			Scenario:    "ownership_hints",
			Outcomes: []models.JudgeCandidateOutcome{
				{
					Root:       "example-store.com",
					Collect:    true,
					Explicit:   true,
					Confidence: 0.91,
					Reason:     "Brand match",
					Support:    []string{"https://example-store.com/"},
				},
				{
					Root:       "facebook.com",
					Collect:    false,
					Explicit:   true,
					Confidence: 0.99,
					Reason:     "Third-party platform",
					Support:    []string{"https://facebook.com/example"},
				},
			},
		}},
	}
	pCtx.EnsureAssetState()
	pCtx.AppendAssetObservations(
		models.AssetObservation{
			ID:            "obs-sub-discovery",
			Kind:          models.ObservationKindDiscovery,
			AssetID:       "asset-sub",
			EnumerationID: "enum-1",
			Type:          models.AssetTypeDomain,
			Identifier:    "api.example.com",
			Source:        "hackertarget_collector",
			DiscoveryDate: ts.Add(7 * time.Second),
		},
		models.AssetObservation{
			ID:            "obs-sub-enrichment",
			Kind:          models.ObservationKindEnrichment,
			AssetID:       "asset-sub",
			EnumerationID: "enum-1",
			Type:          models.AssetTypeDomain,
			Identifier:    "api.example.com",
			Source:        "domain_enricher",
			DiscoveryDate: ts.Add(12 * time.Second),
			DomainDetails: &models.DomainDetails{
				Records: []models.DNSRecord{{Type: "A", Value: "203.0.113.10"}},
			},
			EnrichmentStates: map[string]models.EnrichmentState{
				"domain_enricher": {Status: "completed", UpdatedAt: ts.Add(12 * time.Second)},
			},
		},
		models.AssetObservation{
			ID:            "obs-ip-enrichment",
			Kind:          models.ObservationKindEnrichment,
			AssetID:       "asset-ip",
			EnumerationID: "enum-1",
			Type:          models.AssetTypeIP,
			Identifier:    "203.0.113.10",
			Source:        "ip_enricher",
			DiscoveryDate: ts.Add(13 * time.Second),
			IPDetails: &models.IPDetails{
				ASN:          64500,
				Organization: "Example Hosting",
				PTR:          "edge.example.net",
			},
			EnrichmentStates: map[string]models.EnrichmentState{
				"ip_enricher": {Status: "completed", UpdatedAt: ts.Add(13 * time.Second)},
			},
		},
	)
	pCtx.AppendAssetRelations(models.AssetRelation{
		ID:             "rel-dns-a",
		FromAssetID:    "asset-sub",
		FromAssetType:  models.AssetTypeDomain,
		FromIdentifier: "api.example.com",
		ToAssetID:      "asset-ip",
		ToAssetType:    models.AssetTypeIP,
		ToIdentifier:   "203.0.113.10",
		EnumerationID:  "enum-1",
		Source:         "domain_enricher",
		Kind:           "dns_a",
		Label:          "Resolved IP",
		Reason:         "Resolved from api.example.com via A",
		DiscoveryDate:  ts.Add(10 * time.Second),
	})

	run := BuildRun("run-contract", ts.Add(30*time.Second), Downloads{
		JSON: "../runs/run-contract/results.json",
		CSV:  "../runs/run-contract/results.csv",
		XLSX: "../runs/run-contract/results.xlsx",
	}, pCtx)

	dir := t.TempDir()
	if err := NewFileArchiveStore().Save(dir, run); err != nil {
		t.Fatalf("save contract archive: %v", err)
	}

	manifestBytes, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	runBytes, err := os.ReadFile(filepath.Join(dir, "runs", "run-contract.json"))
	if err != nil {
		t.Fatalf("read run snapshot: %v", err)
	}

	return prettyJSONForContract(t, manifestBytes), prettyJSONForContract(t, runBytes)
}

func prettyJSONForContract(t *testing.T, payload []byte) []byte {
	t.Helper()

	var value interface{}
	if err := json.Unmarshal(payload, &value); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}

	formatted, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("indent payload: %v", err)
	}

	return append(formatted, '\n')
}
