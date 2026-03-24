package filter

import (
	"context"
	"strings"
	"testing"

	"asset-discovery/internal/models"
)

func TestMergeFilter_ValidatesCanonicalGraphReferences(t *testing.T) {
	pCtx := &models.PipelineContext{
		Assets: []models.Asset{
			{
				ID:         "asset-1",
				Type:       models.AssetTypeDomain,
				Identifier: "example.com",
				Source:     "crt.sh",
			},
		},
	}
	pCtx.EnsureAssetState()
	pCtx.Observations = append(pCtx.Observations, models.AssetObservation{
		ID:         "obs-1",
		AssetID:    "missing-asset",
		Type:       models.AssetTypeDomain,
		Identifier: "example.com",
		Source:     "crt.sh",
	})
	pCtx.Relations = append(pCtx.Relations, models.AssetRelation{
		ID:          "rel-1",
		FromAssetID: "asset-1",
		ToAssetID:   "missing-asset",
		Kind:        "dns_a",
		Source:      "dns_collector",
	})

	filter := NewMergeFilter()
	if _, err := filter.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected merge filter validation to complete, got %v", err)
	}

	if len(pCtx.Errors) < 2 {
		t.Fatalf("expected validation errors for dangling observation and relation, got %+v", pCtx.Errors)
	}

	if !containsErrorSubstring(pCtx.Errors, "observation obs-1 references missing asset missing-asset") {
		t.Fatalf("expected dangling observation error, got %+v", pCtx.Errors)
	}
	if !containsErrorSubstring(pCtx.Errors, "relation rel-1 references missing to_asset_id missing-asset") {
		t.Fatalf("expected dangling relation error, got %+v", pCtx.Errors)
	}
}

func containsErrorSubstring(errs []error, want string) bool {
	for _, err := range errs {
		if err != nil && strings.Contains(err.Error(), want) {
			return true
		}
	}
	return false
}
