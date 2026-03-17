package nodes

import (
	"context"
	"log"
	"strings" // Added for strings.Contains

	"asset-discovery/internal/models"
)

// MergeFilter is responsible for deduplicating assets and merging their multi-source attributes.
type MergeFilter struct{}

func NewMergeFilter() *MergeFilter {
	return &MergeFilter{}
}

func (f *MergeFilter) Process(ctx context.Context, pCtx *models.PipelineContext) (*models.PipelineContext, error) {
	log.Println("[Merge Filter] Deduplicating and merging discovered assets...")

	mergedAssets := make(map[string]*models.Asset)

	for _, a := range pCtx.Assets {
		// Group universally by asset Identifier (e.g. gesprobira.cl or 1.1.1.1)
		assetKey := a.Identifier

		existing, ok := mergedAssets[assetKey]
		if !ok {
			// First time we see this asset string, copy it into the map
			cp := a
			mergedAssets[assetKey] = &cp

			// Initialize empty EnrichmentData map if it's nil so we can merge values later
			if mergedAssets[assetKey].EnrichmentData == nil {
				mergedAssets[assetKey].EnrichmentData = make(map[string]interface{})
			}
			continue
		}

		// --- Deduplication & Merge Logic ---

		// 1. Merge Sources (Concat uniquely if not already present)
		if !strings.Contains(existing.Source, a.Source) {
			existing.Source = existing.Source + ", " + a.Source
		}

		// 2. Merge Domain Details (Records, RDAP)
		if a.DomainDetails != nil {
			if existing.DomainDetails == nil {
				existing.DomainDetails = &models.DomainDetails{}
			}

			// Combine DNS Records
			existing.DomainDetails.Records = append(existing.DomainDetails.Records, a.DomainDetails.Records...)

			// Take RDAP if existing lacks it
			if a.DomainDetails.RDAP != nil && existing.DomainDetails.RDAP == nil {
				existing.DomainDetails.RDAP = a.DomainDetails.RDAP
			}
		}

		// 3. Merge IP Details (ASN, PTR, Org)
		if a.IPDetails != nil {
			if existing.IPDetails == nil {
				existing.IPDetails = &models.IPDetails{}
			}
			if existing.IPDetails.ASN == 0 && a.IPDetails.ASN != 0 {
				existing.IPDetails.ASN = a.IPDetails.ASN
			}
			if existing.IPDetails.Organization == "" && a.IPDetails.Organization != "" {
				existing.IPDetails.Organization = a.IPDetails.Organization
			}
			if existing.IPDetails.PTR == "" && a.IPDetails.PTR != "" {
				existing.IPDetails.PTR = a.IPDetails.PTR
			}
		}

		// 4. Merge Extensible Enrichment Data Maps
		if a.EnrichmentData != nil {
			for k, v := range a.EnrichmentData {
				existing.EnrichmentData[k] = v
			}
		}
	}

	// Flatten the map back into the standard pipeline context slice
	var finalAssets []models.Asset
	for _, a := range mergedAssets {
		finalAssets = append(finalAssets, *a)
	}

	log.Printf("[Merge Filter] Compressed pipeline from %d raw records down to %d unique merged assets.", len(pCtx.Assets), len(finalAssets))
	pCtx.Assets = finalAssets
	return pCtx, nil
}
