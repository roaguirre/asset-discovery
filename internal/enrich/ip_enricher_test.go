package enrich

import (
	"context"
	"net"
	"testing"

	"asset-discovery/internal/models"
)

func TestIPEnricher_SchedulesInScopePTRHostnamesWithoutRootPromotion(t *testing.T) {
	collector := NewIPEnricher()
	collector.judge = nil
	collector.enrichAsset = func(asset *models.Asset) {
		if asset.IPDetails == nil {
			asset.IPDetails = &models.IPDetails{}
		}
		asset.IPDetails.PTR = "vpn.example.com."
	}

	pCtx := &models.PipelineContext{
		Seeds: []models.Seed{
			{ID: "seed-1", CompanyName: "Example Corp", Domains: []string{"example.com"}},
		},
		Enumerations: []models.Enumeration{
			{ID: "enum-1", SeedID: "seed-1", Status: "running"},
		},
		Assets: []models.Asset{
			{
				ID:            "ip-1",
				EnumerationID: "enum-1",
				Type:          models.AssetTypeIP,
				Identifier:    "203.0.113.10",
			},
		},
	}
	pCtx.InitializeSeedFrontier(1)

	if _, err := collector.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected collector to succeed, got %v", err)
	}

	if !seedHasDomain(pCtx.Seeds, "vpn.example.com") {
		t.Fatalf("expected PTR hostname to be scheduled for follow-up, got %+v", pCtx.Seeds)
	}
	if len(pCtx.Seeds) != 2 {
		t.Fatalf("expected exactly one new in-scope PTR follow-up seed, got %+v", pCtx.Seeds)
	}
}

func TestIPEnricher_DoesNotScheduleCrossRootPTRHostnamesWithoutJudgeApproval(t *testing.T) {
	collector := NewIPEnricher()
	collector.judge = nil
	collector.enrichAsset = func(asset *models.Asset) {
		if asset.IPDetails == nil {
			asset.IPDetails = &models.IPDetails{}
		}
		asset.IPDetails.PTR = "vpn.example.com."
	}

	pCtx := &models.PipelineContext{
		Seeds: []models.Seed{
			{ID: "seed-1", CompanyName: "Example Corp", Domains: []string{"seedcorp.com"}},
		},
		Enumerations: []models.Enumeration{
			{ID: "enum-1", SeedID: "seed-1", Status: "running"},
		},
		Assets: []models.Asset{
			{
				ID:            "ip-1",
				EnumerationID: "enum-1",
				Type:          models.AssetTypeIP,
				Identifier:    "203.0.113.10",
			},
		},
	}
	pCtx.InitializeSeedFrontier(1)

	if _, err := collector.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected collector to succeed, got %v", err)
	}

	if seedHasDomain(pCtx.Seeds, "vpn.example.com") {
		t.Fatalf("expected cross-root PTR hostname to stay out of follow-up seeds without judge approval, got %+v", pCtx.Seeds)
	}
	if seedHasDomain(pCtx.Seeds, "example.com") {
		t.Fatalf("expected cross-root registrable root to remain unchanged without a judge, got %+v", pCtx.Seeds)
	}
}

func TestIPEnricher_RecordsEnrichmentObservationForPTRMismatch(t *testing.T) {
	collector := NewIPEnricher()
	collector.judge = nil
	collector.enrichAsset = func(asset *models.Asset) {
		if asset.IPDetails == nil {
			asset.IPDetails = &models.IPDetails{}
		}
		if asset.EnrichmentStates == nil {
			asset.EnrichmentStates = make(map[string]models.EnrichmentState)
		}
		asset.IPDetails.PTR = "vps.example.net."
		asset.EnrichmentStates["ip_enricher"] = models.EnrichmentState{Status: "completed"}
	}

	pCtx := &models.PipelineContext{
		Seeds: []models.Seed{
			{ID: "seed-1", CompanyName: "Example Corp", Domains: []string{"example.com"}},
		},
		Enumerations: []models.Enumeration{
			{ID: "enum-1", SeedID: "seed-1", Status: "running"},
		},
		Assets: []models.Asset{
			{
				ID:              "ip-1",
				EnumerationID:   "enum-1",
				Type:            models.AssetTypeIP,
				Identifier:      "203.0.113.10",
				Source:          "domain_enricher",
				OwnershipState:  models.OwnershipStateAssociatedInfrastructure,
				InclusionReason: "Observed as infrastructure supporting an in-scope domain",
			},
		},
	}

	if _, err := collector.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected collector to succeed, got %v", err)
	}

	if len(pCtx.Observations) != 2 {
		t.Fatalf("expected discovery and enrichment observations, got %+v", pCtx.Observations)
	}
	lastObservation := pCtx.Observations[len(pCtx.Observations)-1]
	if lastObservation.Kind != models.ObservationKindEnrichment || lastObservation.Source != "ip_enricher" {
		t.Fatalf("expected ip_enricher observation to be recorded, got %+v", lastObservation)
	}
	if lastObservation.OwnershipState != models.OwnershipStateUncertain {
		t.Fatalf("expected PTR mismatch to be recorded as uncertain, got %+v", lastObservation)
	}
	if pCtx.Assets[0].OwnershipState != models.OwnershipStateUncertain {
		t.Fatalf("expected canonical IP ownership to remain uncertain, got %+v", pCtx.Assets[0])
	}
}

func TestIPEnricher_RetriesRetryableCanonicalIPsOnLaterPass(t *testing.T) {
	collector := NewIPEnricher()
	collector.judge = nil
	calls := 0
	collector.enrichAsset = func(asset *models.Asset) {
		calls++
		if asset.EnrichmentStates == nil {
			asset.EnrichmentStates = make(map[string]models.EnrichmentState)
		}
		if calls == 1 {
			asset.EnrichmentStates["ip_enricher"] = models.EnrichmentState{Status: "retryable", Error: "temporary timeout"}
			return
		}
		if asset.IPDetails == nil {
			asset.IPDetails = &models.IPDetails{}
		}
		asset.IPDetails.ASN = 13335
		asset.EnrichmentStates["ip_enricher"] = models.EnrichmentState{Status: "completed"}
	}

	pCtx := &models.PipelineContext{
		Seeds: []models.Seed{
			{ID: "seed-1", CompanyName: "Example Corp", Domains: []string{"example.com"}},
		},
		Enumerations: []models.Enumeration{
			{ID: "enum-1", SeedID: "seed-1", Status: "running"},
		},
		Assets: []models.Asset{
			{
				ID:            "ip-1",
				EnumerationID: "enum-1",
				Type:          models.AssetTypeIP,
				Identifier:    "203.0.113.10",
			},
		},
	}

	if _, err := collector.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected first pass to succeed, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected one initial enrichment attempt, got %d", calls)
	}
	if got := pCtx.Assets[0].EnrichmentStates["ip_enricher"].Status; got != "retryable" {
		t.Fatalf("expected retryable state after first pass, got %+v", pCtx.Assets[0].EnrichmentStates)
	}

	if _, err := collector.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected second pass to succeed, got %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected retryable IP to be enriched again, got %d", calls)
	}
	if got := pCtx.Assets[0].EnrichmentStates["ip_enricher"].Status; got != "completed" {
		t.Fatalf("expected completed state after retry succeeds, got %+v", pCtx.Assets[0].EnrichmentStates)
	}
	if pCtx.Assets[0].IPDetails == nil || pCtx.Assets[0].IPDetails.ASN != 13335 {
		t.Fatalf("expected second pass to backfill ASN, got %+v", pCtx.Assets[0])
	}
}

func seedHasDomain(seeds []models.Seed, domain string) bool {
	for _, seed := range seeds {
		for _, candidate := range seed.Domains {
			if candidate == domain {
				return true
			}
		}
	}
	return false
}

func TestCymruOriginQueryDomainIPv4(t *testing.T) {
	query, ok := cymruOriginQueryDomain(net.ParseIP("104.21.52.57"))
	if !ok {
		t.Fatal("expected IPv4 query builder to succeed")
	}

	want := "57.52.21.104.origin.asn.cymru.com"
	if query != want {
		t.Fatalf("expected %q, got %q", want, query)
	}
}

func TestCymruOriginQueryDomainIPv6(t *testing.T) {
	query, ok := cymruOriginQueryDomain(net.ParseIP("2606:4700:3031::ac43:ad26"))
	if !ok {
		t.Fatal("expected IPv6 query builder to succeed")
	}

	want := "6.2.d.a.3.4.c.a.0.0.0.0.0.0.0.0.0.0.0.0.1.3.0.3.0.0.7.4.6.0.6.2.origin6.asn.cymru.com"
	if query != want {
		t.Fatalf("expected %q, got %q", want, query)
	}
}
