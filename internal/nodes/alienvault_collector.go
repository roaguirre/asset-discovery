package nodes

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"asset-discovery/internal/models"
)

// OTXPassiveDNSResponse represents the relevant portion of AlienVault's JSON response.
type OTXPassiveDNSResponse struct {
	PassiveDNS []struct {
		Hostname string `json:"hostname"`
	} `json:"passive_dns"`
}

// AlienVaultCollector queries otx.alienvault.com for passive DNS tracking.
type AlienVaultCollector struct {
	client *http.Client
}

func NewAlienVaultCollector() *AlienVaultCollector {
	return &AlienVaultCollector{
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *AlienVaultCollector) Process(ctx context.Context, pCtx *models.PipelineContext) (*models.PipelineContext, error) {
	log.Println("[AlienVault Collector] Processing seeds...")

	var newEnums []models.Enumeration
	var newErrors []error
	var newAssets []models.Asset

	for _, seed := range pCtx.CollectionSeeds() {
		enum := models.Enumeration{
			ID:        fmt.Sprintf("enum-otx-%d", time.Now().UnixNano()),
			SeedID:    seed.ID,
			Status:    "running",
			CreatedAt: time.Now(),
			StartedAt: time.Now(),
		}
		newEnums = append(newEnums, enum)

		for _, baseDomain := range seed.Domains {
			log.Printf("[AlienVault Collector] Querying OTX for: %s", baseDomain)

			// AlienVault API Indicator Endpoint
			url := fmt.Sprintf("https://otx.alienvault.com/api/v1/indicators/domain/%s/passive_dns", baseDomain)

			req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
			if err != nil {
				newErrors = append(newErrors, err)
				continue
			}

			resp, err := c.client.Do(req)
			if err != nil {
				newErrors = append(newErrors, err)
				continue
			}

			if resp.StatusCode != http.StatusOK {
				resp.Body.Close()
				newErrors = append(newErrors, fmt.Errorf("unexpected status %d from AlienVault for %s", resp.StatusCode, baseDomain))
				continue
			}

			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				newErrors = append(newErrors, err)
				continue
			}

			var otxResp OTXPassiveDNSResponse
			if err := json.Unmarshal(body, &otxResp); err != nil {
				newErrors = append(newErrors, fmt.Errorf("failed to parse OTX JSON for %s: %w", baseDomain, err))
				continue
			}

			for _, record := range otxResp.PassiveDNS {
				if record.Hostname != "" {
					newAssets = append(newAssets, models.Asset{
						ID:            fmt.Sprintf("dom-otx-%d", time.Now().UnixNano()),
						EnumerationID: enum.ID,
						Type:          models.AssetTypeDomain,
						Identifier:    record.Hostname,
						Source:        "alienvault_collector",
						DiscoveryDate: time.Now(),
						DomainDetails: &models.DomainDetails{},
					})
				}
			}
		}
	}

	pCtx.Lock()
	pCtx.Enumerations = append(pCtx.Enumerations, newEnums...)
	pCtx.Errors = append(pCtx.Errors, newErrors...)
	pCtx.Assets = append(pCtx.Assets, newAssets...)
	pCtx.Unlock()

	return pCtx, nil
}
