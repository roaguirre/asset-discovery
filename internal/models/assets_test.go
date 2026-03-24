package models

import "testing"

func TestPipelineContext_AppendAssetsPreservesUncertainOwnershipOverAssociatedInfrastructure(t *testing.T) {
	associated := Asset{
		ID:              "ip-associated",
		EnumerationID:   "enum-1",
		Type:            AssetTypeIP,
		Identifier:      "203.0.113.10",
		Source:          "domain_enricher",
		OwnershipState:  OwnershipStateAssociatedInfrastructure,
		InclusionReason: "Observed as infrastructure supporting an in-scope domain",
	}
	uncertain := Asset{
		ID:              "ip-uncertain",
		EnumerationID:   "enum-1",
		Type:            AssetTypeIP,
		Identifier:      "203.0.113.10",
		Source:          "ip_enricher",
		OwnershipState:  OwnershipStateUncertain,
		InclusionReason: "Observed behind an in-scope domain, but PTR points to vps.example.net",
	}

	for _, testCase := range []struct {
		name   string
		assets []Asset
	}{
		{
			name:   "associated then uncertain",
			assets: []Asset{associated, uncertain},
		},
		{
			name:   "uncertain then associated",
			assets: []Asset{uncertain, associated},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			pCtx := &PipelineContext{}
			pCtx.AppendAssets(testCase.assets...)

			if len(pCtx.Assets) != 1 {
				t.Fatalf("expected one canonical asset, got %d", len(pCtx.Assets))
			}

			got := pCtx.Assets[0]
			if got.OwnershipState != OwnershipStateUncertain {
				t.Fatalf("expected uncertain ownership to win, got %+v", got)
			}
			if got.InclusionReason != uncertain.InclusionReason {
				t.Fatalf("expected uncertain inclusion reason to be preserved, got %+v", got)
			}
		})
	}
}

func TestDomainResolutionStatusForAsset(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		asset Asset
		want  DomainResolutionStatus
	}{
		{
			name: "resolved when records exist",
			asset: Asset{
				Type: AssetTypeDomain,
				DomainDetails: &DomainDetails{
					Records: []DNSRecord{{Type: "A", Value: "203.0.113.10"}},
				},
			},
			want: DomainResolutionStatusResolved,
		},
		{
			name: "unresolved when enrichment completed without records",
			asset: Asset{
				Type: AssetTypeDomain,
				EnrichmentStates: map[string]EnrichmentState{
					"domain_enricher": {Status: "completed"},
				},
			},
			want: DomainResolutionStatusUnresolved,
		},
		{
			name: "lookup failed when enrichment failed",
			asset: Asset{
				Type: AssetTypeDomain,
				EnrichmentStates: map[string]EnrichmentState{
					"domain_enricher": {Status: "failed"},
				},
			},
			want: DomainResolutionStatusLookupFailed,
		},
		{
			name: "not checked when no enrichment state exists",
			asset: Asset{
				Type: AssetTypeDomain,
			},
			want: DomainResolutionStatusNotChecked,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := DomainResolutionStatusForAsset(testCase.asset); got != testCase.want {
				t.Fatalf("expected %q, got %q", testCase.want, got)
			}
		})
	}
}

func TestPipelineContext_AppendAssetRelationsResolvesCanonicalAssetIDs(t *testing.T) {
	pCtx := &PipelineContext{}
	pCtx.AppendAssets(
		Asset{
			ID:            "raw-dom-1",
			EnumerationID: "enum-1",
			Type:          AssetTypeDomain,
			Identifier:    "example.com",
			Source:        "crt.sh",
		},
		Asset{
			ID:            "raw-dom-2",
			EnumerationID: "enum-2",
			Type:          AssetTypeDomain,
			Identifier:    "example.com",
			Source:        "wayback_collector",
		},
		Asset{
			ID:            "raw-ip-1",
			EnumerationID: "enum-1",
			Type:          AssetTypeIP,
			Identifier:    "203.0.113.10",
			Source:        "dns_collector",
		},
	)

	if len(pCtx.Assets) != 2 {
		t.Fatalf("expected canonical assets to be deduped, got %+v", pCtx.Assets)
	}

	domainAssetID := ""
	ipAssetID := ""
	for _, asset := range pCtx.Assets {
		switch {
		case asset.Type == AssetTypeDomain && asset.Identifier == "example.com":
			domainAssetID = asset.ID
		case asset.Type == AssetTypeIP && asset.Identifier == "203.0.113.10":
			ipAssetID = asset.ID
		}
	}
	if domainAssetID == "" || ipAssetID == "" {
		t.Fatalf("expected canonical asset IDs to be available, got %+v", pCtx.Assets)
	}

	pCtx.AppendAssetRelations(AssetRelation{
		ID:             "rel-1",
		FromAssetID:    "raw-dom-2",
		FromAssetType:  AssetTypeDomain,
		FromIdentifier: "example.com",
		ToAssetID:      "raw-ip-1",
		ToAssetType:    AssetTypeIP,
		ToIdentifier:   "203.0.113.10",
		Source:         "dns_collector",
		Kind:           "dns_a",
	})

	if len(pCtx.Relations) != 1 {
		t.Fatalf("expected one canonical relation, got %+v", pCtx.Relations)
	}
	if got := pCtx.Relations[0].FromAssetID; got != domainAssetID {
		t.Fatalf("expected from_asset_id %q, got %+v", domainAssetID, pCtx.Relations[0])
	}
	if got := pCtx.Relations[0].ToAssetID; got != ipAssetID {
		t.Fatalf("expected to_asset_id %q, got %+v", ipAssetID, pCtx.Relations[0])
	}
}
