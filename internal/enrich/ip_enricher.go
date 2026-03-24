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
	pCtx.EnsureAssetState()

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
	var enrichmentObservationsMu sync.Mutex
	enrichmentObservations := make([]models.AssetObservation, 0, len(ipAssets))

	for _, asset := range ipAssets {
		wg.Add(1)
		sem <- struct{}{} // Acquire semaphore

		go func(a *models.Asset) {
			defer wg.Done()
			defer func() { <-sem }() // Release semaphore

			performedLookup := needsIPEnrichment(a)
			previousOwnership := a.OwnershipState
			previousReason := a.InclusionReason

			if performedLookup && e.enrichAsset != nil {
				e.enrichAsset(a)
			}

			classifyCanonicalIPAsset(a, enumToSeed)
			if observation := buildIPEnrichmentObservation(*a, performedLookup, previousOwnership, previousReason); observation != nil {
				enrichmentObservationsMu.Lock()
				enrichmentObservations = append(enrichmentObservations, *observation)
				enrichmentObservationsMu.Unlock()
			}

			if a.IPDetails == nil || a.IPDetails.PTR == "" {
				return
			}

			host := discovery.NormalizeDomainIdentifier(a.IPDetails.PTR)
			if host == "" || len(discovery.ExtractDomainCandidates(host)) == 0 {
				return
			}

			root := discovery.RegistrableDomain(host)
			for _, enumerationID := range assetContributorEnumerationIDs(*a) {
				seed, ok := enumToSeed[enumerationID]
				if !ok {
					continue
				}

				ptrObservationsMu.Lock()
				if ptrHostWithinSeedScope(seed, host) {
					if _, exists := ptrFollowUpSeeds[host]; !exists {
						ptrFollowUpSeeds[host] = buildPTRSeed(seed, host)
					}
				}

				if root != "" {
					observationKey := enumerationID + "|" + root
					observation, exists := ptrObservations[observationKey]
					if !exists {
						observation = &ptrObservation{
							root: root,
							seed: seed,
						}
						ptrObservations[observationKey] = observation
					}
					observation.hits++
					if len(observation.hosts) < 3 {
						observation.hosts = append(observation.hosts, a.IPDetails.PTR)
					}
				}
				ptrObservationsMu.Unlock()
			}
		}(asset)
	}

	wg.Wait()
	telemetry.Info(ctx, "[IP Enricher] Finished enriching all IPs.")
	pCtx.AppendAssetObservations(enrichmentObservations...)

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
	if asset.EnrichmentStates == nil {
		asset.EnrichmentStates = make(map[string]models.EnrichmentState)
	}

	ipStr := asset.Identifier
	parsedIP := net.ParseIP(ipStr)
	if parsedIP == nil {
		return
	}

	var wgEnrich sync.WaitGroup

	// Task 1: Reverse DNS Lookup (PTR)
	var lookupErrorsMu sync.Mutex
	lookupErrors := make([]error, 0, 2)
	recordLookupError := func(kind string, err error) {
		if err == nil {
			return
		}
		lookupErrorsMu.Lock()
		defer lookupErrorsMu.Unlock()
		lookupErrors = append(lookupErrors, fmt.Errorf("ip_enricher lookup %s %s: %w", kind, ipStr, err))
	}

	wgEnrich.Add(1)
	go func() {
		defer wgEnrich.Done()
		names, err := lookupAddrWithRetry(ipStr)
		if err != nil {
			recordLookupError("ptr", err)
			return
		}
		if len(names) > 0 {
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

		txtRecords, err := lookupTXTWithRetry(queryDomain)
		if err != nil {
			recordLookupError("asn", err)
			return
		}
		if len(txtRecords) == 0 {
			return
		}

		record := strings.Trim(txtRecords[0], "\"")
		parts := strings.Split(record, "|")
		if len(parts) >= 1 {
			asnStr := strings.TrimSpace(parts[0])
			if asnID, err := strconv.Atoi(asnStr); err == nil {
				asset.IPDetails.ASN = asnID

				orgQuery := fmt.Sprintf("AS%d.asn.cymru.com", asnID)
				orgRecords, orgErr := lookupTXTWithRetry(orgQuery)
				if orgErr != nil {
					recordLookupError("org", orgErr)
				}
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
	stageState := models.EnrichmentState{UpdatedAt: time.Now()}
	if len(lookupErrors) > 0 {
		stageState.Status = "retryable"
		stageState.Error = joinEnrichmentErrors(lookupErrors)
	} else {
		stageState.Status = "completed"
	}
	asset.EnrichmentStates["ip_enricher"] = stageState
}

func lookupAddrWithRetry(ip string) ([]string, error) {
	return retryIPResolverLookup(func(ctx context.Context) ([]string, error) {
		return net.DefaultResolver.LookupAddr(ctx, ip)
	})
}

func lookupTXTWithRetry(name string) ([]string, error) {
	return retryIPResolverLookup(func(ctx context.Context) ([]string, error) {
		return net.DefaultResolver.LookupTXT(ctx, name)
	})
}

func retryIPResolverLookup(lookup func(context.Context) ([]string, error)) ([]string, error) {
	backoff := 250 * time.Millisecond
	var lastErr error

	for attempt := 1; attempt <= 3; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		values, err := lookup(ctx)
		cancel()

		switch {
		case err == nil:
			return values, nil
		case isTerminalDNSLookupError(err):
			return nil, nil
		default:
			lastErr = err
		}

		if attempt == 3 {
			break
		}

		time.Sleep(backoff)
		if backoff < 2*time.Second {
			backoff *= 2
			if backoff > 2*time.Second {
				backoff = 2 * time.Second
			}
		}
	}

	return nil, lastErr
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

func needsIPEnrichment(asset *models.Asset) bool {
	if asset == nil {
		return false
	}
	state, exists := asset.EnrichmentStates["ip_enricher"]
	if !exists {
		return true
	}
	switch strings.TrimSpace(strings.ToLower(state.Status)) {
	case "", "missing", "retryable":
		return true
	default:
		return false
	}
}

func classifyCanonicalIPAsset(asset *models.Asset, enumToSeed map[string]models.Seed) {
	if asset == nil {
		return
	}

	seedContexts := contributorSeedsForAsset(*asset, enumToSeed)
	if len(seedContexts) == 0 {
		if asset.OwnershipState == "" {
			asset.OwnershipState = models.OwnershipStateAssociatedInfrastructure
		}
		return
	}

	if ipOwnershipMatchesSeed(*asset, seedContexts) {
		asset.OwnershipState = models.OwnershipStateOwned
		if asset.InclusionReason == "" {
			asset.InclusionReason = "Corroborated by PTR/ASN/CIDR overlap with an in-scope seed"
		}
		return
	}

	ptrHost := ""
	ptrRoot := ""
	if asset.IPDetails != nil {
		ptrHost = discovery.NormalizeDomainIdentifier(asset.IPDetails.PTR)
		ptrRoot = discovery.RegistrableDomain(ptrHost)
	}

	if ptrRoot != "" {
		matchesSeedRoot := false
		for _, seed := range seedContexts {
			for _, domain := range seed.Domains {
				if discovery.RegistrableDomain(domain) == ptrRoot {
					matchesSeedRoot = true
					break
				}
			}
			if matchesSeedRoot {
				break
			}
		}
		if !matchesSeedRoot {
			asset.OwnershipState = models.OwnershipStateUncertain
			asset.InclusionReason = "Observed behind an in-scope domain, but PTR points to " + ptrHost
			return
		}
	}

	if asset.OwnershipState == "" {
		asset.OwnershipState = models.OwnershipStateAssociatedInfrastructure
	}
	if asset.InclusionReason == "" {
		asset.InclusionReason = "Observed as infrastructure supporting an in-scope domain"
	}
}

func buildIPEnrichmentObservation(asset models.Asset, performedLookup bool, previousOwnership models.OwnershipState, previousReason string) *models.AssetObservation {
	if !performedLookup && asset.OwnershipState == previousOwnership && asset.InclusionReason == previousReason {
		return nil
	}

	stageState := models.EnrichmentState{}
	if asset.EnrichmentStates != nil {
		stageState = asset.EnrichmentStates["ip_enricher"]
	}
	if !performedLookup {
		if stageState.Status == "" {
			stageState.Status = "cached"
		}
		stageState.Cached = true
		if stageState.UpdatedAt.IsZero() {
			stageState.UpdatedAt = time.Now()
		}
	}

	return &models.AssetObservation{
		ID:               models.NewID("obs-ip-enricher"),
		Kind:             models.ObservationKindEnrichment,
		AssetID:          asset.ID,
		EnumerationID:    asset.EnumerationID,
		Type:             asset.Type,
		Identifier:       asset.Identifier,
		Source:           "ip_enricher",
		DiscoveryDate:    time.Now(),
		OwnershipState:   asset.OwnershipState,
		InclusionReason:  asset.InclusionReason,
		IPDetails:        asset.IPDetails,
		EnrichmentData:   asset.EnrichmentData,
		EnrichmentStates: map[string]models.EnrichmentState{"ip_enricher": stageState},
	}
}

func contributorSeedsForAsset(asset models.Asset, enumToSeed map[string]models.Seed) []models.Seed {
	enumerationIDs := assetContributorEnumerationIDs(asset)
	seeds := make([]models.Seed, 0, len(enumerationIDs))
	seen := make(map[string]struct{}, len(enumerationIDs))
	for _, enumerationID := range enumerationIDs {
		seed, ok := enumToSeed[enumerationID]
		if !ok {
			continue
		}
		key := seed.ID
		if key == "" {
			key = strings.Join(seed.Domains, ",")
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		seeds = append(seeds, seed)
	}
	return seeds
}

func ipOwnershipMatchesSeed(asset models.Asset, seeds []models.Seed) bool {
	parsedIP := net.ParseIP(asset.Identifier)
	ptrRoot := ""
	if asset.IPDetails != nil {
		ptrRoot = discovery.RegistrableDomain(discovery.NormalizeDomainIdentifier(asset.IPDetails.PTR))
	}

	for _, seed := range seeds {
		for _, domain := range seed.Domains {
			if ptrRoot != "" && discovery.RegistrableDomain(domain) == ptrRoot {
				return true
			}
		}
		if asset.IPDetails != nil {
			for _, asn := range seed.ASN {
				if asn != 0 && asn == asset.IPDetails.ASN {
					return true
				}
			}
		}
		if parsedIP != nil {
			for _, cidr := range seed.CIDR {
				_, network, err := net.ParseCIDR(strings.TrimSpace(cidr))
				if err == nil && network.Contains(parsedIP) {
					return true
				}
			}
		}
	}
	return false
}
