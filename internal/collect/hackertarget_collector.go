package collect

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"asset-discovery/internal/discovery"
	"asset-discovery/internal/fetchutil"
	"asset-discovery/internal/models"
	"asset-discovery/internal/tracing/telemetry"
)

// HackerTargetCollector queries api.hackertarget.com for passive subdomains.
type HackerTargetCollector struct {
	client *http.Client
}

type HackerTargetCollectorOption func(*HackerTargetCollector)

func WithHackerTargetClient(client *http.Client) HackerTargetCollectorOption {
	return func(c *HackerTargetCollector) {
		if client != nil {
			c.client = client
		}
	}
}

func NewHackerTargetCollector(options ...HackerTargetCollectorOption) *HackerTargetCollector {
	collector := &HackerTargetCollector{
		client: &http.Client{Timeout: 30 * time.Second},
	}

	for _, option := range options {
		if option != nil {
			option(collector)
		}
	}

	return collector
}

func (c *HackerTargetCollector) Process(ctx context.Context, pCtx *models.PipelineContext) (*models.PipelineContext, error) {
	telemetry.Info(ctx, "[HackerTarget Collector] Processing seeds...")

	var newEnums []models.Enumeration
	var newErrors []error
	var newAssets []models.Asset

	for _, seed := range pCtx.CollectionSeeds() {
		enum := models.Enumeration{
			ID:        models.NewID("enum-ht"),
			SeedID:    seed.ID,
			Status:    "running",
			CreatedAt: time.Now(),
			StartedAt: time.Now(),
		}
		newEnums = append(newEnums, enum)

		for _, baseDomain := range seed.Domains {
			telemetry.Infof(ctx, "[HackerTarget Collector] Querying subdomains for: %s", baseDomain)

			// HackerTarget Host Search API (Forward/Reverse DNS mapping)
			url := fmt.Sprintf("https://api.hackertarget.com/hostsearch/?q=%s", baseDomain)

			// Add a slight delay to respect public rate limits before sending (if scaling up)
			// time.Sleep(500 * time.Millisecond)

			resp, err := fetchutil.DoRequest(ctx, c.client, func(ctx context.Context) (*http.Request, error) {
				return http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			})
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

			if err := hackertargetBodyError(string(body)); err != nil {
				newErrors = append(newErrors, fmt.Errorf("hackertarget for %s: %w", baseDomain, err))
				continue
			}

			// HackerTarget returns CSV like: subdomain.domain.com,1.2.3.4
			lines := strings.Split(string(body), "\n")
			for _, line := range lines {
				line = strings.TrimSpace(line)
				if line == "" || strings.HasPrefix(strings.ToLower(line), "error ") {
					continue
				}

				parts := strings.SplitN(line, ",", 2)
				if len(parts) != 2 {
					continue
				}

				subdomain := discovery.NormalizeDomainIdentifier(parts[0])
				if !isAcceptedDomainIdentifier(subdomain) {
					continue
				}

				newAssets = append(newAssets, models.Asset{
					ID:            models.NewID("dom-ht"),
					EnumerationID: enum.ID,
					Type:          models.AssetTypeDomain,
					Identifier:    subdomain,
					Source:        "hackertarget_collector",
					DiscoveryDate: time.Now(),
					DomainDetails: &models.DomainDetails{},
				})

				ip := strings.TrimSpace(parts[1])
				if parsedIP := net.ParseIP(ip); parsedIP != nil {
					newAssets = append(newAssets, models.Asset{
						ID:            models.NewID("ip-ht"),
						EnumerationID: enum.ID,
						Type:          models.AssetTypeIP,
						Identifier:    parsedIP.String(),
						Source:        "hackertarget_collector",
						DiscoveryDate: time.Now(),
						IPDetails:     &models.IPDetails{},
					})
				}
			}
		}
	}

	pCtx.Lock()
	pCtx.Enumerations = append(pCtx.Enumerations, newEnums...)
	pCtx.Errors = append(pCtx.Errors, newErrors...)
	pCtx.Unlock()
	pCtx.AppendAssets(newAssets...)

	return pCtx, nil
}

func hackertargetBodyError(body string) error {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return nil
	}

	lower := strings.ToLower(trimmed)
	switch {
	case strings.HasPrefix(lower, "error "):
		return fmt.Errorf("%s", trimmed)
	case strings.Contains(lower, "api count exceeded"),
		strings.Contains(lower, "increase quota with membership"),
		strings.Contains(lower, "too many requests"):
		return fmt.Errorf("%s", trimmed)
	default:
		return nil
	}
}

func isAcceptedDomainIdentifier(candidate string) bool {
	candidate = discovery.NormalizeDomainIdentifier(candidate)
	if candidate == "" {
		return false
	}

	for _, extracted := range discovery.ExtractDomainCandidates(candidate) {
		if extracted == candidate {
			return true
		}
	}

	return false
}
