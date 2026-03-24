package collect

import (
	"context"
	"net/http"
	"time"

	"asset-discovery/internal/models"
	"asset-discovery/internal/registration"
	"asset-discovery/internal/tracing/telemetry"
)

// RDAPCollector queries the global RDAP bootstrap (rdap.org) to extract granular domain metadata.
type RDAPCollector struct {
	client *http.Client
}

type RDAPCollectorOption func(*RDAPCollector)

func WithRDAPClient(client *http.Client) RDAPCollectorOption {
	return func(c *RDAPCollector) {
		if client != nil {
			c.client = client
		}
	}
}

func NewRDAPCollector(options ...RDAPCollectorOption) *RDAPCollector {
	collector := &RDAPCollector{
		// RDAP.org issues redirects to authoritative servers which can be slow, 30s timeout is safe.
		client: &http.Client{Timeout: 30 * time.Second},
	}

	for _, option := range options {
		if option != nil {
			option(collector)
		}
	}

	return collector
}

func (c *RDAPCollector) Process(ctx context.Context, pCtx *models.PipelineContext) (*models.PipelineContext, error) {
	telemetry.Info(ctx, "[RDAP Collector] Processing seeds...")

	var newEnums []models.Enumeration
	var newErrors []error
	var newAssets []models.Asset

	for _, seed := range pCtx.CollectionSeeds() {
		enum := models.Enumeration{
			ID:        models.NewID("enum-rdap"),
			SeedID:    seed.ID,
			Status:    "running",
			CreatedAt: time.Now(),
			StartedAt: time.Now(),
		}
		newEnums = append(newEnums, enum)

		telemetry.Infof(ctx, "[RDAP Collector] Querying RDAP.org for seed: %s", seed.CompanyName)
		for _, baseDomain := range seed.Domains {
			rdapData, err := registration.LookupDomain(ctx, c.client, baseDomain)
			if err != nil {
				if err == registration.ErrUnsupportedRegistrationData {
					continue
				}
				telemetry.Infof(ctx, "[RDAP Collector] Registration lookup failed for %s: %v", baseDomain, err)
				newErrors = append(newErrors, err)
				continue
			}

			if rdapData == nil {
				continue
			}

			// Add the RDAP domain enrichment asset
			newAssets = append(newAssets, models.Asset{
				ID:            models.NewID("dom-rdap"),
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
	pCtx.Unlock()
	pCtx.AppendAssets(newAssets...)

	return pCtx, nil
}
