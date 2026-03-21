package collect

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"asset-discovery/internal/models"
	"asset-discovery/internal/ownership"
)

func TestSitemapCollector_PromotesSameRootHostsFromNestedSitemaps(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		switch r.URL.Path {
		case "/robots.txt":
			fmt.Fprintf(w, "Sitemap: %s/root-index.xml\n", server.URL)
		case "/root-index.xml":
			fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><sitemapindex><sitemap><loc>%s/nested.xml</loc></sitemap></sitemapindex>`, server.URL)
		case "/nested.xml":
			fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><urlset><url><loc>https://api.example.com/login</loc></url><url><loc>https://www.example.com/docs</loc></url></urlset>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	collector := NewSitemapCollector()
	collector.client = server.Client()
	collector.buildTargets = func(domain string) []sitemapFetchTarget {
		return []sitemapFetchTarget{{URL: server.URL + "/robots.txt", Kind: "robots"}}
	}

	pCtx := &models.PipelineContext{
		Seeds: []models.Seed{{ID: "seed-1", CompanyName: "Example Corp", Domains: []string{"example.com"}}},
	}
	pCtx.InitializeSeedFrontier(1)

	if _, err := collector.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected collector to succeed, got %v", err)
	}

	if !assetExists(pCtx.Assets, "api.example.com") {
		t.Fatalf("expected nested sitemap host to be discovered, got %+v", pCtx.Assets)
	}
	if !assetExists(pCtx.Assets, "www.example.com") {
		t.Fatalf("expected second same-root host to be discovered, got %+v", pCtx.Assets)
	}
}

func TestSitemapCollector_PromotesJudgeApprovedCrossRootCandidate(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><urlset><url><loc>https://portal.example-ops.com/contact</loc></url><url><loc>https://cdn.example-ops.com/assets/app.js</loc></url></urlset>`)
	}))
	defer server.Close()

	judge := &stubOwnershipJudge{
		decisions: []ownership.Decision{
			{
				Root:       "example-ops.com",
				Kind:       "ownership_judged",
				Confidence: 0.95,
			},
		},
	}

	collector := NewSitemapCollector()
	collector.client = server.Client()
	collector.judge = judge
	collector.buildTargets = func(domain string) []sitemapFetchTarget {
		return []sitemapFetchTarget{{URL: server.URL + "/sitemap.xml", Kind: "sitemap"}}
	}

	pCtx := &models.PipelineContext{
		Seeds: []models.Seed{{ID: "seed-1", CompanyName: "Example Corp", Domains: []string{"example.com"}}},
	}
	pCtx.InitializeSeedFrontier(1)

	if _, err := collector.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected collector to succeed, got %v", err)
	}

	if !seedExists(pCtx.Seeds, "example-ops.com") {
		t.Fatalf("expected judged sitemap root to be promoted, got %+v", pCtx.Seeds)
	}
	if !assetExists(pCtx.Assets, "example-ops.com") {
		t.Fatalf("expected judged sitemap root asset, got %+v", pCtx.Assets)
	}
	if len(judge.seen) != 1 {
		t.Fatalf("expected one ownership-judge request, got %+v", judge.seen)
	}
	if judge.seen[0].Scenario != "sitemap host pivot" {
		t.Fatalf("expected sitemap host pivot scenario, got %+v", judge.seen[0])
	}
	if len(judge.seen[0].Candidates) != 1 {
		t.Fatalf("expected one candidate, got %+v", judge.seen[0].Candidates)
	}

	evidence := judge.seen[0].Candidates[0].Evidence
	if len(evidence) < 2 {
		t.Fatalf("expected sitemap evidence summaries, got %+v", evidence)
	}
	if !strings.Contains(evidence[0].Summary, "/sitemap.xml") && !strings.Contains(evidence[1].Summary, "/sitemap.xml") {
		t.Fatalf("expected evidence to include sitemap document, got %+v", evidence)
	}
	if !strings.Contains(evidence[0].Summary, "portal.example-ops.com/contact") && !strings.Contains(evidence[1].Summary, "portal.example-ops.com/contact") {
		t.Fatalf("expected evidence to include sample URL, got %+v", evidence)
	}
}

func TestSitemapCollector_SkipsCrossRootCandidateWithoutJudge(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><urlset><url><loc>https://portal.example-ops.com/contact</loc></url></urlset>`)
	}))
	defer server.Close()

	collector := NewSitemapCollector()
	collector.client = server.Client()
	collector.judge = nil
	collector.buildTargets = func(domain string) []sitemapFetchTarget {
		return []sitemapFetchTarget{{URL: server.URL + "/sitemap.xml", Kind: "sitemap"}}
	}

	pCtx := &models.PipelineContext{
		Seeds: []models.Seed{{ID: "seed-1", CompanyName: "Example Corp", Domains: []string{"example.com"}}},
	}
	pCtx.InitializeSeedFrontier(1)

	if _, err := collector.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected collector to succeed, got %v", err)
	}

	if seedExists(pCtx.Seeds, "example-ops.com") {
		t.Fatalf("expected cross-root sitemap root to stay out of seeds without a judge, got %+v", pCtx.Seeds)
	}
	if assetExists(pCtx.Assets, "example-ops.com") {
		t.Fatalf("expected cross-root sitemap root asset to be skipped without a judge, got %+v", pCtx.Assets)
	}
}

func TestSitemapCollector_RespectsSitemapDocumentCap(t *testing.T) {
	var firstHits int
	var secondHits int

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		switch r.URL.Path {
		case "/first.xml":
			firstHits++
			fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><urlset><url><loc>https://api.example.com/login</loc></url></urlset>`)
		case "/second.xml":
			secondHits++
			fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><urlset><url><loc>https://www.example.com/docs</loc></url></urlset>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	collector := NewSitemapCollector()
	collector.client = server.Client()
	collector.maxSitemapDocsPerSeed = 1
	collector.buildTargets = func(domain string) []sitemapFetchTarget {
		return []sitemapFetchTarget{
			{URL: server.URL + "/first.xml", Kind: "sitemap"},
			{URL: server.URL + "/second.xml", Kind: "sitemap"},
		}
	}

	pCtx := &models.PipelineContext{
		Seeds: []models.Seed{{ID: "seed-1", CompanyName: "Example Corp", Domains: []string{"example.com"}}},
	}
	pCtx.InitializeSeedFrontier(1)

	if _, err := collector.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected collector to succeed, got %v", err)
	}

	if firstHits != 1 || secondHits != 0 {
		t.Fatalf("expected only the first sitemap document to be fetched, got first=%d second=%d", firstHits, secondHits)
	}
	if !assetExists(pCtx.Assets, "api.example.com") {
		t.Fatalf("expected first sitemap doc to contribute assets, got %+v", pCtx.Assets)
	}
	if assetExists(pCtx.Assets, "www.example.com") {
		t.Fatalf("expected second sitemap doc to stay outside the cap, got %+v", pCtx.Assets)
	}
}

func TestSitemapCollector_RespectsJudgeCandidateCap(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><urlset><url><loc>https://portal.example-ops.com/contact</loc></url><url><loc>https://app.example-store.com/cart</loc></url></urlset>`)
	}))
	defer server.Close()

	judge := &stubOwnershipJudge{}

	collector := NewSitemapCollector()
	collector.client = server.Client()
	collector.judge = judge
	collector.maxJudgeCandidatesPerSeed = 1
	collector.buildTargets = func(domain string) []sitemapFetchTarget {
		return []sitemapFetchTarget{{URL: server.URL + "/sitemap.xml", Kind: "sitemap"}}
	}

	pCtx := &models.PipelineContext{
		Seeds: []models.Seed{{ID: "seed-1", CompanyName: "Example Corp", Domains: []string{"example.com"}}},
	}
	pCtx.InitializeSeedFrontier(1)

	if _, err := collector.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected collector to succeed, got %v", err)
	}

	if len(judge.seen) != 1 || len(judge.seen[0].Candidates) != 1 {
		t.Fatalf("expected judge candidate cap to limit submissions, got %+v", judge.seen)
	}
}

func TestSitemapCollector_RecordsNonFatalFetchErrors(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()

	collector := NewSitemapCollector()
	collector.client = server.Client()
	collector.buildTargets = func(domain string) []sitemapFetchTarget {
		return []sitemapFetchTarget{{URL: server.URL + "/broken.xml", Kind: "sitemap"}}
	}

	pCtx := &models.PipelineContext{
		Seeds: []models.Seed{{ID: "seed-1", CompanyName: "Example Corp", Domains: []string{"example.com"}}},
	}
	pCtx.InitializeSeedFrontier(1)

	if _, err := collector.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected collector to record errors without failing, got %v", err)
	}

	if !containsErrorSubstring(pCtx.Errors, "/broken.xml") {
		t.Fatalf("expected fetch error to be recorded, got %+v", pCtx.Errors)
	}
}
