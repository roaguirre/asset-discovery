package nodes

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"asset-discovery/internal/fetchutil"
	"asset-discovery/internal/models"
)

// WaybackCollector queries the Internet Archive's CDX server for historical hostnames.
type WaybackCollector struct {
	client *http.Client
}

func NewWaybackCollector() *WaybackCollector {
	return &WaybackCollector{
		// Historical queries can be large, allow 60s
		client: &http.Client{Timeout: 60 * time.Second},
	}
}

func (c *WaybackCollector) Process(ctx context.Context, pCtx *models.PipelineContext) (*models.PipelineContext, error) {
	log.Println("[Wayback Collector] Processing seeds...")

	var newEnums []models.Enumeration
	var newErrors []error
	var newAssets []models.Asset

	for _, seed := range pCtx.CollectionSeeds() {
		enum := models.Enumeration{
			ID:        newNodeID("enum-wayback"),
			SeedID:    seed.ID,
			Status:    "running",
			CreatedAt: time.Now(),
			StartedAt: time.Now(),
		}
		newEnums = append(newEnums, enum)

		for _, baseDomain := range seed.Domains {
			log.Printf("[Wayback Collector] Querying CDX Archive for: %s", baseDomain)

			// We only want unique domains (fl=original restricts output to just the URL), limit to 1000 to avoid locking
			queryURL := fmt.Sprintf("https://web.archive.org/cdx/search/cdx?url=*.%s/*&output=txt&fl=original&collapse=urlkey&limit=5000", baseDomain)

			resp, err := fetchutil.DoRequest(ctx, c.client, func(ctx context.Context) (*http.Request, error) {
				retryReq, err := http.NewRequestWithContext(ctx, http.MethodGet, queryURL, nil)
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
				newErrors = append(newErrors, fmt.Errorf("unexpected status %d from Wayback for %s", resp.StatusCode, baseDomain))
				continue
			}

			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				newErrors = append(newErrors, err)
				continue
			}

			// We need a map to ensure we don't spam duplicate subdomains locally before locking
			uniqueHosts := make(map[string]bool)

			lines := strings.Split(string(body), "\n")
			for _, line := range lines {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}

				// The URL might be strange ("http://foo.bar:80/"), parse purely for hostname
				u, err := url.Parse(line)
				if err != nil {
					continue
				}

				host := u.Hostname()
				if host != "" && !uniqueHosts[host] {
					uniqueHosts[host] = true

					newAssets = append(newAssets, models.Asset{
						ID:            newNodeID("dom-wb"),
						EnumerationID: enum.ID,
						Type:          models.AssetTypeDomain,
						Identifier:    host,
						Source:        "wayback_collector",
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
