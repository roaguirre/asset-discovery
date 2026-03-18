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

	"github.com/likexian/whois"
	whoisparser "github.com/likexian/whois-parser"
)

// rdapResponse represents the relevant fields from an RDAP domain query.
type rdapResponse struct {
	Entities []struct {
		Roles     []string `json:"roles,omitempty"`
		PublicIds []struct {
			Identifier string `json:"identifier"`
		} `json:"publicIds,omitempty"`
		// Note: vcardArray parsing is complex (jCard format), we'll gracefully skip deep extraction for now
		// but we can extract un-redacted emails/names if required later.
	} `json:"entities,omitempty"`
	Events []struct {
		EventAction string `json:"eventAction,omitempty"`
		EventDate   string `json:"eventDate,omitempty"`
	} `json:"events,omitempty"`
	Status      []string `json:"status,omitempty"`
	NameServers []struct {
		LDHName string `json:"ldhName,omitempty"`
	} `json:"nameservers,omitempty"`
}

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
			// Query the generic bootstrap server which redirects to authoritative
			url := fmt.Sprintf("https://rdap.org/domain/%s", baseDomain)

			req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
			if err != nil {
				log.Printf("[RDAP Collector] Failed to build request for %s: %v", baseDomain, err)
				newErrors = append(newErrors, err)
				continue
			}

			req.Header.Set("Accept", "application/rdap+json")
			req.Header.Set("User-Agent", "Asset-Discovery-Bot/1.0")

			resp, err := c.client.Do(req)
			if err != nil {
				log.Printf("[RDAP Collector] Request failed for %s: %v", baseDomain, err)
				newErrors = append(newErrors, err)
				continue
			}

			if resp.StatusCode != http.StatusOK {
				resp.Body.Close()

				if resp.StatusCode == http.StatusNotFound {
					log.Printf("[RDAP/WHOIS Collector] RDAP 404 for %s, falling back to TCP WHOIS...", baseDomain)

					// Perform TCP WHOIS queries
					whoisRaw, err := whois.Whois(baseDomain)
					if err != nil {
						err := fmt.Errorf("WHOIS lookup failed for %s: %w", baseDomain, err)
						log.Printf("[RDAP/WHOIS Collector] %v", err)
						newErrors = append(newErrors, err)
						continue
					}

					// Parse unstructured WHOIS output
					parsed, err := whoisparser.Parse(whoisRaw)
					if err != nil {
						// nic.cl and other TLDs return non-standard WHOIS that breaks the parser.
						// We silently swallow the error so pipeline proceeds cleanly without logging spam.
						continue
					}

					rdapData := &models.RDAPData{}

					if parsed.Domain != nil {
						rdapData.Statuses = parsed.Domain.Status
					}
					if parsed.Registrar != nil {
						rdapData.RegistrarName = parsed.Registrar.Name
						rdapData.RegistrarIANAID = parsed.Registrar.ID
					}

					if parsed.Domain != nil {
						// Manually map dates if they parsed correctly
						if parsed.Domain.CreatedDate != "" {
							if created, err := time.Parse("2006-01-02T15:04:05Z", parsed.Domain.CreatedDate); err == nil {
								rdapData.CreationDate = created
							}
						}
						if parsed.Domain.ExpirationDate != "" {
							if expires, err := time.Parse("2006-01-02T15:04:05Z", parsed.Domain.ExpirationDate); err == nil {
								rdapData.ExpirationDate = expires
							}
						}
						if parsed.Domain.UpdatedDate != "" {
							if updated, err := time.Parse("2006-01-02T15:04:05Z", parsed.Domain.UpdatedDate); err == nil {
								rdapData.UpdatedDate = updated
							}
						}

						for _, ns := range parsed.Domain.NameServers {
							rdapData.NameServers = append(rdapData.NameServers, ns)
						}
					}

					newAssets = append(newAssets, models.Asset{
						ID:            fmt.Sprintf("dom-rdap-%d", time.Now().UnixNano()),
						EnumerationID: enum.ID,
						Type:          models.AssetTypeDomain,
						Identifier:    baseDomain,
						Source:        "whois_collector",
						DiscoveryDate: time.Now(),
						DomainDetails: &models.DomainDetails{
							RDAP: rdapData,
						},
					})
					continue
				}

				err := fmt.Errorf("unexpected status code %d from RDAP for %s", resp.StatusCode, baseDomain)
				log.Printf("[RDAP/WHOIS Collector] %v", err)
				newErrors = append(newErrors, err)
				continue
			}

			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()

			if err != nil {
				log.Printf("[RDAP/WHOIS Collector] Failed to read RDAP response for %s: %v", baseDomain, err)
				newErrors = append(newErrors, err)
				continue
			}

			var rdapResp rdapResponse
			if err := json.Unmarshal(body, &rdapResp); err != nil {
				log.Printf("[RDAP/WHOIS Collector] Failed to parse JSON for %s: %v", baseDomain, err)
				newErrors = append(newErrors, err)
				continue
			}

			rdapData := &models.RDAPData{
				Statuses: rdapResp.Status,
			}

			// Map Nameservers
			for _, ns := range rdapResp.NameServers {
				if ns.LDHName != "" {
					rdapData.NameServers = append(rdapData.NameServers, ns.LDHName)
				}
			}

			// Map Events
			for _, ev := range rdapResp.Events {
				parsedTime, err := time.Parse(time.RFC3339, ev.EventDate)
				if err != nil {
					continue
				}
				switch ev.EventAction {
				case "registration":
					rdapData.CreationDate = parsedTime
				case "expiration":
					rdapData.ExpirationDate = parsedTime
				case "last changed":
					rdapData.UpdatedDate = parsedTime
				}
			}

			// Map Registrar Data securely dodging missing entities
			for _, ent := range rdapResp.Entities {
				isRegistrar := false
				for _, role := range ent.Roles {
					if role == "registrar" {
						isRegistrar = true
						break
					}
				}
				if isRegistrar && len(ent.PublicIds) > 0 {
					rdapData.RegistrarIANAID = ent.PublicIds[0].Identifier
				}
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
