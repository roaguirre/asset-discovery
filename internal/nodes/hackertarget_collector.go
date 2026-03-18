package nodes

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"asset-discovery/internal/models"
)

// HackerTargetCollector queries api.hackertarget.com for passive subdomains.
type HackerTargetCollector struct {
	client *http.Client
}

func NewHackerTargetCollector() *HackerTargetCollector {
	return &HackerTargetCollector{
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *HackerTargetCollector) Process(ctx context.Context, pCtx *models.PipelineContext) (*models.PipelineContext, error) {
	log.Println("[HackerTarget Collector] Processing seeds...")

	var newEnums []models.Enumeration
	var newErrors []error
	var newAssets []models.Asset

	for _, seed := range pCtx.CollectionSeeds() {
		enum := models.Enumeration{
			ID:        fmt.Sprintf("enum-ht-%d", time.Now().UnixNano()),
			SeedID:    seed.ID,
			Status:    "running",
			CreatedAt: time.Now(),
			StartedAt: time.Now(),
		}
		newEnums = append(newEnums, enum)

		for _, baseDomain := range seed.Domains {
			log.Printf("[HackerTarget Collector] Querying subdomains for: %s", baseDomain)

			// HackerTarget Host Search API (Forward/Reverse DNS mapping)
			url := fmt.Sprintf("https://api.hackertarget.com/hostsearch/?q=%s", baseDomain)

			req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
			if err != nil {
				newErrors = append(newErrors, err)
				continue
			}

			// Add a slight delay to respect public rate limits before sending (if scaling up)
			// time.Sleep(500 * time.Millisecond)

			resp, err := c.client.Do(req)
			if err != nil {
				newErrors = append(newErrors, err)
				continue
			}

			if resp.StatusCode != http.StatusOK {
				resp.Body.Close()
				newErrors = append(newErrors, fmt.Errorf("unexpected status %d from hackertarget for %s", resp.StatusCode, baseDomain))
				continue
			}

			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				newErrors = append(newErrors, err)
				continue
			}

			// HackerTarget returns CSV like: subdomain.domain.com,1.2.3.4
			lines := strings.Split(string(body), "\n")
			for _, line := range lines {
				line = strings.TrimSpace(line)
				if line == "" || strings.HasPrefix(line, "error ") {
					continue
				}

				parts := strings.SplitN(line, ",", 2)
				if len(parts) >= 1 {
					subdomain := parts[0]

					// Add Domain Asset
					newAssets = append(newAssets, models.Asset{
						ID:            fmt.Sprintf("dom-ht-%d", time.Now().UnixNano()),
						EnumerationID: enum.ID,
						Type:          models.AssetTypeDomain,
						Identifier:    subdomain,
						Source:        "hackertarget_collector",
						DiscoveryDate: time.Now(),
						DomainDetails: &models.DomainDetails{},
					})

					// If IP is present, add IP Asset too
					if len(parts) == 2 && parts[1] != "" {
						ip := parts[1]
						newAssets = append(newAssets, models.Asset{
							ID:            fmt.Sprintf("ip-ht-%d", time.Now().UnixNano()),
							EnumerationID: enum.ID,
							Type:          models.AssetTypeIP,
							Identifier:    ip,
							Source:        "hackertarget_collector",
							DiscoveryDate: time.Now(),
							IPDetails:     &models.IPDetails{},
						})
					}
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
