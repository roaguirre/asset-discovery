package nodes

import (
	"context"
	"fmt"
	"log"
	"net"
	"time"

	"asset-discovery/internal/models"
)

// DNSCollector resolves seed domains to discover IP assets.
// It acts as a real collector using the standard net package.
type DNSCollector struct{}

func NewDNSCollector() *DNSCollector {
	return &DNSCollector{}
}

func (c *DNSCollector) Process(ctx context.Context, pCtx *models.PipelineContext) (*models.PipelineContext, error) {
	log.Println("[DNS Collector] Processing seeds...")

	var newEnums []models.Enumeration
	var newErrors []error
	var newAssets []models.Asset

	for _, seed := range pCtx.CollectionSeeds() {
		enum := models.Enumeration{
			ID:        fmt.Sprintf("enum-dns-%d", time.Now().UnixNano()),
			SeedID:    seed.ID,
			Status:    "running",
			CreatedAt: time.Now(),
			StartedAt: time.Now(),
		}
		newEnums = append(newEnums, enum)

		log.Printf("[DNS Collector] Resolving domains for seed: %s", seed.CompanyName)
		for _, baseDomain := range seed.Domains {

			// 1. Resolve IPs (A/AAAA)
			ips, err := net.LookupIP(baseDomain)
			if err != nil {
				log.Printf("[DNS Collector] Failed to lookup IP for %s: %v", baseDomain, err)
				newErrors = append(newErrors, fmt.Errorf("lookup IP %s: %w", baseDomain, err))
				continue
			}

			// Add the Domain Asset
			domainAsset := models.Asset{
				ID:            fmt.Sprintf("dom-%d", time.Now().UnixNano()),
				EnumerationID: enum.ID,
				Type:          models.AssetTypeDomain,
				Identifier:    baseDomain,
				Source:        "dns_collector",
				DiscoveryDate: time.Now(),
				DomainDetails: &models.DomainDetails{},
			}

			// Add IPs as records to the Domain Asset, and ALSO as distinct IP Assets
			for _, ip := range ips {
				recordType := "A"
				if ip.To4() == nil {
					recordType = "AAAA"
				}

				domainAsset.DomainDetails.Records = append(domainAsset.DomainDetails.Records, models.DNSRecord{
					Type:  recordType,
					Value: ip.String(),
				})

				// Register the distinct IP Asset
				newAssets = append(newAssets, models.Asset{
					ID:            fmt.Sprintf("ip-%d", time.Now().UnixNano()),
					EnumerationID: enum.ID,
					Type:          models.AssetTypeIP,
					Identifier:    ip.String(),
					Source:        "dns_collector",
					DiscoveryDate: time.Now(),
					IPDetails:     &models.IPDetails{},
				})
			}

			// 2. Lookup MX Records
			mxs, err := net.LookupMX(baseDomain)
			if err == nil {
				for _, mx := range mxs {
					domainAsset.DomainDetails.Records = append(domainAsset.DomainDetails.Records, models.DNSRecord{
						Type:  "MX",
						Value: mx.Host,
					})
				}
			}

			// 3. Lookup TXT Records
			txts, err := net.LookupTXT(baseDomain)
			if err == nil {
				for _, txt := range txts {
					domainAsset.DomainDetails.Records = append(domainAsset.DomainDetails.Records, models.DNSRecord{
						Type:  "TXT",
						Value: txt,
					})
				}
			}

			// 4. Lookup NS Records
			nss, err := net.LookupNS(baseDomain)
			if err == nil {
				for _, ns := range nss {
					domainAsset.DomainDetails.Records = append(domainAsset.DomainDetails.Records, models.DNSRecord{
						Type:  "NS",
						Value: ns.Host,
					})
				}
			}

			// Finally append the rich domain asset
			newAssets = append(newAssets, domainAsset)
		}
	}

	// Safely merge results back into the shared context
	pCtx.Lock()
	pCtx.Enumerations = append(pCtx.Enumerations, newEnums...)
	pCtx.Errors = append(pCtx.Errors, newErrors...)
	pCtx.Assets = append(pCtx.Assets, newAssets...)
	pCtx.Unlock()

	return pCtx, nil
}
