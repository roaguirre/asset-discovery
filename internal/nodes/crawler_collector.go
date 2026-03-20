package nodes

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"

	"golang.org/x/net/html"

	"asset-discovery/internal/discovery"
	"asset-discovery/internal/fetchutil"
	"asset-discovery/internal/models"
	"asset-discovery/internal/ownership"
)

const (
	defaultCrawlerMaxDepth          = 2
	defaultCrawlerMaxPagesPerSeed   = 12
	defaultCrawlerMaxLinksPerPage   = 128
	defaultCrawlerMaxSamplesPerRoot = 5
	maxCrawlerJudgeBatchSize        = 20
	crawlerBodyLimit                = 1024 * 1024
)

var crawlerSkipExtensions = map[string]struct{}{
	".7z": {}, ".avi": {}, ".bz2": {}, ".css": {}, ".eot": {}, ".gif": {}, ".gz": {}, ".ico": {}, ".jar": {},
	".jpeg": {}, ".jpg": {}, ".js": {}, ".json": {}, ".map": {}, ".mov": {}, ".mp3": {}, ".mp4": {}, ".pdf": {},
	".png": {}, ".svg": {}, ".tar": {}, ".tgz": {}, ".ttf": {}, ".txt": {}, ".wav": {}, ".webm": {}, ".woff": {},
	".woff2": {}, ".xml": {}, ".zip": {},
}

type crawlerQueueItem struct {
	URL   string
	Depth int
}

type crawlerRootCandidate struct {
	Root             string
	ObservationCount int
	samples          []models.CrawlLink
	sampleKeys       map[string]struct{}
	referrers        map[string]struct{}
	relations        map[string]struct{}
}

// CrawlerCollector recursively crawls in-scope HTML pages and judge-gates outbound roots.
type CrawlerCollector struct {
	client            *http.Client
	buildStartURLs    func(domain string) []string
	fetchPage         func(ctx context.Context, target crawlerQueueItem) (*models.CrawlPage, error)
	judge             ownership.Judge
	maxDepth          int
	maxPagesPerSeed   int
	maxLinksPerPage   int
	maxSamplesPerRoot int
	now               func() time.Time
}

func NewCrawlerCollector() *CrawlerCollector {
	collector := &CrawlerCollector{
		client:            &http.Client{Timeout: 20 * time.Second},
		judge:             ownership.NewDefaultJudge(),
		maxDepth:          defaultCrawlerMaxDepth,
		maxPagesPerSeed:   defaultCrawlerMaxPagesPerSeed,
		maxLinksPerPage:   defaultCrawlerMaxLinksPerPage,
		maxSamplesPerRoot: defaultCrawlerMaxSamplesPerRoot,
		now:               time.Now,
	}
	collector.buildStartURLs = func(domain string) []string {
		domain = discovery.NormalizeDomainIdentifier(domain)
		return []string{
			"https://" + domain + "/",
			"http://" + domain + "/",
		}
	}
	collector.fetchPage = collector.defaultFetchPage
	return collector
}

func (c *CrawlerCollector) Process(ctx context.Context, pCtx *models.PipelineContext) (*models.PipelineContext, error) {
	log.Println("[Crawler Collector] Processing seeds...")

	var newEnums []models.Enumeration
	var newErrors []error
	var newAssets []models.Asset

	for _, seed := range pCtx.CollectionSeeds() {
		request := c.buildRequest(seed)
		if len(request.StartURLs) == 0 || len(request.ScopeRoots) == 0 {
			continue
		}

		enum := models.Enumeration{
			ID:        newNodeID("enum-crawler"),
			SeedID:    seed.ID,
			Status:    "running",
			CreatedAt: c.now(),
			StartedAt: c.now(),
		}
		newEnums = append(newEnums, enum)

		internalHosts, outboundByRoot, crawlErrors := c.crawlSeed(ctx, request)
		newErrors = append(newErrors, crawlErrors...)

		for _, host := range sortedCrawlerMapKeys(internalHosts) {
			newAssets = append(newAssets, models.Asset{
				ID:            newNodeID("dom-crawler"),
				EnumerationID: enum.ID,
				Type:          models.AssetTypeDomain,
				Identifier:    host,
				Source:        "crawler_collector",
				DiscoveryDate: c.now(),
				DomainDetails: &models.DomainDetails{},
				EnrichmentData: map[string]interface{}{
					"crawl_kind": "internal_link",
				},
			})
		}

		judgeCandidates, selected := buildCrawlerJudgeCandidates(outboundByRoot)
		if c.judge == nil || len(judgeCandidates) == 0 {
			continue
		}

		for start := 0; start < len(judgeCandidates); start += maxCrawlerJudgeBatchSize {
			end := start + maxCrawlerJudgeBatchSize
			if end > len(judgeCandidates) {
				end = len(judgeCandidates)
			}

			batch := judgeCandidates[start:end]
			allowedRoots := make(map[string]struct{}, len(batch))
			for _, candidate := range batch {
				allowedRoots[candidate.Root] = struct{}{}
			}

			request := ownership.Request{
				Scenario:   "crawler outbound link pivot",
				Seed:       seed,
				Candidates: batch,
			}
			decisions, err := c.judge.EvaluateCandidates(ctx, request)
			if err != nil {
				newErrors = append(newErrors, err)
				continue
			}
			recordOwnershipJudgeEvaluation(pCtx, "crawler_collector", request, decisions)

			for _, decision := range decisions {
				if _, exists := allowedRoots[decision.Root]; !exists {
					continue
				}
				if !decision.Collect {
					continue
				}
				if !hasHighConfidenceOwnership(decision.Confidence) {
					log.Printf("[Crawler Collector] Skipping %s due to low-confidence judge decision %.2f.", decision.Root, decision.Confidence)
					continue
				}

				candidate, exists := selected[decision.Root]
				if !exists {
					continue
				}

				newAssets = append(newAssets, models.Asset{
					ID:            newNodeID("dom-crawler"),
					EnumerationID: enum.ID,
					Type:          models.AssetTypeDomain,
					Identifier:    decision.Root,
					Source:        "crawler_collector",
					DiscoveryDate: c.now(),
					DomainDetails: &models.DomainDetails{},
					EnrichmentData: map[string]interface{}{
						"crawl_kind":           "judged_outbound_root",
						"crawl_observations":   candidate.ObservationCount,
						"crawl_relations":      candidate.sortedRelations(),
						"crawl_samples":        candidate.sampleStrings(3),
						"ownership_confidence": decision.Confidence,
					},
				})

				discoveredSeed := discovery.BuildDiscoveredSeed(seed, decision.Root, "crawler-outbound")
				discoveredSeed.Evidence = []models.SeedEvidence{
					{
						Source:     "crawler_collector",
						Kind:       "outbound_link",
						Value:      decision.Root,
						Confidence: crawlerSeedEvidenceConfidence(candidate),
					},
				}

				if pCtx.EnqueueSeedCandidate(discoveredSeed, models.SeedEvidence{
					Source:     "ownership_judge",
					Kind:       decision.Kind,
					Value:      decision.Root,
					Confidence: decision.Confidence,
					Reasoned:   true,
				}) {
					log.Printf("[Crawler Collector] Promoted %s from judged outbound links.", decision.Root)
				}
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

func (c *CrawlerCollector) buildRequest(seed models.Seed) models.CrawlRequest {
	scopeRoots := make([]string, 0, len(seed.Domains))
	startURLs := make([]string, 0, len(seed.Domains)*2)

	for _, domain := range seed.Domains {
		normalized := discovery.NormalizeDomainIdentifier(domain)
		if normalized == "" {
			continue
		}

		if root := discovery.RegistrableDomain(normalized); root != "" {
			scopeRoots = append(scopeRoots, root)
		}

		for _, raw := range c.buildStartURLs(normalized) {
			if normalizedURL := normalizeCrawlerURL(raw); normalizedURL != "" {
				startURLs = append(startURLs, normalizedURL)
			}
		}
	}

	return models.CrawlRequest{
		SeedID:     seed.ID,
		StartURLs:  uniqueCrawlerStrings(startURLs),
		ScopeRoots: discovery.UniqueLowerStrings(scopeRoots),
		MaxDepth:   c.maxDepth,
		MaxPages:   c.maxPagesPerSeed,
	}
}

func (c *CrawlerCollector) crawlSeed(ctx context.Context, request models.CrawlRequest) (map[string]struct{}, map[string]*crawlerRootCandidate, []error) {
	scopeRoots := make(map[string]struct{}, len(request.ScopeRoots))
	for _, root := range request.ScopeRoots {
		scopeRoots[root] = struct{}{}
	}

	internalHosts := make(map[string]struct{})
	outboundByRoot := make(map[string]*crawlerRootCandidate)
	visited := make(map[string]struct{})
	queued := make(map[string]struct{})
	queue := make([]crawlerQueueItem, 0, len(request.StartURLs))

	for _, startURL := range request.StartURLs {
		if startURL == "" {
			continue
		}
		if _, exists := queued[startURL]; exists {
			continue
		}
		queued[startURL] = struct{}{}
		queue = append(queue, crawlerQueueItem{URL: startURL})
	}

	var newErrors []error
	pagesFetched := 0

	for len(queue) > 0 && pagesFetched < request.MaxPages {
		item := queue[0]
		queue = queue[1:]
		if _, exists := visited[item.URL]; exists {
			continue
		}
		visited[item.URL] = struct{}{}

		page, err := c.fetchPage(ctx, item)
		if err != nil {
			newErrors = append(newErrors, err)
			continue
		}
		if page == nil {
			continue
		}
		pagesFetched++

		finalURL := normalizeCrawlerURL(discovery.FirstNonEmpty(page.FinalURL, page.URL, item.URL))
		finalHost := crawlerHost(finalURL)
		finalRoot := discovery.RegistrableDomain(finalHost)
		if finalHost != "" && finalRoot != "" {
			if _, inScope := scopeRoots[finalRoot]; inScope && finalHost != finalRoot {
				internalHosts[finalHost] = struct{}{}
			}
		}

		if finalRoot != "" {
			if _, inScope := scopeRoots[finalRoot]; !inScope {
				captureCrawlerCandidate(outboundByRoot, models.CrawlLink{
					SourceURL:   item.URL,
					SourceDepth: item.Depth,
					TargetURL:   finalURL,
					TargetHost:  finalHost,
					TargetRoot:  finalRoot,
					Relation:    "redirect",
				}, c.maxSamplesPerRoot)
				continue
			}
		}

		for _, link := range page.Links {
			link.SourceURL = normalizeCrawlerURL(discovery.FirstNonEmpty(link.SourceURL, finalURL))
			link.TargetURL = normalizeCrawlerURL(link.TargetURL)
			link.TargetHost = discovery.NormalizeDomainIdentifier(discovery.FirstNonEmpty(link.TargetHost, crawlerHost(link.TargetURL)))
			link.TargetRoot = discovery.NormalizeDomainIdentifier(discovery.FirstNonEmpty(link.TargetRoot, discovery.RegistrableDomain(link.TargetHost)))
			if link.TargetURL == "" || link.TargetRoot == "" {
				continue
			}

			if _, inScope := scopeRoots[link.TargetRoot]; inScope {
				if link.TargetHost != "" && link.TargetHost != link.TargetRoot && isAcceptedDomainIdentifier(link.TargetHost) {
					internalHosts[link.TargetHost] = struct{}{}
				}

				if item.Depth < request.MaxDepth && shouldFollowCrawlerURL(link.TargetURL) {
					if _, seen := visited[link.TargetURL]; !seen {
						if _, enqueued := queued[link.TargetURL]; !enqueued {
							queued[link.TargetURL] = struct{}{}
							queue = append(queue, crawlerQueueItem{
								URL:   link.TargetURL,
								Depth: item.Depth + 1,
							})
						}
					}
				}
				continue
			}

			captureCrawlerCandidate(outboundByRoot, link, c.maxSamplesPerRoot)
		}
	}

	return internalHosts, outboundByRoot, newErrors
}

func (c *CrawlerCollector) defaultFetchPage(ctx context.Context, target crawlerQueueItem) (*models.CrawlPage, error) {
	resp, err := fetchutil.DoRequest(ctx, c.client, func(ctx context.Context) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.URL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "Asset-Discovery-Bot/1.0")
		return req, nil
	})
	if err != nil {
		return nil, fmt.Errorf("crawl %s: %w", target.URL, err)
	}
	defer resp.Body.Close()

	page := &models.CrawlPage{
		URL:         normalizeCrawlerURL(target.URL),
		FinalURL:    normalizeCrawlerURL(resp.Request.URL.String()),
		Depth:       target.Depth,
		StatusCode:  resp.StatusCode,
		ContentType: strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type"))),
		FetchedAt:   c.now(),
	}

	if resp.StatusCode >= http.StatusBadRequest {
		return page, nil
	}

	if page.ContentType != "" && !strings.Contains(page.ContentType, "html") {
		return page, nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, crawlerBodyLimit))
	if err != nil {
		return nil, fmt.Errorf("read crawl body %s: %w", target.URL, err)
	}
	if len(body) == 0 {
		return page, nil
	}

	finalURL, err := url.Parse(page.FinalURL)
	if err != nil {
		return nil, fmt.Errorf("parse final crawl URL %s: %w", page.FinalURL, err)
	}

	title, links, err := extractCrawlerLinks(finalURL, body, target.Depth, c.maxLinksPerPage)
	if err != nil {
		return nil, fmt.Errorf("parse crawl HTML %s: %w", page.FinalURL, err)
	}
	page.Title = title
	page.Links = links

	return page, nil
}

func extractCrawlerLinks(baseURL *url.URL, body []byte, sourceDepth, maxLinks int) (string, []models.CrawlLink, error) {
	if len(body) == 0 {
		return "", nil, nil
	}

	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return "", nil, err
	}

	sourceURL := normalizeCrawlerURL(baseURL.String())
	title := ""
	links := make([]models.CrawlLink, 0, maxLinks)
	seen := make(map[string]struct{})

	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if maxLinks > 0 && len(links) >= maxLinks {
			return
		}

		if node.Type == html.ElementNode {
			switch strings.ToLower(node.Data) {
			case "title":
				if title == "" {
					title = normalizeCrawlerText(nodeText(node))
				}
			case "a":
				href := attr(node, "href")
				targetURL := resolveCrawlerLink(baseURL, href)
				if targetURL == "" {
					break
				}

				anchorText := normalizeCrawlerText(nodeText(node))
				relation := classifyCrawlerLinkRelation(sourceURL, targetURL, anchorText)
				dedupeKey := targetURL + "|" + relation + "|" + strings.ToLower(anchorText)
				if _, exists := seen[dedupeKey]; exists {
					break
				}
				seen[dedupeKey] = struct{}{}

				targetHost := crawlerHost(targetURL)
				links = append(links, models.CrawlLink{
					SourceURL:   sourceURL,
					SourceDepth: sourceDepth,
					TargetURL:   targetURL,
					TargetHost:  targetHost,
					TargetRoot:  discovery.RegistrableDomain(targetHost),
					Relation:    relation,
					AnchorText:  anchorText,
					NoFollow:    strings.Contains(strings.ToLower(attr(node, "rel")), "nofollow"),
				})
			}
		}

		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
			if maxLinks > 0 && len(links) >= maxLinks {
				return
			}
		}
	}
	walk(doc)

	for i := range links {
		links[i].SourceTitle = title
	}

	return title, links, nil
}

func captureCrawlerCandidate(outboundByRoot map[string]*crawlerRootCandidate, link models.CrawlLink, maxSamples int) {
	root := discovery.RegistrableDomain(link.TargetRoot)
	if root == "" {
		root = discovery.RegistrableDomain(link.TargetHost)
	}
	if root == "" {
		return
	}

	candidate := outboundByRoot[root]
	if candidate == nil {
		candidate = &crawlerRootCandidate{
			Root:       root,
			sampleKeys: make(map[string]struct{}),
			referrers:  make(map[string]struct{}),
			relations:  make(map[string]struct{}),
		}
		outboundByRoot[root] = candidate
	}

	candidate.ObservationCount++
	if link.SourceURL != "" {
		candidate.referrers[link.SourceURL] = struct{}{}
	}
	if relation := strings.TrimSpace(strings.ToLower(link.Relation)); relation != "" {
		candidate.relations[relation] = struct{}{}
	}

	sampleKey := strings.ToLower(strings.TrimSpace(link.SourceURL)) + "|" + strings.ToLower(strings.TrimSpace(link.TargetURL)) + "|" + strings.ToLower(strings.TrimSpace(link.AnchorText))
	if _, exists := candidate.sampleKeys[sampleKey]; exists {
		return
	}
	candidate.sampleKeys[sampleKey] = struct{}{}

	if maxSamples > 0 && len(candidate.samples) >= maxSamples {
		return
	}

	candidate.samples = append(candidate.samples, link)
}

func buildCrawlerJudgeCandidates(outboundByRoot map[string]*crawlerRootCandidate) ([]ownership.Candidate, map[string]*crawlerRootCandidate) {
	candidates := make([]*crawlerRootCandidate, 0, len(outboundByRoot))
	for _, candidate := range outboundByRoot {
		if candidate == nil || candidate.Root == "" {
			continue
		}
		candidates = append(candidates, candidate)
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].ObservationCount != candidates[j].ObservationCount {
			return candidates[i].ObservationCount > candidates[j].ObservationCount
		}
		if len(candidates[i].referrers) != len(candidates[j].referrers) {
			return len(candidates[i].referrers) > len(candidates[j].referrers)
		}
		return candidates[i].Root < candidates[j].Root
	})

	selected := make(map[string]*crawlerRootCandidate, len(candidates))
	requests := make([]ownership.Candidate, 0, len(candidates))
	for _, candidate := range candidates {
		evidence := candidate.evidenceItems()
		if len(evidence) == 0 {
			continue
		}

		selected[candidate.Root] = candidate
		requests = append(requests, ownership.Candidate{
			Root:     candidate.Root,
			Evidence: evidence,
		})
	}

	return requests, selected
}

func (c *crawlerRootCandidate) evidenceItems() []ownership.EvidenceItem {
	if c == nil || c.Root == "" {
		return nil
	}

	relations := c.sortedRelations()
	evidence := []ownership.EvidenceItem{
		{
			Kind: "crawl_link_summary",
			Summary: fmt.Sprintf(
				"Observed %d outbound link(s) from %d crawled page(s)%s",
				c.ObservationCount,
				len(c.referrers),
				crawlerRelationSummary(relations),
			),
		},
	}

	if prominent := crawlerProminentRelations(relations); len(prominent) > 0 {
		evidence = append(evidence, ownership.EvidenceItem{
			Kind:    "crawl_first_party_context",
			Summary: "Linked from first-party page contexts tagged as: " + strings.Join(prominent, ", "),
		})
	}

	if samples := c.sampleStrings(3); len(samples) > 0 {
		evidence = append(evidence, ownership.EvidenceItem{
			Kind:    "crawl_link_samples",
			Summary: "Sample crawl edges: " + strings.Join(samples, " ; "),
		})
	}

	return evidence
}

func (c *crawlerRootCandidate) sortedRelations() []string {
	if len(c.relations) == 0 {
		return nil
	}

	out := make([]string, 0, len(c.relations))
	for relation := range c.relations {
		out = append(out, relation)
	}
	sort.Strings(out)
	return out
}

func (c *crawlerRootCandidate) sampleStrings(limit int) []string {
	if c == nil || len(c.samples) == 0 {
		return nil
	}

	count := len(c.samples)
	if limit > 0 && count > limit {
		count = limit
	}

	samples := make([]string, 0, count)
	for _, sample := range c.samples[:count] {
		parts := make([]string, 0, 4)
		if sample.SourceURL != "" {
			parts = append(parts, sample.SourceURL)
		}
		if sample.Relation != "" && sample.Relation != "anchor" {
			parts = append(parts, "relation "+sample.Relation)
		}
		if sample.AnchorText != "" {
			parts = append(parts, fmt.Sprintf("text %q", truncateCrawlerText(sample.AnchorText, 80)))
		}
		if sample.TargetURL != "" {
			parts = append(parts, "target "+sample.TargetURL)
		}
		if len(parts) == 0 {
			continue
		}
		samples = append(samples, strings.Join(parts, " | "))
	}
	return samples
}

func crawlerSeedEvidenceConfidence(candidate *crawlerRootCandidate) float64 {
	if candidate == nil {
		return 0
	}

	confidence := 0.45 + 0.05*float64(len(candidate.referrers))
	if len(candidate.relations) > 0 {
		confidence += 0.05
	}
	if confidence > 0.85 {
		return 0.85
	}
	return confidence
}

func crawlerRelationSummary(relations []string) string {
	if len(relations) == 0 {
		return ""
	}
	return "; relation tags: " + strings.Join(relations, ", ")
}

func crawlerProminentRelations(relations []string) []string {
	prominent := make([]string, 0, len(relations))
	for _, relation := range relations {
		switch relation {
		case "about", "careers", "contact", "investor_relations", "legal", "privacy", "security":
			prominent = append(prominent, relation)
		}
	}
	return prominent
}

func resolveCrawlerLink(baseURL *url.URL, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	lower := strings.ToLower(raw)
	switch {
	case strings.HasPrefix(lower, "#"),
		strings.HasPrefix(lower, "javascript:"),
		strings.HasPrefix(lower, "mailto:"),
		strings.HasPrefix(lower, "tel:"),
		strings.HasPrefix(lower, "data:"):
		return ""
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}

	resolved := baseURL.ResolveReference(parsed)
	switch strings.ToLower(resolved.Scheme) {
	case "http", "https":
	default:
		return ""
	}

	return normalizeCrawlerURL(resolved.String())
}

func normalizeCrawlerURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}

	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return ""
	}

	host := discovery.NormalizeDomainIdentifier(parsed.Hostname())
	if host == "" {
		return ""
	}

	port := parsed.Port()
	switch {
	case scheme == "http" && port == "80":
		port = ""
	case scheme == "https" && port == "443":
		port = ""
	}

	if port != "" {
		parsed.Host = net.JoinHostPort(host, port)
	} else {
		parsed.Host = host
	}

	if parsed.Path == "" {
		parsed.Path = "/"
	}
	parsed.Fragment = ""
	parsed.RawQuery = normalizeCrawlerQuery(parsed.Query())
	parsed.Scheme = scheme

	return parsed.String()
}

func normalizeCrawlerQuery(values url.Values) string {
	if len(values) == 0 {
		return ""
	}

	filtered := make(url.Values, len(values))
	for key, items := range values {
		lower := strings.ToLower(strings.TrimSpace(key))
		if lower == "" {
			continue
		}
		if strings.HasPrefix(lower, "utm_") || lower == "fbclid" || lower == "gclid" || lower == "mc_cid" || lower == "mc_eid" {
			continue
		}
		filtered[lower] = append([]string(nil), items...)
	}
	return filtered.Encode()
}

func crawlerHost(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return discovery.NormalizeDomainIdentifier(parsed.Hostname())
}

func shouldFollowCrawlerURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}

	ext := strings.ToLower(path.Ext(parsed.Path))
	if ext == "" {
		return true
	}

	_, skip := crawlerSkipExtensions[ext]
	return !skip
}

func classifyCrawlerLinkRelation(sourceURL, targetURL, anchorText string) string {
	targetJoined := strings.ToLower(targetURL + " " + strings.TrimSpace(anchorText))
	sourceJoined := strings.ToLower(sourceURL)

	switch {
	case strings.Contains(targetJoined, "privacy"):
		return "privacy"
	case strings.Contains(targetJoined, "security"), strings.Contains(targetJoined, "trust"), strings.Contains(targetJoined, "bug bounty"):
		return "security"
	case strings.Contains(targetJoined, "legal"), strings.Contains(targetJoined, "terms"), strings.Contains(targetJoined, "cookie"):
		return "legal"
	case strings.Contains(targetJoined, "contact"), strings.Contains(targetJoined, "support"), strings.Contains(targetJoined, "help"):
		return "contact"
	case strings.Contains(targetJoined, "career"), strings.Contains(targetJoined, "jobs"), strings.Contains(targetJoined, "talent"):
		return "careers"
	case strings.Contains(targetJoined, "about"), strings.Contains(targetJoined, "company"), strings.Contains(targetJoined, "group"):
		return "about"
	case strings.Contains(targetJoined, "investor"), strings.Contains(targetJoined, "investors"):
		return "investor_relations"
	case strings.Contains(sourceJoined, "privacy"):
		return "privacy"
	case strings.Contains(sourceJoined, "security"), strings.Contains(sourceJoined, "trust"), strings.Contains(sourceJoined, "bug bounty"):
		return "security"
	case strings.Contains(sourceJoined, "legal"), strings.Contains(sourceJoined, "terms"), strings.Contains(sourceJoined, "cookie"):
		return "legal"
	case strings.Contains(sourceJoined, "contact"), strings.Contains(sourceJoined, "support"), strings.Contains(sourceJoined, "help"):
		return "contact"
	case strings.Contains(sourceJoined, "career"), strings.Contains(sourceJoined, "jobs"), strings.Contains(sourceJoined, "talent"):
		return "careers"
	case strings.Contains(sourceJoined, "about"), strings.Contains(sourceJoined, "company"), strings.Contains(sourceJoined, "group"):
		return "about"
	case strings.Contains(sourceJoined, "investor"), strings.Contains(sourceJoined, "investors"):
		return "investor_relations"
	default:
		return "anchor"
	}
}

func normalizeCrawlerText(raw string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(raw)), " ")
}

func truncateCrawlerText(raw string, limit int) string {
	raw = normalizeCrawlerText(raw)
	if limit <= 0 || len(raw) <= limit {
		return raw
	}
	return strings.TrimSpace(raw[:limit]) + "..."
}

func uniqueCrawlerStrings(values []string) []string {
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

func sortedCrawlerMapKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
