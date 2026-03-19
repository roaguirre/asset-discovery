package nodes

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"asset-discovery/internal/models"
	"asset-discovery/internal/registration"
)

// RDAPCollector queries the global RDAP bootstrap (rdap.org) to extract granular domain metadata.
type RDAPCollector struct {
	client *http.Client
}

func NewRDAPCollector() *RDAPCollector {
	return &RDAPCollector{
		// RDAP.org issues redirects to authoritative servers which can be slow, 30s timeout is safe.
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *RDAPCollector) Process(ctx context.Context, pCtx *models.PipelineContext) (*models.PipelineContext, error) {
	log.Println("[RDAP Collector] Processing seeds...")

	var newEnums []models.Enumeration
	var newErrors []error
	var newAssets []models.Asset

	for _, seed := range pCtx.CollectionSeeds() {
		enum := models.Enumeration{
			ID:        fmt.Sprintf("enum-rdap-%d", time.Now().UnixNano()),
			SeedID:    seed.ID,
			Status:    "running",
			CreatedAt: time.Now(),
			StartedAt: time.Now(),
		}
		newEnums = append(newEnums, enum)

		log.Printf("[RDAP Collector] Querying RDAP.org for seed: %s", seed.CompanyName)
		for _, baseDomain := range seed.Domains {
			rdapData, err := registration.LookupDomain(ctx, c.client, baseDomain)
			if err != nil {
				if err == registration.ErrUnsupportedRegistrationData {
					continue
				}
				log.Printf("[RDAP Collector] Registration lookup failed for %s: %v", baseDomain, err)
				newErrors = append(newErrors, err)
				continue
			}

			if rdapData == nil {
				continue
			}

			// Add the RDAP domain enrichment asset
			newAssets = append(newAssets, models.Asset{
				ID:            fmt.Sprintf("dom-rdap-%d", time.Now().UnixNano()),
				EnumerationID: enum.ID,
				Type:          models.AssetTypeDomain,
				Identifier:    baseDomain,
				Source:        "rdap_collector",
				DiscoveryDate: time.Now(),
				DomainDetails: &models.DomainDetails{
					RDAP: rdapData,
				},
			})
		}
	}

	pCtx.Lock()
	pCtx.Enumerations = append(pCtx.Enumerations, newEnums...)
	pCtx.Errors = append(pCtx.Errors, newErrors...)
	pCtx.Assets = append(pCtx.Assets, newAssets...)
	pCtx.Unlock()

	return pCtx, nil
}
