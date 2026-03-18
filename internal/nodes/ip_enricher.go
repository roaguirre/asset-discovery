package nodes

import (
	"context"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"asset-discovery/internal/models"
	"golang.org/x/net/publicsuffix"
)

// IPEnricher performs fast Reverse DNS (PTR) and ASN/Organization lookups
// using the Team Cymru DNS service.
type IPEnricher struct{}

func NewIPEnricher() *IPEnricher {
	return &IPEnricher{}
}

func (e *IPEnricher) Process(ctx context.Context, pCtx *models.PipelineContext) (*models.PipelineContext, error) {
	log.Println("[IP Enricher] Starting IP enrichment process...")

	// Extract only IP assets from the pipeline context for enrichment
	var ipAssets []*models.Asset
	pCtx.Lock()
	for i := range pCtx.Assets {
		if pCtx.Assets[i].Type == models.AssetTypeIP {
			// We work with pointers to directly mutate the asset within the context
			ipAssets = append(ipAssets, &pCtx.Assets[i])
		}
	}
	pCtx.Unlock()

	if len(ipAssets) == 0 {
		log.Println("[IP Enricher] No IP assets found to enrich.")
		return pCtx, nil
	}

	log.Printf("[IP Enricher] Enriching %d IP assets concurrently...", len(ipAssets))

	var wg sync.WaitGroup
	// Limit concurrency to avoid overwhelming local DNS resolvers
	concurrencyLimit := 50
	sem := make(chan struct{}, concurrencyLimit)

	var newPTRsMu sync.Mutex
	var newPTRs []string

	for _, asset := range ipAssets {
		wg.Add(1)
		sem <- struct{}{} // Acquire semaphore

		go func(a *models.Asset) {
			defer wg.Done()
			defer func() { <-sem }() // Release semaphore

			enrichIP(a)

			// Safely track newly discovered PTRs for recursion re-seeding
			if a.IPDetails != nil && a.IPDetails.PTR != "" {
				newPTRsMu.Lock()
				newPTRs = append(newPTRs, a.IPDetails.PTR)
				newPTRsMu.Unlock()
			}
		}(asset)
	}

	wg.Wait()
	log.Println("[IP Enricher] Finished enriching all IPs.")

	// Hand newly discovered domains back to the engine scheduler as the next collection frontier.
	for _, ptr := range newPTRs {
		targets := []string{ptr}

		// Also target the registrable root domain if different (to get RDAP/Subdomains)
		if root, err := publicsuffix.EffectiveTLDPlusOne(ptr); err == nil && root != ptr {
			targets = append(targets, root)
		}

		for _, target := range targets {
			newSeed := models.Seed{
				ID:          fmt.Sprintf("seed-ptr-%d", time.Now().UnixNano()),
				CompanyName: target,
				Domains:     []string{target},
				Tags:        []string{"auto-discovered", "ptr-recursion"},
			}

			if pCtx.EnqueueSeed(newSeed) {
				log.Printf("[IP Enricher] Found novel domain via PTR: %s. Scheduling another collection wave.", target)
			}
		}
	}

	return pCtx, nil
}

func enrichIP(asset *models.Asset) {
	if asset.IPDetails == nil {
		asset.IPDetails = &models.IPDetails{}
	}
	if asset.EnrichmentData == nil {
		asset.EnrichmentData = make(map[string]interface{})
	}

	ipStr := asset.Identifier
	parsedIP := net.ParseIP(ipStr)
	if parsedIP == nil {
		return
	}

	var wgEnrich sync.WaitGroup

	// Task 1: Reverse DNS Lookup (PTR)
	wgEnrich.Add(1)
	go func() {
		defer wgEnrich.Done()
		// Implement a brief timeout for DNS lookups to prevent hanging
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		names, err := net.DefaultResolver.LookupAddr(ctx, ipStr)
		if err == nil && len(names) > 0 {
			// Clean trailing dot if present
			asset.IPDetails.PTR = strings.TrimSuffix(names[0], ".")
		}
	}()

	// Task 2: ASN and Organization Lookup via Team Cymru DNS
	wgEnrich.Add(1)
	go func() {
		defer wgEnrich.Done()

		// Team Cymru expects the IP octets reversed for IPv4: 4.3.2.1.origin.asn.cymru.com
		// Note: IPv6 uses .origin6.asn.cymru.com, but we'll focus on v4 format parsing for now
		if parsedIP.To4() != nil {
			octets := strings.Split(ipStr, ".")
			if len(octets) != 4 {
				return
			}

			queryDomain := fmt.Sprintf("%s.%s.%s.%s.origin.asn.cymru.com", octets[3], octets[2], octets[1], octets[0])

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			txtRecords, err := net.DefaultResolver.LookupTXT(ctx, queryDomain)
			if err == nil && len(txtRecords) > 0 {
				// Format: "ASN | CIDR | CC | Registry | Allocated"
				// e.g., "15169 | 108.177.123.0/24 | US | arin | 2013-05-23"
				record := txtRecords[0]
				parts := strings.Split(record, "|")
				if len(parts) >= 1 {
					asnStr := strings.TrimSpace(parts[0])
					if asnID, err := strconv.Atoi(asnStr); err == nil {
						asset.IPDetails.ASN = asnID

						// Now query the organization name using the ASN: AS15169.asn.cymru.com
						orgQuery := fmt.Sprintf("AS%d.asn.cymru.com", asnID)
						orgRecords, orgErr := net.DefaultResolver.LookupTXT(ctx, orgQuery)
						if orgErr == nil && len(orgRecords) > 0 {
							// Format: "15169 | US | arin | 2000-03-30 | GOOGLE, US"
							orgParts := strings.Split(orgRecords[0], "|")
							if len(orgParts) >= 5 {
								asset.IPDetails.Organization = strings.TrimSpace(orgParts[4])
							}
						}
					}
				}

				// Optional: Grab CIDR routing info
				if len(parts) >= 2 {
					asset.EnrichmentData["cidr"] = strings.TrimSpace(parts[1])
				}
			}
		}
	}()

	wgEnrich.Wait()
	asset.EnrichmentData["enriched"] = true
}
