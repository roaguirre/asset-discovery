package enrich

import (
	"context"
	"encoding/hex"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"asset-discovery/internal/discovery"
	"asset-discovery/internal/models"
	"asset-discovery/internal/ownership"
	"asset-discovery/internal/tracing/lineage"
	"asset-discovery/internal/tracing/telemetry"
)

// IPEnricher performs fast Reverse DNS (PTR) and ASN/Organization lookups
// using the Team Cymru DNS service.
type IPEnricher struct {
	judge       ownership.Judge
	enrichAsset func(*models.Asset)
}

type IPEnricherOption func(*IPEnricher)

func WithIPEnricherJudge(judge ownership.Judge) IPEnricherOption {
	return func(e *IPEnricher) {
		e.judge = judge
	}
}

func NewIPEnricher(options ...IPEnricherOption) *IPEnricher {
	enricher := &IPEnricher{
		judge:       ownership.NewDefaultJudge(),
		enrichAsset: enrichIP,
	}

	for _, option := range options {
		if option != nil {
			option(enricher)
		}
	}

	return enricher
}

func (e *IPEnricher) Process(ctx context.Context, pCtx *models.PipelineContext) (*models.PipelineContext, error) {
	telemetry.Info(ctx, "[IP Enricher] Starting IP enrichment process...")

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
		telemetry.Info(ctx, "[IP Enricher] No IP assets found to enrich.")
		return pCtx, nil
	}

	telemetry.Infof(ctx, "[IP Enricher] Enriching %d IP assets concurrently...", len(ipAssets))

	var wg sync.WaitGroup
	// Limit concurrency to avoid overwhelming local DNS resolvers
	concurrencyLimit := 50
	sem := make(chan struct{}, concurrencyLimit)

	seedByID := make(map[string]models.Seed, len(pCtx.Seeds))
	for _, seed := range pCtx.Seeds {
		seedByID[seed.ID] = seed
	}

	enumToSeed := make(map[string]models.Seed, len(pCtx.Enumerations))
	for _, enum := range pCtx.Enumerations {
		if seed, ok := seedByID[enum.SeedID]; ok {
			enumToSeed[enum.ID] = seed
		}
	}

	type ptrObservation struct {
		root  string
		seed  models.Seed
		hits  int
		hosts []string
	}

	var ptrObservationsMu sync.Mutex
	ptrObservations := make(map[string]*ptrObservation)
	ptrFollowUpSeeds := make(map[string]models.Seed)

	for _, asset := range ipAssets {
		wg.Add(1)
		sem <- struct{}{} // Acquire semaphore

		go func(a *models.Asset) {
			defer wg.Done()
			defer func() { <-sem }() // Release semaphore

			if e.enrichAsset != nil {
				e.enrichAsset(a)
			}

			if a.IPDetails == nil || a.IPDetails.PTR == "" {
				return
			}

			host := discovery.NormalizeDomainIdentifier(a.IPDetails.PTR)
			if host == "" || len(discovery.ExtractDomainCandidates(host)) == 0 {
				return
			}

			ptrObservationsMu.Lock()
			if ptrHostWithinSeedScope(enumToSeed[a.EnumerationID], host) {
				if _, exists := ptrFollowUpSeeds[host]; !exists {
					ptrFollowUpSeeds[host] = buildPTRSeed(enumToSeed[a.EnumerationID], host)
				}
			}

			root := discovery.RegistrableDomain(host)
			if root == "" {
				ptrObservationsMu.Unlock()
				return
			}

			observationKey := a.EnumerationID + "|" + root

			observation, exists := ptrObservations[observationKey]
			if !exists {
				observation = &ptrObservation{
					root: root,
					seed: enumToSeed[a.EnumerationID],
				}
				ptrObservations[observationKey] = observation
			}
			observation.hits++
			if len(observation.hosts) < 3 {
				observation.hosts = append(observation.hosts, a.IPDetails.PTR)
			}
			ptrObservationsMu.Unlock()
		}(asset)
	}

	wg.Wait()
	telemetry.Info(ctx, "[IP Enricher] Finished enriching all IPs.")

	for _, seed := range ptrFollowUpSeeds {
		if pCtx.EnqueueSeed(seed) {
			telemetry.Infof(ctx, "[IP Enricher] Scheduled PTR hostname %s for follow-up collection.", seed.Domains[0])
		}
	}

	if e.judge == nil || len(ptrObservations) == 0 {
		return pCtx, nil
	}

	type judgeGroup struct {
		seed       models.Seed
		candidates []ownership.Candidate
		byRoot     map[string]*ptrObservation
	}

	groups := make(map[string]*judgeGroup)
	for _, observation := range ptrObservations {
		groupKey := observation.seed.ID
		if groupKey == "" {
			groupKey = observation.seed.CompanyName + "|" + strings.Join(observation.seed.Domains, ",")
		}

		group, exists := groups[groupKey]
		if !exists {
			group = &judgeGroup{
				seed:   observation.seed,
				byRoot: make(map[string]*ptrObservation),
			}
			groups[groupKey] = group
		}

		evidence := []ownership.EvidenceItem{
			{
				Kind:    "ptr_root",
				Summary: fmt.Sprintf("Observed %d PTR-derived hits for this registrable domain from discovered IP assets", observation.hits),
			},
		}
		if len(observation.hosts) > 0 {
			evidence = append(evidence, ownership.EvidenceItem{
				Kind:    "ptr_host_samples",
				Summary: fmt.Sprintf("Sample PTR hostnames: %s", strings.Join(observation.hosts, ", ")),
			})
		}

		group.candidates = append(group.candidates, ownership.Candidate{
			Root:     observation.root,
			Evidence: evidence,
		})
		group.byRoot[observation.root] = observation
	}

	for _, group := range groups {
		request := ownership.Request{
			Scenario:   "reverse DNS pivot",
			Seed:       group.seed,
			Candidates: group.candidates,
		}
		decisions, err := e.judge.EvaluateCandidates(ctx, request)
		if err != nil {
			pCtx.Lock()
			pCtx.Errors = append(pCtx.Errors, err)
			pCtx.Unlock()
			continue
		}
		lineage.RecordOwnershipJudgeEvaluation(pCtx, "ip_enricher", request, decisions)

		for _, decision := range decisions {
			if !decision.Collect {
				continue
			}
			if !ownership.IsHighConfidence(decision.Confidence) {
				telemetry.Infof(ctx, "[IP Enricher] Skipping PTR-derived registrable domain %s due to low-confidence judge decision %.2f.", decision.Root, decision.Confidence)
				continue
			}

			observation, exists := group.byRoot[decision.Root]
			if !exists {
				continue
			}

			seed := discovery.BuildDiscoveredSeed(observation.seed, decision.Root, "ptr-recursion")
			if seed.CompanyName == "" {
				seed.CompanyName = decision.Root
			}

			if pCtx.EnqueueSeedCandidate(seed, models.SeedEvidence{
				Source:     "ownership_judge",
				Kind:       decision.Kind,
				Value:      decision.Root,
				Confidence: decision.Confidence,
				Reasoned:   true,
			}) {
				telemetry.Infof(ctx, "[IP Enricher] Promoted PTR-derived registrable domain %s into the next collection frontier.", decision.Root)
			}
		}
	}

	return pCtx, nil
}

func buildPTRSeed(parent models.Seed, host string) models.Seed {
	host = discovery.NormalizeDomainIdentifier(host)
	if host == "" {
		return models.Seed{}
	}

	id := host
	if parent.ID != "" {
		id = parent.ID + ":ptr:" + host
	}

	tags := append([]string{}, parent.Tags...)
	tags = append(tags, "ptr-recursion")

	return models.Seed{
		ID:          id,
		CompanyName: host,
		Domains:     []string{host},
		Address:     parent.Address,
		Industry:    parent.Industry,
		Tags:        discovery.UniqueLowerStrings(tags),
	}
}

func ptrHostWithinSeedScope(seed models.Seed, host string) bool {
	hostRoot := discovery.RegistrableDomain(host)
	if hostRoot == "" {
		return false
	}

	for _, domain := range seed.Domains {
		if discovery.RegistrableDomain(domain) == hostRoot {
			return true
		}
	}

	return false
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

		queryDomain, ok := cymruOriginQueryDomain(parsedIP)
		if !ok {
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		txtRecords, err := net.DefaultResolver.LookupTXT(ctx, queryDomain)
		if err != nil || len(txtRecords) == 0 {
			return
		}

		record := strings.Trim(txtRecords[0], "\"")
		parts := strings.Split(record, "|")
		if len(parts) >= 1 {
			asnStr := strings.TrimSpace(parts[0])
			if asnID, err := strconv.Atoi(asnStr); err == nil {
				asset.IPDetails.ASN = asnID

				orgQuery := fmt.Sprintf("AS%d.asn.cymru.com", asnID)
				orgRecords, orgErr := net.DefaultResolver.LookupTXT(ctx, orgQuery)
				if orgErr == nil && len(orgRecords) > 0 {
					orgParts := strings.Split(strings.Trim(orgRecords[0], "\""), "|")
					if len(orgParts) >= 5 {
						asset.IPDetails.Organization = strings.TrimSpace(orgParts[4])
					}
				}
			}
		}

		if len(parts) >= 2 {
			asset.EnrichmentData["cidr"] = strings.TrimSpace(parts[1])
		}
	}()

	wgEnrich.Wait()
	asset.EnrichmentData["enriched"] = true
}

func cymruOriginQueryDomain(parsedIP net.IP) (string, bool) {
	if ipv4 := parsedIP.To4(); ipv4 != nil {
		octets := strings.Split(ipv4.String(), ".")
		if len(octets) != 4 {
			return "", false
		}
		return fmt.Sprintf("%s.%s.%s.%s.origin.asn.cymru.com", octets[3], octets[2], octets[1], octets[0]), true
	}

	ipv6 := parsedIP.To16()
	if ipv6 == nil {
		return "", false
	}

	hexValue := hex.EncodeToString(ipv6)
	nibbles := make([]string, 0, len(hexValue))
	for i := len(hexValue) - 1; i >= 0; i-- {
		nibbles = append(nibbles, string(hexValue[i]))
	}

	return strings.Join(nibbles, ".") + ".origin6.asn.cymru.com", true
}
