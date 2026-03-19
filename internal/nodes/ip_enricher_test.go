package nodes

import (
	"context"
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
