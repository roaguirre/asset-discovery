package enrich

import (
	"context"
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
	enrichedAssets map[string]struct{}
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
		enrichedAssets: make(map[string]struct{}),
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
			if e.assetAlreadyEnriched(asset) {
				continue
			}
			assetIndexesByIdentifier[identifier] = append(assetIndexesByIdentifier[identifier], i)

			if identifier == discovery.RegistrableDomain(identifier) && (asset.DomainDetails == nil || asset.DomainDetails.RDAP == nil) {
				needsRDAP[identifier] = true
			}
		case models.AssetTypeIP:
			e.seedEmittedIP(asset.EnumerationID, asset.Identifier)
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

	readyResults := make(map[string]cachedDomainEnrichment, len(identifiers))
	identifiersToLookup := make([]string, 0, len(identifiers))
	for _, identifier := range identifiers {
		entry, exists := e.cachedEntry(identifier)
		if exists && entry.dnsDone && (!needsRDAP[identifier] || entry.rdapDone) {
			readyResults[identifier] = entry
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

	for result := range results {
		newErrors = append(newErrors, result.errors...)
		e.storeCachedEntry(result.identifier, result.cache)
		readyResults[result.identifier] = result.cache
	}

	for _, identifier := range identifiers {
		result, exists := readyResults[identifier]
		if !exists {
			continue
		}

		indexes := assetIndexesByIdentifier[identifier]
		for _, index := range indexes {
			asset := &pCtx.Assets[index]
			if asset.DomainDetails == nil {
				asset.DomainDetails = &models.DomainDetails{}
			}
			if asset.EnrichmentData == nil {
				asset.EnrichmentData = make(map[string]interface{})
			}

			asset.DomainDetails.Records = mergeDomainRecords(asset.DomainDetails.Records, result.records)
			if result.rdap != nil && identifier == discovery.RegistrableDomain(identifier) && asset.DomainDetails.RDAP == nil {
				asset.DomainDetails.RDAP = cloneRDAPData(result.rdap)
			}
			asset.EnrichmentData["enriched"] = true
			e.markAssetEnriched(asset)

			for _, ip := range result.ips {
				if !e.claimEmittedIP(asset.EnumerationID, ip) {
					continue
				}

				newIPAssets = append(newIPAssets, models.Asset{
					ID:            models.NewID("ip-domain-enricher"),
					EnumerationID: asset.EnumerationID,
					Type:          models.AssetTypeIP,
					Identifier:    ip,
					Source:        "domain_enricher",
					DiscoveryDate: e.now(),
					IPDetails:     &models.IPDetails{},
				})
			}
		}
	}

	pCtx.Lock()
	pCtx.Errors = append(pCtx.Errors, newErrors...)
	pCtx.Assets = append(pCtx.Assets, newIPAssets...)
	pCtx.Unlock()

	return pCtx, nil
}

func (e *DomainEnricher) enrichDomain(ctx context.Context, identifier string, wantRDAP bool) domainEnrichmentResult {
	result := domainEnrichmentResult{identifier: identifier}
	cacheEntry, _ := e.cachedEntry(identifier)

	addLookupError := func(kind string, err error) {
		if err == nil {
			return
		}
		result.errors = append(result.errors, fmt.Errorf("domain_enricher lookup %s %s: %w", kind, identifier, err))
	}

	if !cacheEntry.dnsDone {
		ipv4, err := e.lookupIPsWithTimeout(ctx, "ip4", identifier)
		addLookupError("A", err)
		for _, ip := range ipv4 {
			value := ip.String()
			if value == "" {
				continue
			}
			cacheEntry.records = append(cacheEntry.records, models.DNSRecord{Type: "A", Value: value})
			cacheEntry.ips = append(cacheEntry.ips, value)
		}

		ipv6, err := e.lookupIPsWithTimeout(ctx, "ip6", identifier)
		addLookupError("AAAA", err)
		for _, ip := range ipv6 {
			value := ip.String()
			if value == "" {
				continue
			}
			cacheEntry.records = append(cacheEntry.records, models.DNSRecord{Type: "AAAA", Value: value})
			cacheEntry.ips = append(cacheEntry.ips, value)
		}

		cname, err := e.lookupCNAMEWithTimeout(ctx, identifier)
		addLookupError("CNAME", err)
		cname = discovery.NormalizeDomainIdentifier(cname)
		if cname != "" && cname != identifier {
			cacheEntry.records = append(cacheEntry.records, models.DNSRecord{Type: "CNAME", Value: cname})
		}

		mxs, err := e.lookupMXWithTimeout(ctx, identifier)
		addLookupError("MX", err)
		for _, mx := range mxs {
			host := discovery.NormalizeDomainIdentifier(mx.Host)
			if host == "" {
				continue
			}
			cacheEntry.records = append(cacheEntry.records, models.DNSRecord{Type: "MX", Value: host})
		}

		txts, err := e.lookupTXTWithTimeout(ctx, identifier)
		addLookupError("TXT", err)
		for _, txt := range txts {
			txt = strings.TrimSpace(txt)
			if txt == "" {
				continue
			}
			cacheEntry.records = append(cacheEntry.records, models.DNSRecord{Type: "TXT", Value: txt})
		}

		cacheEntry.dnsDone = true
	}

	if wantRDAP && !cacheEntry.rdapDone {
		rdap, err := e.lookupRDAPWithTimeout(ctx, identifier)
		if err != nil && err != registration.ErrUnsupportedRegistrationData {
			result.errors = append(result.errors, fmt.Errorf("domain_enricher lookup RDAP %s: %w", identifier, err))
		} else if err == nil {
			cacheEntry.rdap = cloneRDAPData(rdap)
		}
		cacheEntry.rdapDone = true
	}

	cacheEntry.records = uniqueDomainRecords(cacheEntry.records)
	cacheEntry.ips = discovery.UniqueLowerStrings(cacheEntry.ips)
	result.cache = cacheEntry
	return result
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
	e.enrichedAssets = make(map[string]struct{})
	e.emittedIPKeys = make(map[string]struct{})
}

func (e *DomainEnricher) storeCachedEntry(identifier string, entry cachedDomainEnrichment) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.cache[identifier] = cloneCachedDomainEnrichment(entry)
}

func (e *DomainEnricher) assetAlreadyEnriched(asset *models.Asset) bool {
	key := domainEnricherAssetKey(asset)
	if key == "" {
		return false
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	_, exists := e.enrichedAssets[key]
	return exists
}

func (e *DomainEnricher) markAssetEnriched(asset *models.Asset) {
	key := domainEnricherAssetKey(asset)
	if key == "" {
		return
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	e.enrichedAssets[key] = struct{}{}
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

func domainEnricherAssetKey(asset *models.Asset) string {
	if asset == nil {
		return ""
	}
	if id := strings.TrimSpace(asset.ID); id != "" {
		return "id:" + id
	}

	parts := []string{
		strings.TrimSpace(asset.EnumerationID),
		string(asset.Type),
		strings.TrimSpace(asset.Identifier),
		strings.TrimSpace(asset.Source),
		asset.DiscoveryDate.UTC().Format(time.RFC3339Nano),
	}
	return strings.Join(parts, "|")
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
