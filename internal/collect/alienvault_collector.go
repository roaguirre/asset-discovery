package collect

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"asset-discovery/internal/fetchutil"
	"asset-discovery/internal/models"
	"asset-discovery/internal/tracing/telemetry"
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

type AlienVaultCollectorOption func(*AlienVaultCollector)

func WithAlienVaultClient(client *http.Client) AlienVaultCollectorOption {
	return func(c *AlienVaultCollector) {
		if client != nil {
			c.client = client
		}
	}
}

func NewAlienVaultCollector(options ...AlienVaultCollectorOption) *AlienVaultCollector {
	collector := &AlienVaultCollector{
		client: &http.Client{Timeout: 30 * time.Second},
	}

	for _, option := range options {
		if option != nil {
			option(collector)
		}
	}

	return collector
}

func (c *AlienVaultCollector) Process(ctx context.Context, pCtx *models.PipelineContext) (*models.PipelineContext, error) {
	telemetry.Info(ctx, "[AlienVault Collector] Processing seeds...")

	var newEnums []models.Enumeration
	var newErrors []error
	var newAssets []models.Asset

	for _, seed := range pCtx.CollectionSeeds() {
		enum := models.Enumeration{
			ID:        models.NewID("enum-otx"),
			SeedID:    seed.ID,
			Status:    "running",
			CreatedAt: time.Now(),
			StartedAt: time.Now(),
		}
		newEnums = append(newEnums, enum)

		for _, baseDomain := range seed.Domains {
			telemetry.Infof(ctx, "[AlienVault Collector] Querying OTX for: %s", baseDomain)

			// AlienVault API Indicator Endpoint
			url := fmt.Sprintf("https://otx.alienvault.com/api/v1/indicators/domain/%s/passive_dns", baseDomain)

			resp, err := fetchutil.DoRequest(ctx, c.client, func(ctx context.Context) (*http.Request, error) {
				retryReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
				if err != nil {
					return nil, err
				}
				retryReq.Header.Set("User-Agent", "Asset-Discovery-Bot/1.0")
				return retryReq, nil
			})
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
						ID:            models.NewID("dom-otx"),
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
