package enrich

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"asset-discovery/internal/discovery"
	"asset-discovery/internal/models"
	"asset-discovery/internal/registration"
	"asset-discovery/internal/tracing/telemetry"
)

const (
	defaultDomainLookupTimeout     = 2 * time.Second
	defaultDomainRDAPTimeout       = 10 * time.Second
	defaultDomainEnrichConcurrency = 32
)

type domainLookupIPFunc func(ctx context.Context, network string, host string) ([]net.IP, error)
type domainLookupMXFunc func(ctx context.Context, host string) ([]*net.MX, error)
type domainLookupTXTFunc func(ctx context.Context, host string) ([]string, error)
type domainLookupCNAMEFunc func(ctx context.Context, host string) (string, error)
type domainLookupRDAPFunc func(ctx context.Context, domain string) (*models.RDAPData, error)

type cachedDomainEnrichment struct {
	records  []models.DNSRecord
	ips      []string
	rdap     *models.RDAPData
	dnsDone  bool
	rdapDone bool
}

type domainEnrichmentResult struct {
	identifier string
	cache      cachedDomainEnrichment
	errors     []error
	cached     bool
	retryable  bool
}

type DomainEnricher struct {
	rdapClient     *http.Client
	lookupIP       domainLookupIPFunc
	lookupMX       domainLookupMXFunc
	lookupTXT      domainLookupTXTFunc
	lookupCNAME    domainLookupCNAMEFunc
	lookupRDAP     domainLookupRDAPFunc
	lookupTimeout  time.Duration
	rdapTimeout    time.Duration
	maxConcurrency int
	now            func() time.Time
	mu             sync.Mutex
	cache          map[string]cachedDomainEnrichment
	emittedIPKeys  map[string]struct{}
	lastContext    *models.PipelineContext
}

type DomainEnricherOption func(*DomainEnricher)

func WithDomainEnricherRDAPClient(client *http.Client) DomainEnricherOption {
	return func(e *DomainEnricher) {
		if client != nil {
			e.rdapClient = client
		}
	}
}

func NewDomainEnricher(options ...DomainEnricherOption) *DomainEnricher {
	enricher := &DomainEnricher{
		rdapClient: &http.Client{Timeout: defaultDomainRDAPTimeout},
		lookupIP: func(ctx context.Context, network string, host string) ([]net.IP, error) {
			return net.DefaultResolver.LookupIP(ctx, network, host)
		},
		lookupMX: func(ctx context.Context, host string) ([]*net.MX, error) {
			return net.DefaultResolver.LookupMX(ctx, host)
		},
		lookupTXT: func(ctx context.Context, host string) ([]string, error) {
			return net.DefaultResolver.LookupTXT(ctx, host)
		},
		lookupCNAME: func(ctx context.Context, host string) (string, error) {
			return net.DefaultResolver.LookupCNAME(ctx, host)
		},
		lookupTimeout:  defaultDomainLookupTimeout,
		rdapTimeout:    defaultDomainRDAPTimeout,
		maxConcurrency: defaultDomainEnrichConcurrency,
		now:            time.Now,
		cache:          make(map[string]cachedDomainEnrichment),
		emittedIPKeys:  make(map[string]struct{}),
	}

	for _, option := range options {
		if option != nil {
			option(enricher)
		}
	}

	enricher.lookupRDAP = func(ctx context.Context, domain string) (*models.RDAPData, error) {
		return registration.LookupDomain(ctx, enricher.rdapClient, domain)
	}

	return enricher
}

func (e *DomainEnricher) Process(ctx context.Context, pCtx *models.PipelineContext) (*models.PipelineContext, error) {
	telemetry.Info(ctx, "[Domain Enricher] Enriching domain assets...")
	pCtx.EnsureAssetState()
	e.ensureRunState(pCtx)

	assetIndexesByIdentifier := make(map[string][]int)
	needsRDAP := make(map[string]bool)

	for i := range pCtx.Assets {
		asset := &pCtx.Assets[i]
		switch asset.Type {
		case models.AssetTypeDomain:
			identifier := discovery.NormalizeDomainIdentifier(asset.Identifier)
			if identifier == "" {
				continue
			}
			assetIndexesByIdentifier[identifier] = append(assetIndexesByIdentifier[identifier], i)

			if identifier == discovery.RegistrableDomain(identifier) && (asset.DomainDetails == nil || asset.DomainDetails.RDAP == nil) {
				needsRDAP[identifier] = true
			}
		case models.AssetTypeIP:
			for _, enumerationID := range assetContributorEnumerationIDs(*asset) {
				e.seedEmittedIP(enumerationID, asset.Identifier)
			}
		}
	}

	if len(assetIndexesByIdentifier) == 0 {
		telemetry.Info(ctx, "[Domain Enricher] No domain assets found to enrich.")
		return pCtx, nil
	}

	identifiers := make([]string, 0, len(assetIndexesByIdentifier))
	for identifier := range assetIndexesByIdentifier {
		identifiers = append(identifiers, identifier)
	}
	sort.Strings(identifiers)

	readyResults := make(map[string]domainEnrichmentResult, len(identifiers))
	identifiersToLookup := make([]string, 0, len(identifiers))
	for _, identifier := range identifiers {
		entry, exists := e.cachedEntry(identifier)
		if exists && entry.dnsDone && (!needsRDAP[identifier] || entry.rdapDone) {
			readyResults[identifier] = domainEnrichmentResult{
				identifier: identifier,
				cache:      entry,
				cached:     true,
			}
			continue
		}
		identifiersToLookup = append(identifiersToLookup, identifier)
	}

	results := make(chan domainEnrichmentResult, len(identifiersToLookup))
	workerCount := minInt(e.maxConcurrency, len(identifiersToLookup))
	if workerCount <= 0 {
		workerCount = 0
	}

	jobs := make(chan string)
	var wg sync.WaitGroup
	for worker := 0; worker < workerCount; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for identifier := range jobs {
				results <- e.enrichDomain(ctx, identifier, needsRDAP[identifier])
			}
		}()
	}

	for _, identifier := range identifiersToLookup {
		jobs <- identifier
	}
	close(jobs)
	wg.Wait()
	close(results)

	var newErrors []error
	var newIPAssets []models.Asset
	var newRelations []models.AssetRelation
	var enrichmentObservations []models.AssetObservation

	for result := range results {
		newErrors = append(newErrors, result.errors...)
		e.storeCachedEntry(result.identifier, result.cache)
		readyResults[result.identifier] = result
	}

	for _, identifier := range identifiers {
		result, exists := readyResults[identifier]
		if !exists {
			continue
		}

		indexes := assetIndexesByIdentifier[identifier]
		for _, index := range indexes {
			asset := &pCtx.Assets[index]
			shouldRecordObservation := shouldRecordDomainEnrichmentObservation(*asset)
			if asset.DomainDetails == nil {
				asset.DomainDetails = &models.DomainDetails{}
			}
			if asset.EnrichmentData == nil {
				asset.EnrichmentData = make(map[string]interface{})
			}

			asset.DomainDetails.Records = mergeDomainRecords(asset.DomainDetails.Records, result.cache.records)
			if result.cache.rdap != nil && identifier == discovery.RegistrableDomain(identifier) && asset.DomainDetails.RDAP == nil {
				asset.DomainDetails.RDAP = cloneRDAPData(result.cache.rdap)
			}
			asset.EnrichmentData["enriched"] = true
			if asset.EnrichmentStates == nil {
				asset.EnrichmentStates = make(map[string]models.EnrichmentState)
			}
			stageState := models.EnrichmentState{UpdatedAt: e.now()}
			switch {
			case result.cached:
				stageState.Status = "cached"
				stageState.Cached = true
			case result.retryable:
				stageState.Status = "retryable"
				stageState.Error = joinEnrichmentErrors(result.errors)
			default:
				stageState.Status = "completed"
			}
			asset.EnrichmentStates["domain_enricher"] = stageState

			if shouldRecordObservation {
				enrichmentObservations = append(enrichmentObservations, models.AssetObservation{
					ID:               models.NewID("obs-domain-enricher"),
					Kind:             models.ObservationKindEnrichment,
					AssetID:          asset.ID,
					EnumerationID:    asset.EnumerationID,
					Type:             asset.Type,
					Identifier:       asset.Identifier,
					Source:           "domain_enricher",
					DiscoveryDate:    e.now(),
					DomainDetails:    asset.DomainDetails,
					EnrichmentData:   asset.EnrichmentData,
					EnrichmentStates: map[string]models.EnrichmentState{"domain_enricher": stageState},
				})
			}

			relationKindsByIP := domainIPRelationKinds(result.cache.records)
			for _, enumerationID := range assetContributorEnumerationIDs(*asset) {
				for _, ip := range result.cache.ips {
					if !e.claimEmittedIP(enumerationID, ip) {
						continue
					}

					relationKinds := relationKindsByIP[ip]
					if len(relationKinds) == 0 {
						relationKinds = []string{"dns_a"}
					}
					inclusionReason := "Resolved from " + asset.Identifier + " via " + strings.ToUpper(strings.TrimPrefix(relationKinds[0], "dns_"))
					observationID := models.NewID("ip-domain-enricher")
					newIPAssets = append(newIPAssets, models.Asset{
						ID:              observationID,
						EnumerationID:   enumerationID,
						Type:            models.AssetTypeIP,
						Identifier:      ip,
						Source:          "domain_enricher",
						DiscoveryDate:   e.now(),
						OwnershipState:  models.OwnershipStateAssociatedInfrastructure,
						InclusionReason: inclusionReason,
						IPDetails:       &models.IPDetails{},
					})

					for _, relationKind := range relationKinds {
						newRelations = append(newRelations, models.AssetRelation{
							ID:             models.NewID("rel-domain-ip"),
							FromAssetID:    asset.ID,
							FromAssetType:  models.AssetTypeDomain,
							FromIdentifier: asset.Identifier,
							ToAssetType:    models.AssetTypeIP,
							ToIdentifier:   ip,
							ObservationID:  observationID,
							EnumerationID:  enumerationID,
							Source:         "domain_enricher",
							Kind:           relationKind,
							Label:          "Resolved IP",
							Reason:         inclusionReason,
							DiscoveryDate:  e.now(),
						})
					}
				}
			}
		}
	}

	pCtx.Lock()
	pCtx.Errors = append(pCtx.Errors, newErrors...)
	pCtx.Unlock()
	pCtx.AppendAssetObservations(enrichmentObservations...)
	pCtx.AppendAssets(newIPAssets...)
	pCtx.AppendAssetRelations(newRelations...)

	return pCtx, nil
}

func (e *DomainEnricher) enrichDomain(ctx context.Context, identifier string, wantRDAP bool) domainEnrichmentResult {
	result := domainEnrichmentResult{identifier: identifier}
	cacheEntry, _ := e.cachedEntry(identifier)
	if cacheEntry.dnsDone && (!wantRDAP || cacheEntry.rdapDone) {
		result.cache = cacheEntry
		result.cached = true
		return result
	}

	addLookupError := func(kind string, err error) bool {
		if err == nil {
			return false
		}
		if isTerminalDNSLookupError(err) {
			return false
		}
		result.errors = append(result.errors, fmt.Errorf("domain_enricher lookup %s %s: %w", kind, identifier, err))
		return true
	}

	if !cacheEntry.dnsDone {
		dnsRetryable := false

		ipv4, err := e.lookupIPsWithTimeout(ctx, "ip4", identifier)
		dnsRetryable = addLookupError("A", err) || dnsRetryable
		for _, ip := range ipv4 {
			value := ip.String()
			if value == "" {
				continue
			}
			cacheEntry.records = append(cacheEntry.records, models.DNSRecord{Type: "A", Value: value})
			cacheEntry.ips = append(cacheEntry.ips, value)
		}

		ipv6, err := e.lookupIPsWithTimeout(ctx, "ip6", identifier)
		dnsRetryable = addLookupError("AAAA", err) || dnsRetryable
		for _, ip := range ipv6 {
			value := ip.String()
			if value == "" {
				continue
			}
			cacheEntry.records = append(cacheEntry.records, models.DNSRecord{Type: "AAAA", Value: value})
			cacheEntry.ips = append(cacheEntry.ips, value)
		}

		cname, err := e.lookupCNAMEWithTimeout(ctx, identifier)
		dnsRetryable = addLookupError("CNAME", err) || dnsRetryable
		cname = discovery.NormalizeDomainIdentifier(cname)
		if cname != "" && cname != identifier {
			cacheEntry.records = append(cacheEntry.records, models.DNSRecord{Type: "CNAME", Value: cname})
		}

		mxs, err := e.lookupMXWithTimeout(ctx, identifier)
		dnsRetryable = addLookupError("MX", err) || dnsRetryable
		for _, mx := range mxs {
			host := discovery.NormalizeDomainIdentifier(mx.Host)
			if host == "" {
				continue
			}
			cacheEntry.records = append(cacheEntry.records, models.DNSRecord{Type: "MX", Value: host})
		}

		txts, err := e.lookupTXTWithTimeout(ctx, identifier)
		dnsRetryable = addLookupError("TXT", err) || dnsRetryable
		for _, txt := range txts {
			txt = strings.TrimSpace(txt)
			if txt == "" {
				continue
			}
			cacheEntry.records = append(cacheEntry.records, models.DNSRecord{Type: "TXT", Value: txt})
		}

		cacheEntry.dnsDone = !dnsRetryable
		result.retryable = dnsRetryable
	}

	if wantRDAP && !cacheEntry.rdapDone {
		rdap, err := e.lookupRDAPWithTimeout(ctx, identifier)
		switch {
		case err == nil:
			cacheEntry.rdap = cloneRDAPData(rdap)
			cacheEntry.rdapDone = true
		case err == registration.ErrUnsupportedRegistrationData:
			cacheEntry.rdapDone = true
		default:
			result.errors = append(result.errors, fmt.Errorf("domain_enricher lookup RDAP %s: %w", identifier, err))
			result.retryable = true
		}
	}

	cacheEntry.records = uniqueDomainRecords(cacheEntry.records)
	cacheEntry.ips = discovery.UniqueLowerStrings(cacheEntry.ips)
	result.cache = cacheEntry
	return result
}

func isTerminalDNSLookupError(err error) bool {
	if err == nil {
		return false
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		if dnsErr.IsNotFound {
			return true
		}
		if !dnsErr.IsTemporary && !dnsErr.IsTimeout && strings.Contains(strings.ToLower(dnsErr.Err), "no such host") {
			return true
		}
	}

	return strings.Contains(strings.ToLower(err.Error()), "no such host")
}

func joinEnrichmentErrors(errs []error) string {
	if len(errs) == 0 {
		return ""
	}

	values := make([]string, 0, len(errs))
	seen := make(map[string]struct{}, len(errs))
	for _, err := range errs {
		if err == nil {
			continue
		}
		value := strings.TrimSpace(err.Error())
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		values = append(values, value)
	}

	return strings.Join(values, " | ")
}

func (e *DomainEnricher) lookupIPsWithTimeout(ctx context.Context, network string, host string) ([]net.IP, error) {
	lookupCtx, cancel := context.WithTimeout(ctx, e.lookupTimeout)
	defer cancel()
	return e.lookupIP(lookupCtx, network, host)
}

func (e *DomainEnricher) lookupMXWithTimeout(ctx context.Context, host string) ([]*net.MX, error) {
	lookupCtx, cancel := context.WithTimeout(ctx, e.lookupTimeout)
	defer cancel()
	return e.lookupMX(lookupCtx, host)
}

func (e *DomainEnricher) lookupTXTWithTimeout(ctx context.Context, host string) ([]string, error) {
	lookupCtx, cancel := context.WithTimeout(ctx, e.lookupTimeout)
	defer cancel()
	return e.lookupTXT(lookupCtx, host)
}

func (e *DomainEnricher) lookupCNAMEWithTimeout(ctx context.Context, host string) (string, error) {
	lookupCtx, cancel := context.WithTimeout(ctx, e.lookupTimeout)
	defer cancel()
	return e.lookupCNAME(lookupCtx, host)
}

func (e *DomainEnricher) lookupRDAPWithTimeout(ctx context.Context, host string) (*models.RDAPData, error) {
	lookupCtx, cancel := context.WithTimeout(ctx, e.rdapTimeout)
	defer cancel()
	return e.lookupRDAP(lookupCtx, host)
}

func mergeDomainRecords(existing []models.DNSRecord, incoming []models.DNSRecord) []models.DNSRecord {
	if len(incoming) == 0 {
		return existing
	}

	merged := append([]models.DNSRecord(nil), existing...)
	seen := make(map[string]struct{}, len(existing)+len(incoming))
	for _, record := range merged {
		seen[domainRecordKey(record)] = struct{}{}
	}

	for _, record := range incoming {
		key := domainRecordKey(record)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		merged = append(merged, record)
	}

	return merged
}

func uniqueDomainRecords(records []models.DNSRecord) []models.DNSRecord {
	if len(records) == 0 {
		return nil
	}

	unique := make([]models.DNSRecord, 0, len(records))
	seen := make(map[string]struct{}, len(records))
	for _, record := range records {
		record.Type = strings.TrimSpace(strings.ToUpper(record.Type))
		record.Value = strings.TrimSpace(record.Value)
		if record.Type == "" || record.Value == "" {
			continue
		}
		key := domainRecordKey(record)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, record)
	}

	return unique
}

func domainRecordKey(record models.DNSRecord) string {
	return strings.TrimSpace(strings.ToUpper(record.Type)) + "|" + strings.TrimSpace(strings.ToLower(record.Value))
}

func cloneRDAPData(data *models.RDAPData) *models.RDAPData {
	if data == nil {
		return nil
	}

	clone := *data
	clone.Statuses = append([]string(nil), data.Statuses...)
	clone.NameServers = append([]string(nil), data.NameServers...)
	return &clone
}

func cloneCachedDomainEnrichment(entry cachedDomainEnrichment) cachedDomainEnrichment {
	entry.records = append([]models.DNSRecord(nil), entry.records...)
	entry.ips = append([]string(nil), entry.ips...)
	entry.rdap = cloneRDAPData(entry.rdap)
	return entry
}

func (e *DomainEnricher) cachedEntry(identifier string) (cachedDomainEnrichment, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()

	entry, exists := e.cache[identifier]
	if !exists {
		return cachedDomainEnrichment{}, false
	}

	return cloneCachedDomainEnrichment(entry), true
}

func (e *DomainEnricher) ensureRunState(pCtx *models.PipelineContext) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.lastContext == pCtx {
		return
	}

	e.lastContext = pCtx
	e.cache = make(map[string]cachedDomainEnrichment)
	e.emittedIPKeys = make(map[string]struct{})
}

func (e *DomainEnricher) storeCachedEntry(identifier string, entry cachedDomainEnrichment) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.cache[identifier] = cloneCachedDomainEnrichment(entry)
}

func (e *DomainEnricher) seedEmittedIP(enumerationID string, ip string) {
	key := domainEnricherIPKey(enumerationID, ip)
	if key == "" {
		return
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	e.emittedIPKeys[key] = struct{}{}
}

func (e *DomainEnricher) claimEmittedIP(enumerationID string, ip string) bool {
	key := domainEnricherIPKey(enumerationID, ip)
	if key == "" {
		return false
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	if _, exists := e.emittedIPKeys[key]; exists {
		return false
	}

	e.emittedIPKeys[key] = struct{}{}
	return true
}

func domainEnricherIPKey(enumerationID string, ip string) string {
	enumerationID = strings.TrimSpace(enumerationID)
	ip = strings.TrimSpace(ip)
	if enumerationID == "" || ip == "" {
		return ""
	}
	return enumerationID + "|" + ip
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func assetContributorEnumerationIDs(asset models.Asset) []string {
	values := make([]string, 0, len(asset.Provenance)+1)
	if asset.EnumerationID != "" {
		values = append(values, asset.EnumerationID)
	}
	for _, item := range asset.Provenance {
		if item.EnumerationID != "" {
			values = append(values, item.EnumerationID)
		}
	}
	return uniquePreservingOrder(values)
}

func shouldRecordDomainEnrichmentObservation(asset models.Asset) bool {
	state, exists := asset.EnrichmentStates["domain_enricher"]
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

func domainIPRelationKinds(records []models.DNSRecord) map[string][]string {
	kindsByIP := make(map[string][]string)
	for _, record := range records {
		kind := ""
		switch strings.ToUpper(strings.TrimSpace(record.Type)) {
		case "A":
			kind = "dns_a"
		case "AAAA":
			kind = "dns_aaaa"
		}
		if kind == "" {
			continue
		}
		ip := strings.TrimSpace(strings.ToLower(record.Value))
		if ip == "" {
			continue
		}
		kindsByIP[ip] = appendUniqueString(kindsByIP[ip], kind)
	}
	return kindsByIP
}

func appendUniqueString(values []string, candidate string) []string {
	for _, value := range values {
		if value == candidate {
			return values
		}
	}
	return append(values, candidate)
}

func uniquePreservingOrder(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}

	return out
}
