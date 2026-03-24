package filter

import (
	"context"
	"fmt"
	"strings"

	"asset-discovery/internal/models"
	"asset-discovery/internal/tracing/telemetry"
)

// MergeFilter validates the canonical runtime asset graph after collectors and enrichers
// have already upserted into the canonical model.
type MergeFilter struct{}

func NewMergeFilter() *MergeFilter {
	return &MergeFilter{}
}

func (f *MergeFilter) Process(ctx context.Context, pCtx *models.PipelineContext) (*models.PipelineContext, error) {
	telemetry.Info(ctx, "[Merge Filter] Deduplicating and merging discovered assets...")
	pCtx.EnsureAssetState()

	duplicateAssets, danglingObservations, danglingRelations := validateCanonicalGraph(pCtx)
	if duplicateAssets > 0 || danglingObservations > 0 || danglingRelations > 0 {
		telemetry.Infof(
			ctx,
			"[Merge Filter] Canonical graph validation found duplicate_assets=%d dangling_observations=%d dangling_relations=%d.",
			duplicateAssets,
			danglingObservations,
			danglingRelations,
		)
	} else {
		telemetry.Infof(ctx, "[Merge Filter] Canonical graph validated: %d observations, %d relations, %d unique assets.", len(pCtx.Observations), len(pCtx.Relations), len(pCtx.Assets))
	}

	return pCtx, nil
}

func validateCanonicalGraph(pCtx *models.PipelineContext) (duplicateAssets int, danglingObservations int, danglingRelations int) {
	assetIDs := make(map[string]struct{}, len(pCtx.Assets))
	assetKeys := make(map[string]string, len(pCtx.Assets))
	validationErrors := make([]error, 0)

	for _, asset := range pCtx.Assets {
		if asset.ID != "" {
			assetIDs[asset.ID] = struct{}{}
		}

		key := canonicalValidationKey(asset.Type, asset.Identifier)
		if key == "" {
			continue
		}
		if existingID, exists := assetKeys[key]; exists && existingID != asset.ID {
			duplicateAssets++
			validationErrors = append(validationErrors, fmt.Errorf("duplicate canonical asset key %s for %s and %s", key, existingID, asset.ID))
			continue
		}
		assetKeys[key] = asset.ID
	}

	for _, observation := range pCtx.Observations {
		if observation.AssetID == "" {
			danglingObservations++
			validationErrors = append(validationErrors, fmt.Errorf("observation %s is missing a canonical asset id", observation.ID))
			continue
		}
		if _, exists := assetIDs[observation.AssetID]; !exists {
			danglingObservations++
			validationErrors = append(validationErrors, fmt.Errorf("observation %s references missing asset %s", observation.ID, observation.AssetID))
		}
	}

	for _, relation := range pCtx.Relations {
		if relation.FromAssetID != "" {
			if _, exists := assetIDs[relation.FromAssetID]; !exists {
				danglingRelations++
				validationErrors = append(validationErrors, fmt.Errorf("relation %s references missing from_asset_id %s", relation.ID, relation.FromAssetID))
			}
		}
		if relation.ToAssetID != "" {
			if _, exists := assetIDs[relation.ToAssetID]; !exists {
				danglingRelations++
				validationErrors = append(validationErrors, fmt.Errorf("relation %s references missing to_asset_id %s", relation.ID, relation.ToAssetID))
			}
		}
	}

	if len(validationErrors) > 0 {
		pCtx.Lock()
		pCtx.Errors = append(pCtx.Errors, validationErrors...)
		pCtx.Unlock()
	}

	return duplicateAssets, danglingObservations, danglingRelations
}

func canonicalValidationKey(assetType models.AssetType, identifier string) string {
	identifier = strings.TrimSpace(strings.ToLower(identifier))
	if assetType == "" || identifier == "" {
		return ""
	}
	return string(assetType) + "|" + identifier
}
