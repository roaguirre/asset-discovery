package models

import "testing"

func TestPipelineContext_SnapshotReadModelClonesNestedState(t *testing.T) {
	pCtx := &PipelineContext{
		Seeds: []Seed{
			{
				ID:          "seed-1",
				CompanyName: "ZeroFox",
				Domains:     []string{"zerofox.com"},
				ASN:         []int{64512},
				Tags:        []string{"demo"},
				Evidence: []SeedEvidence{
					{Source: "seed_form", Kind: "domain", Value: "zerofox.com"},
				},
			},
		},
		Enumerations: []Enumeration{
			{ID: "enum-1", SeedID: "seed-1", Status: "running"},
		},
		Assets: []Asset{
			{
				ID:            "asset-1",
				EnumerationID: "enum-1",
				Type:          AssetTypeDomain,
				Identifier:    "api.zerofox.com",
				Source:        "dns_collector",
				DomainDetails: &DomainDetails{
					Records: []DNSRecord{{Type: "A", Value: "203.0.113.10"}},
				},
				EnrichmentData: map[string]interface{}{"cidr": "203.0.113.0/24"},
			},
		},
		Observations: []AssetObservation{
			{
				ID:         "obs-1",
				AssetID:    "asset-1",
				Type:       AssetTypeDomain,
				Identifier: "api.zerofox.com",
				DomainDetails: &DomainDetails{
					Records: []DNSRecord{{Type: "CNAME", Value: "origin.zerofox.com"}},
				},
			},
		},
		Relations: []AssetRelation{
			{ID: "rel-1", Kind: "resolves_to", FromIdentifier: "api.zerofox.com", ToIdentifier: "203.0.113.10"},
		},
		JudgeEvaluations: []JudgeEvaluation{
			{
				Collector:   "web_hint_collector",
				SeedID:      "seed-1",
				SeedDomains: []string{"zerofox.com"},
				Outcomes: []JudgeCandidateOutcome{
					{Root: "zerofoxapp.com", Collect: true, Support: []string{"brand overlap"}},
				},
			},
		},
		DNSVariantSweepLabels: []string{"zerofox"},
	}

	snapshot := pCtx.SnapshotReadModel()

	pCtx.Seeds[0].Domains[0] = "changed.zerofox.com"
	pCtx.Seeds[0].Evidence[0].Value = "changed"
	pCtx.Assets[0].DomainDetails.Records[0].Value = "198.51.100.10"
	pCtx.Assets[0].EnrichmentData["cidr"] = "198.51.100.0/24"
	pCtx.Observations[0].DomainDetails.Records[0].Value = "changed.origin.zerofox.com"
	pCtx.Relations[0].Kind = "redirects_to"
	pCtx.JudgeEvaluations[0].SeedDomains[0] = "changed.zerofox.com"
	pCtx.JudgeEvaluations[0].Outcomes[0].Support[0] = "changed support"
	pCtx.DNSVariantSweepLabels[0] = "changed"

	if got := snapshot.Seeds[0].Domains[0]; got != "zerofox.com" {
		t.Fatalf("expected snapshot seed domain to remain unchanged, got %q", got)
	}
	if got := snapshot.Seeds[0].Evidence[0].Value; got != "zerofox.com" {
		t.Fatalf("expected snapshot seed evidence to remain unchanged, got %q", got)
	}
	if got := snapshot.Assets[0].DomainDetails.Records[0].Value; got != "203.0.113.10" {
		t.Fatalf("expected snapshot asset record to remain unchanged, got %q", got)
	}
	if got := snapshot.Assets[0].EnrichmentData["cidr"]; got != "203.0.113.0/24" {
		t.Fatalf("expected snapshot enrichment data to remain unchanged, got %#v", got)
	}
	if got := snapshot.Observations[0].DomainDetails.Records[0].Value; got != "origin.zerofox.com" {
		t.Fatalf("expected snapshot observation record to remain unchanged, got %q", got)
	}
	if got := snapshot.Relations[0].Kind; got != "resolves_to" {
		t.Fatalf("expected snapshot relation kind to remain unchanged, got %q", got)
	}
	if got := snapshot.JudgeEvaluations[0].SeedDomains[0]; got != "zerofox.com" {
		t.Fatalf("expected snapshot judge seed domain to remain unchanged, got %q", got)
	}
	if got := snapshot.JudgeEvaluations[0].Outcomes[0].Support[0]; got != "brand overlap" {
		t.Fatalf("expected snapshot judge support to remain unchanged, got %q", got)
	}
	if got := snapshot.DNSVariantSweepLabels[0]; got != "zerofox" {
		t.Fatalf("expected snapshot variant sweep labels to remain unchanged, got %q", got)
	}
}
