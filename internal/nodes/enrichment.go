package nodes

import (
	"context"
	"log"

	"asset-discovery/internal/models"
)

// DNSResolverEnricher provides foundational enrichment by ensuring we have basic metadata
type DNSResolverEnricher struct{}

func NewDNSResolverEnricher() *DNSResolverEnricher {
	return &DNSResolverEnricher{}
}

func (e *DNSResolverEnricher) Process(ctx context.Context, pCtx *models.PipelineContext) (*models.PipelineContext, error) {
	log.Println("[Enricher] Checking assets for missing enrichment...")

	for i := range pCtx.Assets {
		// Initialize the EnrichmentData map if it's nil
		if pCtx.Assets[i].EnrichmentData == nil {
			pCtx.Assets[i].EnrichmentData = make(map[string]interface{})
		}

		// Placeholder for real enrichment logic (e.g. parallel Port Scanning, HTTP title grabbing)
		// For now, we just ensure the map is ready for future workers.
		pCtx.Assets[i].EnrichmentData["enriched"] = true
	}

	return pCtx, nil
}
