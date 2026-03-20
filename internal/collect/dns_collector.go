package collect

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
	"asset-discovery/internal/ownership"
	"asset-discovery/internal/registration"
	"asset-discovery/internal/tracing/lineage"
	"asset-discovery/internal/tracing/telemetry"
	"golang.org/x/net/publicsuffix"
)

const (
	defaultDNSLookupTimeout         = 2 * time.Second
	defaultDNSRDAPTimeout           = 10 * time.Second
	defaultMaxVariantProbesPerRoot  = 256
	defaultMaxLiveVariantCandidates = 25
	defaultVariantProbeConcurrency  = 32
	dnsAssetSourcePivot             = "dns_dns_pivot"
	dnsAssetSourceVariant           = "dns_variant_sweep"
	dnsSeedEvidenceKindRecordPivot  = "dns_record_pivot"
	dnsSeedEvidenceKindVariantPivot = "dns_variant_pivot"
	dnsSeedTagRecordPivot           = "dns-record-pivot"
	dnsSeedTagVariantPivot          = "dns-variant-pivot"
	dnsSourceKindNS                 = "ns_root"
	dnsSourceKindMX                 = "mx_root"
	dnsSourceKindTXT                = "txt_root"
	dnsSourceKindCNAME              = "cname_root"
	dnsSourceKindVariant            = "dns_variant"
)

var defaultGenericVariantSuffixes = []string{"com", "net", "org", "io", "co", "app", "dev", "ai", "cloud", "tech"}

type dnsLookupIPFunc func(ctx context.Context, host string) ([]net.IP, error)
type dnsLookupMXFunc func(ctx context.Context, host string) ([]*net.MX, error)
type dnsLookupTXTFunc func(ctx context.Context, host string) ([]string, error)
type dnsLookupNSFunc func(ctx context.Context, host string) ([]*net.NS, error)
type dnsLookupCNAMEFunc func(ctx context.Context, host string) (string, error)
type dnsLookupRDAPFunc func(ctx context.Context, domain string) (*models.RDAPData, error)

type dnsObservation struct {
	domain     string
	root       string
	records    []models.DNSRecord
	ips        []string
	mxHosts    []string
	mxRoots    []string
	nsHosts    []string
	nsRoots    []string
	txtValues  []string
	txtDomains []string
	txtRoots   []string
	cname      string
	live       bool
}

type dnsLookupIssue struct {
	recordKind string
	domain     string
	err        error
}

type dnsDomainCandidate struct {
	host       string
	root       string
	sourceKind string
}

type dnsPivotCandidate struct {
	root        string
	variant     bool
	sourceKinds map[string]struct{}
	sampleHosts []string
	observation *dnsObservation
	rdap        *models.RDAPData
}

type dnsSeedBaseline struct {
	seedRoots      map[string]struct{}
	ips            map[string]struct{}
	nsRoots        map[string]struct{}
	mailRoots      map[string]struct{}
	txtRoots       map[string]struct{}
	cnameRoots     map[string]struct{}
	rdapOrgs       map[string]struct{}
	rdapEmailRoots map[string]struct{}
}

type observedDNSCandidate struct {
	root        string
	observation dnsObservation
	rdap        *models.RDAPData
	index       int
}

type dnsLookupIssueAggregator struct {
	counts  map[string]int
	samples map[string]error
}

// DNSCollector resolves seed domains, extracts more DNS-derived assets, and
// judge-gates cross-root DNS pivots into later collection waves.
type DNSCollector struct {
	judge                    ownership.Judge
	rdapClient               *http.Client
	lookupIPs                dnsLookupIPFunc
	lookupMX                 dnsLookupMXFunc
	lookupTXT                dnsLookupTXTFunc
	lookupNS                 dnsLookupNSFunc
	lookupCNAME              dnsLookupCNAMEFunc
	lookupRDAP               dnsLookupRDAPFunc
	lookupTimeout            time.Duration
	rdapTimeout              time.Duration
	maxVariantProbesPerRoot  int
	maxLiveVariantCandidates int
	variantProbeConcurrency  int
	variantSuffixes          []string
	genericVariantSuffixes   []string
}

type DNSCollectorOption func(*DNSCollector)

func WithDNSCollectorJudge(judge ownership.Judge) DNSCollectorOption {
	return func(c *DNSCollector) {
		c.judge = judge
	}
}

func WithDNSCollectorRDAPClient(client *http.Client) DNSCollectorOption {
	return func(c *DNSCollector) {
		if client != nil {
			c.rdapClient = client
		}
	}
}

func NewDNSCollector(options ...DNSCollectorOption) *DNSCollector {
	collector := &DNSCollector{
		judge:      ownership.NewDefaultJudge(),
		rdapClient: &http.Client{Timeout: defaultDNSRDAPTimeout},
		lookupIPs: func(ctx context.Context, host string) ([]net.IP, error) {
			return net.DefaultResolver.LookupIP(ctx, "ip", host)
		},
		lookupMX: func(ctx context.Context, host string) ([]*net.MX, error) {
			return net.DefaultResolver.LookupMX(ctx, host)
		},
		lookupTXT: func(ctx context.Context, host string) ([]string, error) {
			return net.DefaultResolver.LookupTXT(ctx, host)
		},
		lookupNS: func(ctx context.Context, host string) ([]*net.NS, error) {
			return net.DefaultResolver.LookupNS(ctx, host)
		},
		lookupCNAME: func(ctx context.Context, host string) (string, error) {
			return net.DefaultResolver.LookupCNAME(ctx, host)
		},
		lookupTimeout:            defaultDNSLookupTimeout,
		rdapTimeout:              defaultDNSRDAPTimeout,
		maxVariantProbesPerRoot:  defaultMaxVariantProbesPerRoot,
		maxLiveVariantCandidates: defaultMaxLiveVariantCandidates,
		variantProbeConcurrency:  defaultVariantProbeConcurrency,
		variantSuffixes:          discovery.ICANNPublicSuffixes(),
		genericVariantSuffixes:   append([]string(nil), defaultGenericVariantSuffixes...),
	}

	for _, option := range options {
		if option != nil {
			option(collector)
		}
	}

	collector.lookupRDAP = func(ctx context.Context, domain string) (*models.RDAPData, error) {
		return registration.LookupDomain(ctx, collector.rdapClient, domain)
	}

	return collector
}

func (c *DNSCollector) Process(ctx context.Context, pCtx *models.PipelineContext) (*models.PipelineContext, error) {
	telemetry.Info(ctx, "[DNS Collector] Processing seeds...")

	var newEnums []models.Enumeration
	var newErrors []error
	var newAssets []models.Asset

	for _, seed := range pCtx.CollectionSeeds() {
		enum := models.Enumeration{
			ID:        models.NewID("enum-dns"),
			SeedID:    seed.ID,
			Status:    "running",
			CreatedAt: time.Now(),
			StartedAt: time.Now(),
		}
		newEnums = append(newEnums, enum)

		metricsProbed := 0
		metricsLiveVariants := 0
		metricsJudgeSubmitted := 0
		metricsPromoted := 0

		baseline := newDNSSeedBaseline(seed)
		candidateByRoot := make(map[string]*dnsPivotCandidate)

		telemetry.Infof(ctx, "[DNS Collector] Resolving domains for seed: %s", seed.CompanyName)
		for _, baseDomain := range seed.Domains {
			baseDomain = discovery.NormalizeDomainIdentifier(baseDomain)
			if baseDomain == "" {
				continue
			}

			observation, lookupIssues := c.observeDomain(ctx, baseDomain)
			for _, issue := range lookupIssues {
				err := issue.asError()
				telemetry.Infof(ctx, "[DNS Collector] %v", err)
				newErrors = append(newErrors, err)
			}
			baseRoot := discovery.RegistrableDomain(baseDomain)

			rdapData, err := c.lookupRDAPWithTimeout(ctx, baseRoot)
			if err != nil && err != registration.ErrUnsupportedRegistrationData {
				newErrors = append(newErrors, err)
			}

			baseline.addObservation(baseDomain, observation, rdapData)

			if len(observation.records) > 0 {
				newAssets = append(newAssets, domainAssetFromObservation(models.NewID("dom"), enum.ID, baseDomain, "dns_collector", observation, nil))
			}

			for _, ip := range observation.ips {
				newAssets = append(newAssets, models.Asset{
					ID:            models.NewID("ip"),
					EnumerationID: enum.ID,
					Type:          models.AssetTypeIP,
					Identifier:    ip,
					Source:        "dns_collector",
					DiscoveryDate: time.Now(),
					IPDetails:     &models.IPDetails{},
				})
			}

			for _, candidate := range observation.domainCandidates() {
				if candidate.root == "" {
					continue
				}

				if _, inScope := baseline.seedRoots[candidate.root]; inScope {
					if candidate.host != "" && candidate.host != baseDomain {
						newAssets = append(newAssets, models.Asset{
							ID:            models.NewID("dom-dns-host"),
							EnumerationID: enum.ID,
							Type:          models.AssetTypeDomain,
							Identifier:    candidate.host,
							Source:        "dns_collector",
							DiscoveryDate: time.Now(),
							DomainDetails: &models.DomainDetails{},
						})
					}
					continue
				}

				pivotCandidate := ensureDNSPivotCandidate(candidateByRoot, candidate.root)
				pivotCandidate.addSource(candidate.sourceKind, candidate.host)
			}
		}

		exactRoots := sortedDNSPivotRoots(candidateByRoot)
		observedRoots, observedErrors := c.observeCandidateRoots(ctx, exactRoots, len(exactRoots), "record pivot")
		newErrors = append(newErrors, observedErrors...)
		for _, observed := range observedRoots {
			pivotCandidate := ensureDNSPivotCandidate(candidateByRoot, observed.root)
			observation := observed.observation
			pivotCandidate.observation = &observation
			pivotCandidate.rdap = observed.rdap
		}

		for _, labelGroup := range groupSeedRootsByLabel(baseline.seedRoots) {
			probeRoots := c.buildVariantProbeRoots(seed, labelGroup.roots, baseline.seedRoots, candidateByRoot)
			metricsProbed += len(probeRoots)
			if len(probeRoots) == 0 {
				continue
			}

			liveVariants, variantErrors := c.observeCandidateRoots(ctx, probeRoots, c.maxLiveVariantCandidates, "variant sweep")
			newErrors = append(newErrors, variantErrors...)
			metricsLiveVariants += len(liveVariants)

			for _, observed := range liveVariants {
				pivotCandidate := ensureDNSPivotCandidate(candidateByRoot, observed.root)
				pivotCandidate.variant = true
				pivotCandidate.addSource(dnsSourceKindVariant, observed.root)
				observation := observed.observation
				pivotCandidate.observation = &observation
				pivotCandidate.rdap = observed.rdap
			}
		}

		if c.judge != nil {
			judgeCandidates := make([]ownership.Candidate, 0)
			for _, root := range sortedDNSPivotRoots(candidateByRoot) {
				pivotCandidate := candidateByRoot[root]
				if pivotCandidate == nil || pivotCandidate.observation == nil || !pivotCandidate.observation.live {
					continue
				}

				evidence, corroborated := buildDNSCandidateEvidence(pivotCandidate, baseline)
				if !corroborated {
					continue
				}

				judgeCandidates = append(judgeCandidates, ownership.Candidate{
					Root:     root,
					Evidence: evidence,
				})
			}

			metricsJudgeSubmitted = len(judgeCandidates)
			if len(judgeCandidates) > 0 {
				request := ownership.Request{
					Scenario:   "dns root variant pivot",
					Seed:       seed,
					Candidates: judgeCandidates,
				}
				decisions, err := c.judge.EvaluateCandidates(ctx, request)
				if err != nil {
					newErrors = append(newErrors, err)
				} else {
					lineage.RecordOwnershipJudgeEvaluation(pCtx, "dns_collector", request, decisions)
					for _, decision := range decisions {
						if !decision.Collect {
							continue
						}
						if !ownership.IsHighConfidence(decision.Confidence) {
							telemetry.Infof(ctx, "[DNS Collector] Skipping %s due to low-confidence judge decision %.2f.", decision.Root, decision.Confidence)
							continue
						}

						pivotCandidate, exists := candidateByRoot[decision.Root]
						if !exists || pivotCandidate == nil || pivotCandidate.observation == nil {
							continue
						}

						source := dnsAssetSourcePivot
						seedTag := dnsSeedTagRecordPivot
						evidenceKind := dnsSeedEvidenceKindRecordPivot
						if pivotCandidate.variant {
							source = dnsAssetSourceVariant
							seedTag = dnsSeedTagVariantPivot
							evidenceKind = dnsSeedEvidenceKindVariantPivot
						}

						newAssets = append(newAssets, domainAssetFromObservation(models.NewID("dom-dns-pivot"), enum.ID, decision.Root, source, *pivotCandidate.observation, pivotCandidate.rdap))

						discoveredSeed := discovery.BuildDiscoveredSeed(seed, decision.Root, seedTag)
						discoveredSeed.Evidence = append(discoveredSeed.Evidence, models.SeedEvidence{
							Source:     source,
							Kind:       evidenceKind,
							Value:      decision.Root,
							Confidence: decision.Confidence,
						})

						if pCtx.EnqueueSeedCandidate(discoveredSeed, models.SeedEvidence{
							Source:     "ownership_judge",
							Kind:       decision.Kind,
							Value:      decision.Root,
							Confidence: decision.Confidence,
							Reasoned:   true,
						}) {
							metricsPromoted++
							telemetry.Infof(ctx, "[DNS Collector] Promoted %s from judged DNS pivots.", decision.Root)
						}
					}
				}
			}
		}

		telemetry.Infof(ctx,
			"[DNS Collector] Seed %s summary: variant probes=%d live_variants=%d judge_submissions=%d promotions=%d",
			discovery.FirstNonEmpty(seed.CompanyName, seed.ID),
			metricsProbed,
			metricsLiveVariants,
			metricsJudgeSubmitted,
			metricsPromoted,
		)
	}

	pCtx.Lock()
	pCtx.Enumerations = append(pCtx.Enumerations, newEnums...)
	pCtx.Errors = append(pCtx.Errors, newErrors...)
	pCtx.Assets = append(pCtx.Assets, newAssets...)
	pCtx.Unlock()

	return pCtx, nil
}

type dnsLabelGroup struct {
	label string
	roots []string
}

func groupSeedRootsByLabel(seedRoots map[string]struct{}) []dnsLabelGroup {
	byLabel := make(map[string][]string)
	for root := range seedRoots {
		label := discovery.RegistrableLabel(root)
		if label == "" {
			continue
		}
		byLabel[label] = append(byLabel[label], root)
	}

	labels := make([]string, 0, len(byLabel))
	for label := range byLabel {
		labels = append(labels, label)
	}
	sort.Strings(labels)

	out := make([]dnsLabelGroup, 0, len(labels))
	for _, label := range labels {
		roots := append([]string(nil), byLabel[label]...)
		sort.Strings(roots)
		out = append(out, dnsLabelGroup{label: label, roots: roots})
	}

	return out
}

func (c *DNSCollector) buildVariantProbeRoots(seed models.Seed, knownRoots []string, seedRoots map[string]struct{}, candidateByRoot map[string]*dnsPivotCandidate) []string {
	if c.maxVariantProbesPerRoot <= 0 {
		return nil
	}

	label := ""
	for _, root := range knownRoots {
		label = discovery.RegistrableLabel(root)
		if label != "" {
			break
		}
	}
	if label == "" {
		return nil
	}

	prioritizedSuffixes := c.prioritizedVariantSuffixes(seed, knownRoots)
	probeRoots := make([]string, 0, minInt(c.maxVariantProbesPerRoot, len(prioritizedSuffixes)))
	seen := make(map[string]struct{})

	for _, suffix := range prioritizedSuffixes {
		candidateRoot := discovery.NormalizeDomainIdentifier(label + "." + suffix)
		if candidateRoot == "" {
			continue
		}
		if _, exists := seen[candidateRoot]; exists {
			continue
		}
		seen[candidateRoot] = struct{}{}

		if _, exists := seedRoots[candidateRoot]; exists {
			continue
		}
		if discovery.RegistrableDomain(candidateRoot) != candidateRoot {
			continue
		}

		if existing, exists := candidateByRoot[candidateRoot]; exists {
			existing.variant = true
			existing.addSource(dnsSourceKindVariant, candidateRoot)
			continue
		}

		probeRoots = append(probeRoots, candidateRoot)
		if len(probeRoots) >= c.maxVariantProbesPerRoot {
			break
		}
	}

	return probeRoots
}

func (c *DNSCollector) prioritizedVariantSuffixes(seed models.Seed, knownRoots []string) []string {
	available := c.variantSuffixes
	if len(available) == 0 {
		available = discovery.ICANNPublicSuffixes()
	}

	currentSuffixes := make(map[string]struct{})
	familyHints := make(map[string]struct{})
	for _, root := range knownRoots {
		suffix := publicSuffix(root)
		if suffix == "" {
			continue
		}
		currentSuffixes[suffix] = struct{}{}
		familyHints[discovery.LastLabel(suffix)] = struct{}{}
	}
	for _, domain := range seed.Domains {
		suffix := publicSuffix(domain)
		if suffix == "" {
			continue
		}
		currentSuffixes[suffix] = struct{}{}
		familyHints[discovery.LastLabel(suffix)] = struct{}{}
	}

	ordered := make([]string, 0, len(available))
	seen := make(map[string]struct{}, len(available))
	appendSuffix := func(suffix string) {
		suffix = discovery.NormalizeDomainIdentifier(suffix)
		if suffix == "" {
			return
		}
		if _, exists := seen[suffix]; exists {
			return
		}
		seen[suffix] = struct{}{}
		ordered = append(ordered, suffix)
	}

	for _, suffix := range available {
		if _, exists := currentSuffixes[suffix]; exists {
			appendSuffix(suffix)
		}
	}
	for _, suffix := range available {
		last := discovery.LastLabel(suffix)
		if _, exists := familyHints[last]; exists {
			appendSuffix(suffix)
		}
	}
	for _, suffix := range c.genericVariantSuffixes {
		appendSuffix(suffix)
	}
	for _, suffix := range available {
		appendSuffix(suffix)
	}

	return ordered
}

func publicSuffix(domain string) string {
	domain = discovery.NormalizeDomainIdentifier(domain)
	if domain == "" {
		return ""
	}

	suffix, _ := publicsuffix.PublicSuffix(domain)
	return discovery.NormalizeDomainIdentifier(suffix)
}

func (c *DNSCollector) observeDomain(ctx context.Context, domain string) (dnsObservation, []dnsLookupIssue) {
	domain = discovery.NormalizeDomainIdentifier(domain)
	observation := dnsObservation{
		domain: domain,
		root:   discovery.RegistrableDomain(domain),
	}
	lookupIssues := make([]dnsLookupIssue, 0, 5)

	ips, err := c.lookupIPsWithTimeout(ctx, domain)
	if err != nil {
		lookupIssues = append(lookupIssues, dnsLookupIssue{
			recordKind: "A/AAAA",
			domain:     domain,
			err:        err,
		})
	}
	for _, ip := range ips {
		recordType := "A"
		if net.ParseIP(ip).To4() == nil {
			recordType = "AAAA"
		}
		observation.ips = append(observation.ips, ip)
		observation.records = append(observation.records, models.DNSRecord{
			Type:  recordType,
			Value: ip,
		})
	}
	if len(observation.ips) > 0 {
		observation.live = true
	}

	mxs, err := c.lookupMXWithTimeout(ctx, domain)
	if err != nil {
		lookupIssues = append(lookupIssues, dnsLookupIssue{
			recordKind: "MX",
			domain:     domain,
			err:        err,
		})
	}
	for _, mx := range mxs {
		host := discovery.NormalizeDomainIdentifier(mx.Host)
		if host == "" {
			continue
		}
		observation.mxHosts = append(observation.mxHosts, host)
		observation.records = append(observation.records, models.DNSRecord{
			Type:  "MX",
			Value: host,
		})
	}
	if len(observation.mxHosts) > 0 {
		observation.live = true
	}

	txts, err := c.lookupTXTWithTimeout(ctx, domain)
	if err != nil {
		lookupIssues = append(lookupIssues, dnsLookupIssue{
			recordKind: "TXT",
			domain:     domain,
			err:        err,
		})
	}
	for _, txt := range txts {
		txt = strings.TrimSpace(txt)
		if txt == "" {
			continue
		}
		observation.txtValues = append(observation.txtValues, txt)
		observation.records = append(observation.records, models.DNSRecord{
			Type:  "TXT",
			Value: txt,
		})
	}

	nss, err := c.lookupNSWithTimeout(ctx, domain)
	if err != nil {
		lookupIssues = append(lookupIssues, dnsLookupIssue{
			recordKind: "NS",
			domain:     domain,
			err:        err,
		})
	}
	for _, ns := range nss {
		host := discovery.NormalizeDomainIdentifier(ns.Host)
		if host == "" {
			continue
		}
		observation.nsHosts = append(observation.nsHosts, host)
		observation.records = append(observation.records, models.DNSRecord{
			Type:  "NS",
			Value: host,
		})
	}
	if len(observation.nsHosts) > 0 {
		observation.live = true
	}

	cname, err := c.lookupCNAMEWithTimeout(ctx, domain)
	if err != nil {
		lookupIssues = append(lookupIssues, dnsLookupIssue{
			recordKind: "CNAME",
			domain:     domain,
			err:        err,
		})
	}
	cname = discovery.NormalizeDomainIdentifier(cname)
	if cname != "" && cname != domain {
		observation.cname = cname
		observation.records = append(observation.records, models.DNSRecord{
			Type:  "CNAME",
			Value: cname,
		})
	}

	observation.ips = discovery.UniqueLowerStrings(observation.ips)
	observation.mxHosts = discovery.UniqueLowerStrings(observation.mxHosts)
	observation.nsHosts = discovery.UniqueLowerStrings(observation.nsHosts)
	observation.txtValues = discovery.UniqueLowerStrings(observation.txtValues)
	observation.txtDomains = discovery.ExtractStructuredTXTDomainCandidates(observation.txtValues...)
	observation.txtRoots = rootsFromDomains(observation.txtDomains)
	observation.mxRoots = rootsFromDomains(observation.mxHosts)
	observation.nsRoots = rootsFromDomains(observation.nsHosts)

	return observation, lookupIssues
}

func (o dnsObservation) domainCandidates() []dnsDomainCandidate {
	candidates := make([]dnsDomainCandidate, 0)
	addCandidates := func(values []string, sourceKind string) {
		for _, value := range values {
			for _, candidate := range discovery.ExtractDNSDomainCandidates(value) {
				root := discovery.RegistrableDomain(candidate)
				if root == "" {
					continue
				}
				candidates = append(candidates, dnsDomainCandidate{
					host:       candidate,
					root:       root,
					sourceKind: sourceKind,
				})
			}
		}
	}

	addCandidates(o.mxHosts, dnsSourceKindMX)
	addCandidates(o.nsHosts, dnsSourceKindNS)
	if o.cname != "" {
		addCandidates([]string{o.cname}, dnsSourceKindCNAME)
	}
	addCandidates(o.txtDomains, dnsSourceKindTXT)

	seen := make(map[string]struct{}, len(candidates))
	unique := make([]dnsDomainCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		key := candidate.sourceKind + "|" + candidate.host + "|" + candidate.root
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, candidate)
	}

	return unique
}

func rootsFromDomains(domains []string) []string {
	roots := make([]string, 0, len(domains))
	for _, domain := range domains {
		root := discovery.RegistrableDomain(domain)
		if root == "" {
			continue
		}
		roots = append(roots, root)
	}
	return discovery.UniqueLowerStrings(roots)
}

func (c *DNSCollector) observeCandidateRoots(ctx context.Context, roots []string, liveCap int, probeKind string) ([]observedDNSCandidate, []error) {
	if len(roots) == 0 {
		return nil, nil
	}
	if liveCap <= 0 {
		return nil, nil
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, maxInt(1, c.variantProbeConcurrency))

	var mu sync.Mutex
	live := make([]observedDNSCandidate, 0)
	errs := make([]error, 0)
	lookupAggregator := dnsLookupIssueAggregator{
		counts:  make(map[string]int),
		samples: make(map[string]error),
	}

	for idx, root := range roots {
		wg.Add(1)
		sem <- struct{}{}

		go func(index int, candidateRoot string) {
			defer wg.Done()
			defer func() { <-sem }()

			observation, lookupIssues := c.observeDomain(ctx, candidateRoot)
			if len(lookupIssues) > 0 {
				mu.Lock()
				for _, issue := range lookupIssues {
					lookupAggregator.add(issue)
				}
				mu.Unlock()
			}
			if !observation.live {
				return
			}

			rdapData, err := c.lookupRDAPWithTimeout(ctx, candidateRoot)
			if err != nil && err != registration.ErrUnsupportedRegistrationData {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}

			mu.Lock()
			live = append(live, observedDNSCandidate{
				root:        candidateRoot,
				observation: observation,
				rdap:        rdapData,
				index:       index,
			})
			mu.Unlock()
		}(idx, root)
	}

	wg.Wait()
	sort.SliceStable(live, func(i, j int) bool {
		return live[i].index < live[j].index
	})
	if len(live) > liveCap {
		live = append([]observedDNSCandidate(nil), live[:liveCap]...)
	}
	lookupAggregator.log(ctx, probeKind)

	return live, errs
}

func (c *DNSCollector) lookupIPsWithTimeout(ctx context.Context, domain string) ([]string, error) {
	if c.lookupIPs == nil {
		return nil, nil
	}
	lookupCtx, cancel := context.WithTimeout(ctx, c.lookupTimeout)
	defer cancel()

	ips, err := c.lookupIPs(lookupCtx, domain)
	if err != nil {
		return nil, err
	}

	values := make([]string, 0, len(ips))
	for _, ip := range ips {
		if parsed := net.ParseIP(ip.String()); parsed != nil {
			values = append(values, parsed.String())
		}
	}
	return discovery.UniqueLowerStrings(values), nil
}

func (c *DNSCollector) lookupMXWithTimeout(ctx context.Context, domain string) ([]*net.MX, error) {
	if c.lookupMX == nil {
		return nil, nil
	}
	lookupCtx, cancel := context.WithTimeout(ctx, c.lookupTimeout)
	defer cancel()
	return c.lookupMX(lookupCtx, domain)
}

func (c *DNSCollector) lookupTXTWithTimeout(ctx context.Context, domain string) ([]string, error) {
	if c.lookupTXT == nil {
		return nil, nil
	}
	lookupCtx, cancel := context.WithTimeout(ctx, c.lookupTimeout)
	defer cancel()
	return c.lookupTXT(lookupCtx, domain)
}

func (c *DNSCollector) lookupNSWithTimeout(ctx context.Context, domain string) ([]*net.NS, error) {
	if c.lookupNS == nil {
		return nil, nil
	}
	lookupCtx, cancel := context.WithTimeout(ctx, c.lookupTimeout)
	defer cancel()
	return c.lookupNS(lookupCtx, domain)
}

func (c *DNSCollector) lookupCNAMEWithTimeout(ctx context.Context, domain string) (string, error) {
	if c.lookupCNAME == nil {
		return "", nil
	}
	lookupCtx, cancel := context.WithTimeout(ctx, c.lookupTimeout)
	defer cancel()
	return c.lookupCNAME(lookupCtx, domain)
}

func (c *DNSCollector) lookupRDAPWithTimeout(ctx context.Context, domain string) (*models.RDAPData, error) {
	if c.lookupRDAP == nil {
		return nil, nil
	}
	domain = discovery.NormalizeDomainIdentifier(domain)
	if domain == "" {
		return nil, nil
	}

	lookupCtx, cancel := context.WithTimeout(ctx, c.rdapTimeout)
	defer cancel()
	return c.lookupRDAP(lookupCtx, domain)
}

func domainAssetFromObservation(id string, enumID string, identifier string, source string, observation dnsObservation, rdap *models.RDAPData) models.Asset {
	domainDetails := &models.DomainDetails{
		Records: append([]models.DNSRecord(nil), observation.records...),
	}
	if rdap != nil {
		domainDetails.RDAP = rdap
	}

	return models.Asset{
		ID:            id,
		EnumerationID: enumID,
		Type:          models.AssetTypeDomain,
		Identifier:    identifier,
		Source:        source,
		DiscoveryDate: time.Now(),
		DomainDetails: domainDetails,
	}
}

func ensureDNSPivotCandidate(candidateByRoot map[string]*dnsPivotCandidate, root string) *dnsPivotCandidate {
	root = discovery.NormalizeDomainIdentifier(root)
	if root == "" {
		return nil
	}

	if existing, exists := candidateByRoot[root]; exists {
		return existing
	}

	candidate := &dnsPivotCandidate{
		root:        root,
		sourceKinds: make(map[string]struct{}),
	}
	candidateByRoot[root] = candidate
	return candidate
}

func (c *dnsPivotCandidate) addSource(kind string, sample string) {
	if c == nil {
		return
	}
	kind = strings.TrimSpace(strings.ToLower(kind))
	if kind != "" {
		c.sourceKinds[kind] = struct{}{}
	}

	sample = discovery.NormalizeDomainIdentifier(sample)
	if sample == "" {
		return
	}
	for _, existing := range c.sampleHosts {
		if existing == sample {
			return
		}
	}
	if len(c.sampleHosts) < 3 {
		c.sampleHosts = append(c.sampleHosts, sample)
	}
}

func sortedDNSPivotRoots(candidateByRoot map[string]*dnsPivotCandidate) []string {
	roots := make([]string, 0, len(candidateByRoot))
	for root := range candidateByRoot {
		roots = append(roots, root)
	}
	sort.Strings(roots)
	return roots
}

func newDNSSeedBaseline(seed models.Seed) *dnsSeedBaseline {
	baseline := &dnsSeedBaseline{
		seedRoots:      make(map[string]struct{}),
		ips:            make(map[string]struct{}),
		nsRoots:        make(map[string]struct{}),
		mailRoots:      make(map[string]struct{}),
		txtRoots:       make(map[string]struct{}),
		cnameRoots:     make(map[string]struct{}),
		rdapOrgs:       make(map[string]struct{}),
		rdapEmailRoots: make(map[string]struct{}),
	}

	for _, domain := range seed.Domains {
		if root := discovery.RegistrableDomain(domain); root != "" {
			baseline.seedRoots[root] = struct{}{}
		}
	}

	return baseline
}

func (b *dnsSeedBaseline) addObservation(_ string, observation dnsObservation, rdap *models.RDAPData) {
	if b == nil {
		return
	}

	for _, ip := range observation.ips {
		b.ips[ip] = struct{}{}
	}
	for _, root := range observation.nsRoots {
		b.nsRoots[root] = struct{}{}
	}
	for _, root := range observation.mxRoots {
		b.mailRoots[root] = struct{}{}
	}
	for _, root := range observation.txtRoots {
		b.txtRoots[root] = struct{}{}
	}
	if observation.cname != "" {
		if root := discovery.RegistrableDomain(observation.cname); root != "" {
			b.cnameRoots[root] = struct{}{}
		}
	}

	if rdap == nil {
		return
	}
	if normalized := discovery.NormalizeOrganization(rdap.RegistrantOrg); normalized != "" {
		b.rdapOrgs[normalized] = struct{}{}
	}
	if emailRoot := rdapEmailRoot(rdap.RegistrantEmail); emailRoot != "" {
		b.rdapEmailRoots[emailRoot] = struct{}{}
	}
	for _, nameserver := range rdap.NameServers {
		if root := discovery.RegistrableDomain(nameserver); root != "" {
			b.nsRoots[root] = struct{}{}
		}
	}
}

func rdapEmailRoot(email string) string {
	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" {
		return ""
	}

	parts := strings.Split(email, "@")
	if len(parts) != 2 {
		return ""
	}

	return discovery.RegistrableDomain(parts[1])
}

func buildDNSCandidateEvidence(candidate *dnsPivotCandidate, baseline *dnsSeedBaseline) ([]ownership.EvidenceItem, bool) {
	if candidate == nil || baseline == nil || candidate.observation == nil {
		return nil, false
	}

	evidence := make([]ownership.EvidenceItem, 0, 8)
	corroborated := false

	if candidate.variant {
		evidence = append(evidence, ownership.EvidenceItem{
			Kind:    "dns_variant",
			Summary: fmt.Sprintf("Live DNS variant for registrable label %q under alternate public suffix %q", discovery.RegistrableLabel(candidate.root), publicSuffix(candidate.root)),
		})
	}

	if _, exists := candidate.sourceKinds[dnsSourceKindNS]; exists {
		evidence = append(evidence, ownership.EvidenceItem{
			Kind:    "ns_root_reference",
			Summary: fmt.Sprintf("Observed as an NS-derived registrable root from the seed DNS records: %s", candidate.root),
		})
	}
	if _, exists := candidate.sourceKinds[dnsSourceKindMX]; exists {
		evidence = append(evidence, ownership.EvidenceItem{
			Kind:    "mx_root_reference",
			Summary: fmt.Sprintf("Observed as an MX-derived registrable root from the seed DNS records: %s", candidate.root),
		})
	}
	if _, exists := candidate.sourceKinds[dnsSourceKindTXT]; exists {
		evidence = append(evidence, ownership.EvidenceItem{
			Kind:    "txt_root_reference",
			Summary: fmt.Sprintf("Observed in structured TXT records for the seed DNS: %s", candidate.root),
		})
	}
	if _, exists := candidate.sourceKinds[dnsSourceKindCNAME]; exists {
		evidence = append(evidence, ownership.EvidenceItem{
			Kind:    "cname_root_reference",
			Summary: fmt.Sprintf("Observed as a CNAME target root from the seed DNS records: %s", candidate.root),
		})
	}

	ipOverlap := overlapWithSet(candidate.observation.ips, baseline.ips)
	if len(ipOverlap) > 0 {
		evidence = append(evidence, ownership.EvidenceItem{
			Kind:    "ip_overlap",
			Summary: fmt.Sprintf("Shares resolved IPs with the seed baseline: %s", strings.Join(ipOverlap, ", ")),
		})
		corroborated = true
	}

	nsOverlap := overlapWithSet(candidate.observation.nsRoots, baseline.nsRoots)
	if len(nsOverlap) > 0 {
		evidence = append(evidence, ownership.EvidenceItem{
			Kind:    "nameserver_overlap",
			Summary: fmt.Sprintf("Shares registrable nameserver roots with the seed baseline: %s", strings.Join(nsOverlap, ", ")),
		})
		corroborated = true
	}

	mailOverlap := overlapWithSet(candidate.observation.mxRoots, baseline.mailRoots)
	if len(mailOverlap) > 0 {
		evidence = append(evidence, ownership.EvidenceItem{
			Kind:    "mx_overlap",
			Summary: fmt.Sprintf("Shares MX-derived registrable roots with the seed baseline: %s", strings.Join(mailOverlap, ", ")),
		})
		corroborated = true
	}

	txtOverlap := overlapWithSet(candidate.observation.txtRoots, baseline.txtRoots)
	if len(txtOverlap) > 0 {
		evidence = append(evidence, ownership.EvidenceItem{
			Kind:    "txt_overlap",
			Summary: fmt.Sprintf("Shares TXT-derived registrable roots with the seed baseline: %s", strings.Join(txtOverlap, ", ")),
		})
		corroborated = true
	}

	if candidate.rdap != nil {
		if org := discovery.NormalizeOrganization(candidate.rdap.RegistrantOrg); org != "" {
			if _, exists := baseline.rdapOrgs[org]; exists {
				evidence = append(evidence, ownership.EvidenceItem{
					Kind:    "registrant_org_match",
					Summary: fmt.Sprintf("Registrant organization %q matches the seed baseline", candidate.rdap.RegistrantOrg),
				})
				corroborated = true
			}
		}

		if emailRoot := rdapEmailRoot(candidate.rdap.RegistrantEmail); emailRoot != "" {
			if _, exists := baseline.rdapEmailRoots[emailRoot]; exists {
				evidence = append(evidence, ownership.EvidenceItem{
					Kind:    "registrant_email_root_match",
					Summary: fmt.Sprintf("Registrant email root %q matches the seed baseline", emailRoot),
				})
				corroborated = true
			}
		}
	}

	recordKinds := make([]string, 0, len(candidate.observation.records))
	seenKinds := make(map[string]struct{})
	for _, record := range candidate.observation.records {
		if _, exists := seenKinds[record.Type]; exists {
			continue
		}
		seenKinds[record.Type] = struct{}{}
		recordKinds = append(recordKinds, record.Type)
	}
	sort.Strings(recordKinds)
	if len(recordKinds) > 0 {
		evidence = append(evidence, ownership.EvidenceItem{
			Kind:    "live_dns",
			Summary: fmt.Sprintf("Live DNS records observed for the candidate root: %s", strings.Join(recordKinds, ", ")),
		})
	}
	if len(candidate.sampleHosts) > 0 {
		evidence = append(evidence, ownership.EvidenceItem{
			Kind:    "dns_samples",
			Summary: fmt.Sprintf("Sample DNS-derived hostnames or roots: %s", strings.Join(candidate.sampleHosts, ", ")),
		})
	}

	return evidence, corroborated
}

func overlapWithSet(values []string, target map[string]struct{}) []string {
	if len(values) == 0 || len(target) == 0 {
		return nil
	}

	overlap := make([]string, 0)
	seen := make(map[string]struct{})
	for _, value := range values {
		value = discovery.NormalizeDomainIdentifier(value)
		if value == "" {
			continue
		}
		if _, exists := target[value]; !exists {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		overlap = append(overlap, value)
	}
	sort.Strings(overlap)
	return overlap
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func (i dnsLookupIssue) asError() error {
	return fmt.Errorf("lookup %s %s: %w", i.recordKind, i.domain, i.err)
}

func (a *dnsLookupIssueAggregator) add(issue dnsLookupIssue) {
	if a == nil {
		return
	}

	key := strings.TrimSpace(strings.ToUpper(issue.recordKind))
	if key == "" {
		key = "UNKNOWN"
	}
	a.counts[key]++
	if _, exists := a.samples[key]; !exists {
		a.samples[key] = issue.asError()
	}
}

func (a *dnsLookupIssueAggregator) log(ctx context.Context, probeKind string) {
	if a == nil || len(a.counts) == 0 {
		return
	}

	keys := make([]string, 0, len(a.counts))
	for key := range a.counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		telemetry.Infof(ctx,
			"[DNS Collector] %s DNS lookup errors for %s probes: count=%d sample=%v",
			key,
			probeKind,
			a.counts[key],
			a.samples[key],
		)
	}
}
