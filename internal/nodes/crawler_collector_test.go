package nodes

import (
	"context"
	"fmt"
	"net/url"
	"testing"

	"asset-discovery/internal/models"
	"asset-discovery/internal/ownership"
)

func TestCrawlerCollector_RecursivelyFollowsInScopePagesAndPromotesJudgedOutboundRoot(t *testing.T) {
	collector := NewCrawlerCollector()
	collector.maxDepth = 2
	collector.maxPagesPerSeed = 5
	collector.judge = &stubOwnershipJudge{
		decisions: []ownership.Decision{
			{
				Root:       "example-holdings.com",
				Kind:       "ownership_judged",
				Confidence: 0.97,
			},
		},
	}
	collector.buildStartURLs = func(domain string) []string {
		return []string{"https://example.com/"}
	}

	var seen []string
	pages := map[string]*models.CrawlPage{
		"https://example.com/": {
			URL:      "https://example.com/",
			FinalURL: "https://example.com/",
			Links: []models.CrawlLink{
				{
					SourceURL:  "https://example.com/",
					TargetURL:  "https://legal.example.com/privacy",
					TargetHost: "legal.example.com",
					TargetRoot: "example.com",
					Relation:   "privacy",
				},
				{
					SourceURL:  "https://example.com/",
					TargetURL:  "https://www.example-holdings.com/about",
					TargetHost: "www.example-holdings.com",
					TargetRoot: "example-holdings.com",
					Relation:   "about",
					AnchorText: "About our group",
				},
			},
		},
		"https://legal.example.com/privacy": {
			URL:      "https://legal.example.com/privacy",
			FinalURL: "https://legal.example.com/privacy",
			Links: []models.CrawlLink{
				{
					SourceURL:  "https://legal.example.com/privacy",
					TargetURL:  "https://investors.example-holdings.com/",
					TargetHost: "investors.example-holdings.com",
					TargetRoot: "example-holdings.com",
					Relation:   "investor_relations",
					AnchorText: "Investor Relations",
				},
			},
		},
	}
	collector.fetchPage = func(ctx context.Context, target crawlerQueueItem) (*models.CrawlPage, error) {
		seen = append(seen, target.URL)
		page, exists := pages[target.URL]
		if !exists {
			return nil, fmt.Errorf("unexpected crawl target %s", target.URL)
		}
		return cloneCrawlPage(page), nil
	}

	pCtx := &models.PipelineContext{
		Seeds: []models.Seed{
			{ID: "seed-1", CompanyName: "Example Corp", Domains: []string{"example.com"}},
		},
	}
	pCtx.InitializeSeedFrontier(1)

	if _, err := collector.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected collector to succeed, got %v", err)
	}

	if len(seen) != 2 {
		t.Fatalf("expected recursive crawl to fetch 2 pages, got %v", seen)
	}
	if !assetExists(pCtx.Assets, "legal.example.com") {
		t.Fatalf("expected internal subdomain from crawl to be added, got %+v", pCtx.Assets)
	}
	if !assetExists(pCtx.Assets, "example-holdings.com") {
		t.Fatalf("expected judged outbound root to be added as an asset, got %+v", pCtx.Assets)
	}
	if !seedExists(pCtx.Seeds, "example-holdings.com") {
		t.Fatalf("expected judged outbound root to be promoted into seeds, got %+v", pCtx.Seeds)
	}

	judge, _ := collector.judge.(*stubOwnershipJudge)
	if judge == nil || len(judge.seen) != 1 {
		t.Fatalf("expected one ownership-judge request, got %+v", judge)
	}
	if judge.seen[0].Scenario != "crawler outbound link pivot" {
		t.Fatalf("expected crawler scenario, got %+v", judge.seen[0])
	}
	if len(judge.seen[0].Candidates) != 1 || judge.seen[0].Candidates[0].Root != "example-holdings.com" {
		t.Fatalf("expected judged candidate to be example-holdings.com, got %+v", judge.seen[0].Candidates)
	}
}

func TestCrawlerCollector_JudgesOverflowCandidatesInBatches(t *testing.T) {
	collector := NewCrawlerCollector()
	collector.maxDepth = 1
	collector.maxPagesPerSeed = 1

	targetRoot := "zz-owned-example.com"
	collector.judge = &stubOwnershipJudge{
		decisions: []ownership.Decision{
			{
				Root:       targetRoot,
				Kind:       "ownership_judged",
				Confidence: 0.96,
			},
		},
	}
	collector.buildStartURLs = func(domain string) []string {
		return []string{"https://example.com/"}
	}

	links := make([]models.CrawlLink, 0, maxCrawlerJudgeBatchSize+1)
	for i := 0; i < maxCrawlerJudgeBatchSize; i++ {
		root := fmt.Sprintf("candidate-%02d-example.com", i)
		links = append(links, models.CrawlLink{
			SourceURL:  "https://example.com/",
			TargetURL:  "https://www." + root + "/",
			TargetHost: "www." + root,
			TargetRoot: root,
			Relation:   "anchor",
		})
	}
	links = append(links, models.CrawlLink{
		SourceURL:  "https://example.com/",
		TargetURL:  "https://www." + targetRoot + "/",
		TargetHost: "www." + targetRoot,
		TargetRoot: targetRoot,
		Relation:   "anchor",
	})

	collector.fetchPage = func(ctx context.Context, target crawlerQueueItem) (*models.CrawlPage, error) {
		return &models.CrawlPage{
			URL:      target.URL,
			FinalURL: target.URL,
			Links:    append([]models.CrawlLink(nil), links...),
		}, nil
	}

	pCtx := &models.PipelineContext{
		Seeds: []models.Seed{
			{ID: "seed-1", CompanyName: "Example Corp", Domains: []string{"example.com"}},
		},
	}
	pCtx.InitializeSeedFrontier(1)

	if _, err := collector.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected collector to succeed, got %v", err)
	}

	if !assetExists(pCtx.Assets, targetRoot) {
		t.Fatalf("expected overflow candidate to be judged and added as an asset, got %+v", pCtx.Assets)
	}
	if !seedExists(pCtx.Seeds, targetRoot) {
		t.Fatalf("expected overflow candidate to be promoted into seeds, got %+v", pCtx.Seeds)
	}

	judge, _ := collector.judge.(*stubOwnershipJudge)
	if judge == nil || len(judge.seen) != 2 {
		t.Fatalf("expected two batched ownership-judge requests, got %+v", judge)
	}
	if len(judge.seen[0].Candidates) != maxCrawlerJudgeBatchSize {
		t.Fatalf("expected first batch to contain %d candidates, got %+v", maxCrawlerJudgeBatchSize, judge.seen[0].Candidates)
	}
	if len(judge.seen[1].Candidates) != 1 || judge.seen[1].Candidates[0].Root != targetRoot {
		t.Fatalf("expected second batch to contain only %s, got %+v", targetRoot, judge.seen[1].Candidates)
	}
}

func TestCrawlerCollector_DoesNotSurfaceUnapprovedOutboundRoot(t *testing.T) {
	collector := NewCrawlerCollector()
	collector.maxDepth = 1
	collector.maxPagesPerSeed = 3
	collector.judge = &stubOwnershipJudge{}
	collector.buildStartURLs = func(domain string) []string {
		return []string{"https://example.com/"}
	}
	collector.fetchPage = func(ctx context.Context, target crawlerQueueItem) (*models.CrawlPage, error) {
		return &models.CrawlPage{
			URL:      target.URL,
			FinalURL: target.URL,
			Links: []models.CrawlLink{
				{
					SourceURL:  target.URL,
					TargetURL:  "https://blog.example.com/",
					TargetHost: "blog.example.com",
					TargetRoot: "example.com",
					Relation:   "about",
				},
				{
					SourceURL:  target.URL,
					TargetURL:  "https://vendor-chat.example.net/widget",
					TargetHost: "vendor-chat.example.net",
					TargetRoot: "example.net",
					Relation:   "contact",
					AnchorText: "Chat with us",
				},
			},
		}, nil
	}

	pCtx := &models.PipelineContext{
		Seeds: []models.Seed{
			{ID: "seed-1", CompanyName: "Example Corp", Domains: []string{"example.com"}},
		},
	}
	pCtx.InitializeSeedFrontier(1)

	if _, err := collector.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected collector to succeed, got %v", err)
	}

	if !assetExists(pCtx.Assets, "blog.example.com") {
		t.Fatalf("expected in-scope subdomain to be added, got %+v", pCtx.Assets)
	}
	if assetExists(pCtx.Assets, "example.net") {
		t.Fatalf("expected unapproved outbound root to stay out of visualizer assets, got %+v", pCtx.Assets)
	}
	if seedExists(pCtx.Seeds, "example.net") {
		t.Fatalf("expected unapproved outbound root to stay out of seeds, got %+v", pCtx.Seeds)
	}
}

func TestCrawlerCollector_SkipsLowConfidenceJudgedOutboundRoot(t *testing.T) {
	collector := NewCrawlerCollector()
	collector.maxDepth = 1
	collector.maxPagesPerSeed = 2
	collector.judge = &stubOwnershipJudge{
		decisions: []ownership.Decision{
			{
				Root:       "example-low-confidence.com",
				Kind:       "ownership_judged",
				Confidence: 0.49,
			},
		},
	}
	collector.buildStartURLs = func(domain string) []string {
		return []string{"https://example.com/"}
	}
	collector.fetchPage = func(ctx context.Context, target crawlerQueueItem) (*models.CrawlPage, error) {
		return &models.CrawlPage{
			URL:      target.URL,
			FinalURL: target.URL,
			Links: []models.CrawlLink{
				{
					SourceURL:  target.URL,
					TargetURL:  "https://www.example-low-confidence.com/",
					TargetHost: "www.example-low-confidence.com",
					TargetRoot: "example-low-confidence.com",
					Relation:   "about",
				},
			},
		}, nil
	}

	pCtx := &models.PipelineContext{
		Seeds: []models.Seed{
			{ID: "seed-1", CompanyName: "Example Corp", Domains: []string{"example.com"}},
		},
	}
	pCtx.InitializeSeedFrontier(1)

	if _, err := collector.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected collector to succeed, got %v", err)
	}

	if assetExists(pCtx.Assets, "example-low-confidence.com") {
		t.Fatalf("expected low-confidence outbound root to stay out of assets, got %+v", pCtx.Assets)
	}
	if seedExists(pCtx.Seeds, "example-low-confidence.com") {
		t.Fatalf("expected low-confidence outbound root to stay out of seeds, got %+v", pCtx.Seeds)
	}
}

func TestExtractCrawlerLinks_ResolvesRelativeLinksAndClassifiesContext(t *testing.T) {
	baseURL, err := url.Parse("https://example.com/legal/")
	if err != nil {
		t.Fatalf("expected base URL parse to succeed, got %v", err)
	}

	title, links, err := extractCrawlerLinks(baseURL, []byte(`
		<html>
			<head><title>Legal Center</title></head>
			<body>
				<a href="/privacy">Privacy Policy</a>
				<a href="https://example-holdings.com/about">About the Group</a>
				<a href="javascript:void(0)">Ignore me</a>
			</body>
		</html>
	`), 1, 10)
	if err != nil {
		t.Fatalf("expected HTML extraction to succeed, got %v", err)
	}

	if title != "Legal Center" {
		t.Fatalf("expected parsed title, got %q", title)
	}
	if len(links) != 2 {
		t.Fatalf("expected 2 extracted links, got %+v", links)
	}
	if links[0].TargetURL != "https://example.com/privacy" {
		t.Fatalf("expected relative link to resolve against base URL, got %+v", links[0])
	}
	if links[0].Relation != "privacy" {
		t.Fatalf("expected privacy relation for policy link, got %+v", links[0])
	}
	if links[1].Relation != "about" {
		t.Fatalf("expected about relation for corporate link, got %+v", links[1])
	}
}

func cloneCrawlPage(page *models.CrawlPage) *models.CrawlPage {
	if page == nil {
		return nil
	}

	cloned := *page
	cloned.Links = append([]models.CrawlLink(nil), page.Links...)
	return &cloned
}
