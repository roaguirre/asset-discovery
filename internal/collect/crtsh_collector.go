package collect

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"asset-discovery/internal/fetchutil"
	"asset-discovery/internal/models"
	"asset-discovery/internal/tracing/telemetry"
)

// crtshResponse models the JSON array returned by crt.sh
type crtshResponse struct {
	IssuerCaID     int    `json:"issuer_ca_id"`
	IssuerName     string `json:"issuer_name"`
	CommonName     string `json:"common_name"`
	NameValue      string `json:"name_value"`
	ID             int64  `json:"id"`
	EntryTimestamp string `json:"entry_timestamp"`
	NotBefore      string `json:"not_before"`
	NotAfter       string `json:"not_after"`
	SerialNumber   string `json:"serial_number"`
}

// CrtShCollector queries the crt.sh Certificate Transparency log search API.
type CrtShCollector struct {
	client *http.Client
}

type CrtShCollectorOption func(*CrtShCollector)

func WithCrtShClient(client *http.Client) CrtShCollectorOption {
	return func(c *CrtShCollector) {
		if client != nil {
			c.client = client
		}
	}
}

func NewCrtShCollector(options ...CrtShCollectorOption) *CrtShCollector {
	collector := &CrtShCollector{
		client: &http.Client{Timeout: 60 * time.Second},
	}

	for _, option := range options {
		if option != nil {
			option(collector)
		}
	}

	return collector
}

func (c *CrtShCollector) Process(ctx context.Context, pCtx *models.PipelineContext) (*models.PipelineContext, error) {
	telemetry.Info(ctx, "[crt.sh Collector] Processing seeds...")

	var newEnums []models.Enumeration
	var newErrors []error
	var newAssets []models.Asset

	for _, seed := range pCtx.CollectionSeeds() {
		enum := models.Enumeration{
			ID:        models.NewID("enum-crtsh"),
			SeedID:    seed.ID,
			Status:    "running",
			CreatedAt: time.Now(),
			StartedAt: time.Now(),
		}
		newEnums = append(newEnums, enum)

		telemetry.Infof(ctx, "[crt.sh Collector] Querying CT logs for seed: %s", seed.CompanyName)
		for _, baseDomain := range seed.Domains {
			// Query crt.sh for %baseDomain
			url := fmt.Sprintf("https://crt.sh/?q=%%.%s&output=json", baseDomain)

			resp, err := fetchutil.DoRequest(ctx, c.client, func(ctx context.Context) (*http.Request, error) {
				retryReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
				if err != nil {
					return nil, err
				}
				retryReq.Header.Set("User-Agent", "Asset-Discovery-Bot/1.0")
				return retryReq, nil
			})
			if err != nil {
				telemetry.Infof(ctx, "[crt.sh Collector] Request failed for %s: %v", baseDomain, err)
				newErrors = append(newErrors, err)
				continue
			}

			if resp.StatusCode != http.StatusOK {
				resp.Body.Close()
				err := fmt.Errorf("unexpected status code %d from crt.sh for %s", resp.StatusCode, baseDomain)
				telemetry.Infof(ctx, "[crt.sh Collector] %v", err)
				newErrors = append(newErrors, err)
				continue
			}

			body, err := io.ReadAll(resp.Body)
			resp.Body.Close() // Explicit close inside loop instead of defer

			if err != nil {
				telemetry.Infof(ctx, "[crt.sh Collector] Failed to read response body for %s: %v", baseDomain, err)
				newErrors = append(newErrors, err)
				continue
			}

			var records []crtshResponse
			if err := json.Unmarshal(body, &records); err != nil {
				telemetry.Infof(ctx, "[crt.sh Collector] Failed to parse JSON for %s: %v", baseDomain, err)
				newErrors = append(newErrors, err)
				continue
			}

			// Parse unique domains to avoid massive duplicates (crt.sh returns many certs for same domain)
			foundDomains := make(map[string]bool)
			for _, rec := range records {
				// NameValue can sometimes contain multiple domains separated by newlines
				names := strings.Split(rec.NameValue, "\n")
				for _, name := range names {
					name = strings.TrimSpace(name)
					// Ignore wildcards for actual domain mapping (could be kept as theoretical patterns later)
					if strings.HasPrefix(name, "*.") {
						name = name[2:]
					}

					if name != "" && !foundDomains[name] {
						foundDomains[name] = true

						newAssets = append(newAssets, models.Asset{
							ID:            models.NewID("dom-crtsh"),
							EnumerationID: enum.ID,
							Type:          models.AssetTypeDomain,
							Identifier:    name,
							Source:        "crt.sh",
							DiscoveryDate: time.Now(),
							DomainDetails: &models.DomainDetails{},
						})
					}
				}
			}
			telemetry.Infof(ctx, "[crt.sh Collector] Discovered %d unique domains for %s", len(foundDomains), baseDomain)
		}
	}

	pCtx.Lock()
	pCtx.Enumerations = append(pCtx.Enumerations, newEnums...)
	pCtx.Errors = append(pCtx.Errors, newErrors...)
	pCtx.Unlock()
	pCtx.AppendAssets(newAssets...)

	return pCtx, nil
}
