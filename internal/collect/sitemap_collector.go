package collect

import (
	"bufio"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"asset-discovery/internal/discovery"
	"asset-discovery/internal/fetchutil"
	"asset-discovery/internal/models"
	"asset-discovery/internal/ownership"
	"asset-discovery/internal/tracing/lineage"
	"asset-discovery/internal/tracing/telemetry"
)

const (
	defaultMaxSitemapDocsPerSeed         = 10
	defaultMaxSitemapURLsPerSeed         = 500
	defaultMaxSitemapJudgeCandidates     = 25
	defaultMaxSitemapSamplesPerRoot      = 3
	sitemapSeedEvidenceKindRootReference = "sitemap_root_reference"
)

type sitemapFetchTarget struct {
	URL  string
	Kind string
}

type sitemapRootCandidate struct {
	root      string
	documents []string
	samples   []string
	docSet    map[string]struct{}
	sampleSet map[string]struct{}
}

func newSitemapRootCandidate(root string) *sitemapRootCandidate {
	return &sitemapRootCandidate{
		root:      root,
		docSet:    make(map[string]struct{}),
		sampleSet: make(map[string]struct{}),
	}
}

func (c *sitemapRootCandidate) addDocument(raw string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return
	}
	if _, exists := c.docSet[raw]; exists {
		return
	}
	c.docSet[raw] = struct{}{}
	c.documents = append(c.documents, raw)
}

func (c *sitemapRootCandidate) addSample(raw string, limit int) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return
	}
	if _, exists := c.sampleSet[raw]; exists {
		return
	}
	c.sampleSet[raw] = struct{}{}
	if len(c.samples) >= limit {
		return
	}
	c.samples = append(c.samples, raw)
}

type SitemapCollector struct {
	client                    *http.Client
	buildTargets              func(domain string) []sitemapFetchTarget
	judge                     ownership.Judge
	maxSitemapDocsPerSeed     int
	maxURLsPerSeed            int
	maxJudgeCandidatesPerSeed int
	maxSamplesPerRoot         int
	now                       func() time.Time
}

type SitemapCollectorOption func(*SitemapCollector)

func WithSitemapClient(client *http.Client) SitemapCollectorOption {
	return func(c *SitemapCollector) {
		if client != nil {
			c.client = client
		}
	}
}

func WithSitemapJudge(judge ownership.Judge) SitemapCollectorOption {
	return func(c *SitemapCollector) {
		c.judge = judge
	}
}

func NewSitemapCollector(options ...SitemapCollectorOption) *SitemapCollector {
	collector := &SitemapCollector{
		client: &http.Client{Timeout: 20 * time.Second},
		buildTargets: func(domain string) []sitemapFetchTarget {
			domain = discovery.NormalizeDomainIdentifier(domain)
			return []sitemapFetchTarget{
				{URL: "https://" + domain + "/robots.txt", Kind: "robots"},
				{URL: "http://" + domain + "/robots.txt", Kind: "robots"},
				{URL: "https://" + domain + "/sitemap.xml", Kind: "sitemap"},
				{URL: "http://" + domain + "/sitemap.xml", Kind: "sitemap"},
			}
		},
		judge:                     ownership.NewDefaultJudge(),
		maxSitemapDocsPerSeed:     defaultMaxSitemapDocsPerSeed,
		maxURLsPerSeed:            defaultMaxSitemapURLsPerSeed,
		maxJudgeCandidatesPerSeed: defaultMaxSitemapJudgeCandidates,
		maxSamplesPerRoot:         defaultMaxSitemapSamplesPerRoot,
		now:                       time.Now,
	}

	for _, option := range options {
		if option != nil {
			option(collector)
		}
	}

	return collector
}

func (c *SitemapCollector) Process(ctx context.Context, pCtx *models.PipelineContext) (*models.PipelineContext, error) {
	telemetry.Info(ctx, "[Sitemap Collector] Processing seeds...")

	var newEnums []models.Enumeration
	var newErrors []error
	var newAssets []models.Asset

	for _, seed := range pCtx.CollectionSeeds() {
		enum := models.Enumeration{
			ID:        models.NewID("enum-sitemap"),
			SeedID:    seed.ID,
			Status:    "running",
			CreatedAt: c.now(),
			StartedAt: c.now(),
		}
		newEnums = append(newEnums, enum)

		scopeRoots := make(map[string]struct{}, len(seed.Domains))
		for _, domain := range seed.Domains {
			if root := discovery.RegistrableDomain(domain); root != "" {
				scopeRoots[root] = struct{}{}
			}
		}
		if len(scopeRoots) == 0 {
			continue
		}

		sameRootHosts := make(map[string]struct{})
		candidateByRoot := make(map[string]*sitemapRootCandidate)
		queuedDocs := make([]string, 0, c.maxSitemapDocsPerSeed)
		queuedDocSet := make(map[string]struct{})
		seenDocs := make(map[string]struct{})
		seenPageURLs := make(map[string]struct{})

		enqueueSitemapDoc := func(raw string, base *url.URL) {
			normalized := normalizeSitemapURL(raw, base)
			if normalized == "" {
				return
			}
			if _, exists := seenDocs[normalized]; exists {
				return
			}
			if _, exists := queuedDocSet[normalized]; exists {
				return
			}
			if len(seenDocs)+len(queuedDocSet) >= c.maxSitemapDocsPerSeed {
				return
			}
			queuedDocSet[normalized] = struct{}{}
			queuedDocs = append(queuedDocs, normalized)
		}

		addObservedURL := func(rawURL string, sitemapDoc string) {
			normalizedURL := normalizeSitemapURL(rawURL, nil)
			if normalizedURL == "" {
				return
			}
			if _, exists := seenPageURLs[normalizedURL]; exists {
				return
			}
			if len(seenPageURLs) >= c.maxURLsPerSeed {
				return
			}
			seenPageURLs[normalizedURL] = struct{}{}

			host := sitemapURLHost(normalizedURL)
			if host == "" {
				return
			}

			root := discovery.RegistrableDomain(host)
			if root == "" {
				return
			}

			if _, inScope := scopeRoots[root]; inScope {
				sameRootHosts[host] = struct{}{}
				return
			}

			if c.judge == nil {
				return
			}

			candidate, exists := candidateByRoot[root]
			if !exists {
				if len(candidateByRoot) >= c.maxJudgeCandidatesPerSeed {
					return
				}
				candidate = newSitemapRootCandidate(root)
				candidateByRoot[root] = candidate
			}

			candidate.addDocument(sitemapDoc)
			candidate.addSample(normalizedURL, c.maxSamplesPerRoot)
		}

		for _, domain := range seed.Domains {
			for _, target := range c.buildTargets(domain) {
				switch target.Kind {
				case "robots":
					body, finalURL, err := c.fetchURL(ctx, target.URL)
					if err != nil {
						newErrors = append(newErrors, err)
						continue
					}
					for _, sitemapURL := range parseRobotsSitemapURLs(body, finalURL) {
						enqueueSitemapDoc(sitemapURL, finalURL)
					}
				case "sitemap":
					enqueueSitemapDoc(target.URL, nil)
				}
			}
		}

		for len(queuedDocs) > 0 && len(seenDocs) < c.maxSitemapDocsPerSeed {
			docURL := queuedDocs[0]
			queuedDocs = queuedDocs[1:]
			delete(queuedDocSet, docURL)

			body, finalURL, err := c.fetchURL(ctx, docURL)
			if err != nil {
				newErrors = append(newErrors, err)
				continue
			}

			normalizedDoc := normalizeSitemapURL(finalURL.String(), nil)
			if normalizedDoc == "" {
				normalizedDoc = docURL
			}
			if _, exists := seenDocs[normalizedDoc]; exists {
				continue
			}
			seenDocs[normalizedDoc] = struct{}{}

			kind, locs, err := parseSitemapXML(body)
			if err != nil {
				newErrors = append(newErrors, fmt.Errorf("parse sitemap %s: %w", normalizedDoc, err))
				continue
			}

			switch kind {
			case "sitemapindex":
				for _, loc := range locs {
					enqueueSitemapDoc(loc, finalURL)
				}
			case "urlset":
				for _, loc := range locs {
					addObservedURL(normalizeSitemapURL(loc, finalURL), normalizedDoc)
				}
			}
		}

		for _, host := range sortedStringKeys(sameRootHosts) {
			newAssets = append(newAssets, models.Asset{
				ID:            models.NewID("dom-sitemap"),
				EnumerationID: enum.ID,
				Type:          models.AssetTypeDomain,
				Identifier:    host,
				Source:        "sitemap_collector",
				DiscoveryDate: c.now(),
				DomainDetails: &models.DomainDetails{},
			})
		}

		if c.judge == nil || len(candidateByRoot) == 0 {
			continue
		}

		roots := make([]string, 0, len(candidateByRoot))
		for root := range candidateByRoot {
			roots = append(roots, root)
		}
		sort.Strings(roots)

		judgeCandidates := make([]ownership.Candidate, 0, len(roots))
		for _, root := range roots {
			candidate := candidateByRoot[root]
			if candidate == nil {
				continue
			}

			evidence := make([]ownership.EvidenceItem, 0, 2)
			if len(candidate.documents) > 0 {
				evidence = append(evidence, ownership.EvidenceItem{
					Kind:    "sitemap_document",
					Summary: fmt.Sprintf("Referenced in sitemap document(s): %s", strings.Join(candidate.documents, ", ")),
				})
			}
			if len(candidate.samples) > 0 {
				evidence = append(evidence, ownership.EvidenceItem{
					Kind:    "sitemap_page_samples",
					Summary: fmt.Sprintf("Sample sitemap URLs under this root: %s", strings.Join(candidate.samples, ", ")),
				})
			}
			if len(evidence) == 0 {
				continue
			}

			judgeCandidates = append(judgeCandidates, ownership.Candidate{
				Root:     root,
				Evidence: evidence,
			})
		}

		if len(judgeCandidates) == 0 {
			continue
		}

		request := ownership.Request{
			Scenario:   "sitemap host pivot",
			Seed:       seed,
			Candidates: judgeCandidates,
		}
		decisions, err := c.judge.EvaluateCandidates(ctx, request)
		if err != nil {
			newErrors = append(newErrors, err)
			continue
		}
		lineage.RecordOwnershipJudgeEvaluation(pCtx, "sitemap_collector", request, decisions)

		for _, decision := range decisions {
			if !decision.Collect {
				continue
			}
			if !ownership.IsHighConfidence(decision.Confidence) {
				telemetry.Infof(ctx, "[Sitemap Collector] Skipping %s due to low-confidence judge decision %.2f.", decision.Root, decision.Confidence)
				continue
			}

			root := discovery.RegistrableDomain(decision.Root)
			if root == "" {
				continue
			}

			newAssets = append(newAssets, models.Asset{
				ID:            models.NewID("dom-sitemap"),
				EnumerationID: enum.ID,
				Type:          models.AssetTypeDomain,
				Identifier:    root,
				Source:        "sitemap_collector",
				DiscoveryDate: c.now(),
				DomainDetails: &models.DomainDetails{},
			})

			discoveredSeed := discovery.BuildDiscoveredSeed(seed, root, "sitemap-pivot")
			discoveredSeed.Evidence = append(discoveredSeed.Evidence, models.SeedEvidence{
				Source:     "sitemap_collector",
				Kind:       sitemapSeedEvidenceKindRootReference,
				Value:      root,
				Confidence: decision.Confidence,
			})

			if pCtx.EnqueueSeedCandidate(discoveredSeed, models.SeedEvidence{
				Source:     "ownership_judge",
				Kind:       decision.Kind,
				Value:      root,
				Confidence: decision.Confidence,
				Reasoned:   true,
			}) {
				telemetry.Infof(ctx, "[Sitemap Collector] Promoted %s from sitemap host pivots.", root)
			}
		}
	}

	pCtx.Lock()
	pCtx.Enumerations = append(pCtx.Enumerations, newEnums...)
	pCtx.Errors = append(pCtx.Errors, newErrors...)
	pCtx.Assets = append(pCtx.Assets, newAssets...)
	pCtx.Unlock()

	return pCtx, nil
}

func (c *SitemapCollector) fetchURL(ctx context.Context, rawURL string) ([]byte, *url.URL, error) {
	resp, err := fetchutil.DoRequest(ctx, c.client, func(ctx context.Context) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "Asset-Discovery-Bot/1.0")
		return req, nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("fetch %s: %w", rawURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.Request.URL, fmt.Errorf("fetch %s: unexpected status %d", rawURL, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return nil, resp.Request.URL, fmt.Errorf("read %s: %w", rawURL, err)
	}

	return body, resp.Request.URL, nil
}

func parseRobotsSitemapURLs(body []byte, base *url.URL) []string {
	scanner := bufio.NewScanner(bytes.NewReader(body))
	urls := make([]string, 0)
	seen := make(map[string]struct{})

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		if !strings.HasPrefix(lower, "sitemap:") {
			continue
		}
		value := strings.TrimSpace(line[len("sitemap:"):])
		normalized := normalizeSitemapURL(value, base)
		if normalized == "" {
			continue
		}
		key := sitemapURLDedupKey(normalized)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		urls = append(urls, normalized)
	}

	return urls
}

func parseSitemapXML(body []byte) (string, []string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(body))
	root := ""
	locs := make([]string, 0)

	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", nil, err
		}

		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}

		if root == "" {
			root = strings.ToLower(strings.TrimSpace(start.Name.Local))
		}

		if !strings.EqualFold(start.Name.Local, "loc") {
			continue
		}

		var value string
		if err := decoder.DecodeElement(&value, &start); err != nil {
			return root, nil, err
		}
		value = strings.TrimSpace(value)
		if value != "" {
			locs = append(locs, value)
		}
	}

	switch root {
	case "sitemapindex", "urlset":
		return root, locs, nil
	case "":
		return "", nil, fmt.Errorf("empty sitemap document")
	default:
		return "", nil, fmt.Errorf("unsupported sitemap root %q", root)
	}
}

func normalizeSitemapURL(raw string, base *url.URL) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	var (
		parsed *url.URL
		err    error
	)
	if base != nil {
		parsed, err = base.Parse(raw)
	} else {
		parsed, err = url.Parse(raw)
	}
	if err != nil || parsed == nil {
		return ""
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}

	parsed.Fragment = ""
	return parsed.String()
}

func sitemapURLDedupKey(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil {
		return raw
	}

	parsed.Scheme = strings.ToLower(parsed.Scheme)
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return raw
	}
	if port := parsed.Port(); port != "" {
		parsed.Host = net.JoinHostPort(host, port)
	} else {
		parsed.Host = host
	}

	return parsed.String()
}

func sitemapURLHost(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err == nil {
		if host := discovery.NormalizeDomainIdentifier(parsed.Hostname()); host != "" {
			return host
		}
	}

	candidates := discovery.ExtractDomainCandidates(raw)
	if len(candidates) == 0 {
		return ""
	}

	return candidates[0]
}

func sortedStringKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for value := range values {
		keys = append(keys, value)
	}
	sort.Strings(keys)
	return keys
}
