package collect

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"asset-discovery/internal/models"
	"asset-discovery/internal/ownership"
	"asset-discovery/internal/webhint"
)

type recordingMutationListener struct {
	mu     sync.Mutex
	events []models.ExecutionEvent
}

type stubCandidatePromotionHandler struct {
	decision models.CandidatePromotionDecision
	seen     []models.CandidatePromotionRequest
}

func (h *stubCandidatePromotionHandler) HandleCandidatePromotion(candidate models.CandidatePromotionRequest) models.CandidatePromotionDecision {
	h.seen = append(h.seen, candidate)
	return h.decision
}

func (l *recordingMutationListener) OnAssetUpsert(asset models.Asset) {}

func (l *recordingMutationListener) OnObservationAdded(observation models.AssetObservation) {}

func (l *recordingMutationListener) OnRelationAdded(relation models.AssetRelation) {}

func (l *recordingMutationListener) OnJudgeEvaluationRecorded(evaluation models.JudgeEvaluation) {}

func (l *recordingMutationListener) OnExecutionEvent(event models.ExecutionEvent) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.events = append(l.events, event)
}

func (l *recordingMutationListener) Events() []models.ExecutionEvent {
	l.mu.Lock()
	defer l.mu.Unlock()

	return append([]models.ExecutionEvent(nil), l.events...)
}

func TestASNCIDRCollector_PromotesPTRRootWhenSignalsStack(t *testing.T) {
	collector := NewASNCIDRCollector()
	collector.judge = &stubOwnershipJudge{
		decisions: []ownership.Decision{
			{
				Root:       "example-security.com",
				Kind:       "ownership_judged",
				Confidence: 0.94,
			},
		},
	}
	collector.ptrLookup = func(ctx context.Context, ip string) ([]string, error) {
		if ip == "203.0.113.0" {
			return []string{"mail.example-security.com."}, nil
		}
		return nil, errors.New("no ptr")
	}
	collector.maxHostsPerCIDR = 1

	pCtx := &models.PipelineContext{
		Seeds: []models.Seed{
			{
				ID:          "seed-1",
				CompanyName: "Example Corp",
				Domains:     []string{"example.com"},
				CIDR:        []string{"203.0.113.0/30"},
			},
		},
	}
	pCtx.InitializeSeedFrontier(1)

	if _, err := collector.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected collector to succeed, got %v", err)
	}

	if !seedExists(pCtx.Seeds, "example-security.com") {
		t.Fatalf("expected PTR root to be promoted into seeds, got %+v", pCtx.Seeds)
	}

	if !assetExists(pCtx.Assets, "example-security.com") {
		t.Fatalf("expected PTR root asset to be added, got %+v", pCtx.Assets)
	}
}

func TestWebHintCollector_PromotesJudgeApprovedHTMLHints(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`
			<html>
				<head>
					<link rel="canonical" href="https://example-store.com/">
					<link rel="alternate" hreflang="es" href="https://example-store.com/es">
				</head>
				<body></body>
			</html>
		`))
	}))
	defer server.Close()

	judge := &stubWebHintJudge{
		hints: []webhint.Decision{
			{
				Root:       "example-store.com",
				Kind:       "llm_link",
				Confidence: 0.95,
			},
		},
	}

	collector := NewWebHintCollector()
	collector.client = server.Client()
	collector.judge = judge
	collector.buildTargets = func(domain string) []webFetchTarget {
		return []webFetchTarget{{URL: server.URL, Kind: "homepage"}}
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

	if !seedExists(pCtx.Seeds, "example-store.com") {
		t.Fatalf("expected web-hinted root to be promoted, got %+v", pCtx.Seeds)
	}
	if len(judge.seen) != 1 || judge.seen[0].Root != "example-store.com" {
		t.Fatalf("expected judge to receive the HTML candidate, got %+v", judge.seen)
	}
}

func TestWebHintCollector_UsesInjectedJudgeForExternalAnchorRoots(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`
			<html>
				<body>
					<a href="https://portal.example-ops.com/contact">Contact our operations team</a>
				</body>
			</html>
		`))
	}))
	defer server.Close()

	judge := &stubWebHintJudge{
		hints: []webhint.Decision{
			{
				Root:       "example-ops.com",
				Kind:       "llm_link",
				Confidence: 0.95,
			},
		},
	}

	collector := NewWebHintCollector()
	collector.client = server.Client()
	collector.judge = judge
	collector.buildTargets = func(domain string) []webFetchTarget {
		return []webFetchTarget{{URL: server.URL, Kind: "homepage"}}
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

	if !seedExists(pCtx.Seeds, "example-ops.com") {
		t.Fatalf("expected judge-approved root to be promoted, got %+v", pCtx.Seeds)
	}
	if len(judge.seen) != 1 || judge.seen[0].Root != "example-ops.com" {
		t.Fatalf("expected judge to receive external anchor candidate, got %+v", judge.seen)
	}
}

// TestWebHintCollector_SkipsExternalAnchorRootsWithoutJudge verifies external
// roots stay out of the frontier when the optional judge is unavailable.
func TestWebHintCollector_SkipsExternalAnchorRootsWithoutJudge(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`
			<html>
				<body>
					<a href="https://api.whatsapp.com/send/?phone=123456789">Contacta con nosotros hoy</a>
				</body>
			</html>
		`))
	}))
	defer server.Close()

	collector := NewWebHintCollector()
	collector.client = server.Client()
	collector.judge = nil
	collector.buildTargets = func(domain string) []webFetchTarget {
		return []webFetchTarget{{URL: server.URL, Kind: "homepage"}}
	}

	pCtx := &models.PipelineContext{
		Seeds: []models.Seed{
			{ID: "seed-1", CompanyName: "Example Corp", Domains: []string{"example.com"}},
		},
	}
	listener := &recordingMutationListener{}
	pCtx.SetMutationListener(listener)
	pCtx.InitializeSeedFrontier(1)

	if _, err := collector.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected collector to succeed, got %v", err)
	}

	if assetExists(pCtx.Assets, "whatsapp.com") {
		t.Fatalf("expected external anchor root to be skipped without a judge, got %+v", pCtx.Assets)
	}

	events := listener.Events()
	disabledEvent, ok := findExecutionEvent(events, "judge_disabled")
	if !ok {
		t.Fatalf("expected judge_disabled event, got %+v", events)
	}
	if !strings.Contains(disabledEvent.Message, "Web hint judge is disabled") {
		t.Fatalf("expected disabled-judge message, got %+v", disabledEvent)
	}
	if _, ok := findExecutionEvent(events, "web_hint_candidates_discovered"); !ok {
		t.Fatalf("expected web_hint_candidates_discovered event, got %+v", events)
	}
	if _, ok := findExecutionEvent(events, "web_hint_candidates_unjudged"); !ok {
		t.Fatalf("expected web_hint_candidates_unjudged event, got %+v", events)
	}
}

// TestWebHintCollector_EmitsNoCandidateDiagnostics verifies the collector
// explains no-op runs when fetched pages never leave the seed root.
func TestWebHintCollector_EmitsNoCandidateDiagnostics(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`
			<html>
				<head>
					<link rel="canonical" href="https://example.com/">
				</head>
				<body>
					<a href="https://example.com/contact">Contact</a>
				</body>
			</html>
		`))
	}))
	defer server.Close()

	collector := NewWebHintCollector()
	collector.client = server.Client()
	collector.judge = &stubWebHintJudge{}
	collector.buildTargets = func(domain string) []webFetchTarget {
		return []webFetchTarget{{URL: server.URL, Kind: "homepage"}}
	}

	pCtx := &models.PipelineContext{
		Seeds: []models.Seed{
			{ID: "seed-1", CompanyName: "Example Corp", Domains: []string{"example.com"}},
		},
	}
	listener := &recordingMutationListener{}
	pCtx.SetMutationListener(listener)
	pCtx.InitializeSeedFrontier(1)

	if _, err := collector.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected collector to succeed, got %v", err)
	}

	event, ok := findExecutionEvent(listener.Events(), "web_hint_no_candidates")
	if !ok {
		t.Fatalf("expected web_hint_no_candidates event, got %+v", listener.Events())
	}
	if got, ok := event.Metadata["target_count"].(int); !ok || got != 1 {
		t.Fatalf("expected target_count=1, got %+v", event.Metadata)
	}
	if got, ok := event.Metadata["successful_fetch_count"].(int); !ok || got != 1 {
		t.Fatalf("expected successful_fetch_count=1, got %+v", event.Metadata)
	}
}

// TestWebHintCollector_EmitsFetchFailureDiagnostics verifies target fetch
// failures surface in live activity instead of disappearing into a silent no-op.
func TestWebHintCollector_EmitsFetchFailureDiagnostics(t *testing.T) {
	collector := NewWebHintCollector()
	collector.client = &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return nil, errors.New("dial timeout")
		}),
	}
	collector.judge = &stubWebHintJudge{}
	collector.buildTargets = func(domain string) []webFetchTarget {
		return []webFetchTarget{{URL: "https://example.com", Kind: "homepage"}}
	}

	pCtx := &models.PipelineContext{
		Seeds: []models.Seed{
			{ID: "seed-1", CompanyName: "Example Corp", Domains: []string{"example.com"}},
		},
	}
	listener := &recordingMutationListener{}
	pCtx.SetMutationListener(listener)
	pCtx.InitializeSeedFrontier(1)

	if _, err := collector.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected collector to succeed, got %v", err)
	}

	if _, ok := findExecutionEvent(listener.Events(), "web_hint_fetch_failed"); !ok {
		t.Fatalf("expected web_hint_fetch_failed event, got %+v", listener.Events())
	}
	noCandidates, ok := findExecutionEvent(listener.Events(), "web_hint_no_candidates")
	if !ok {
		t.Fatalf("expected web_hint_no_candidates event after fetch failure, got %+v", listener.Events())
	}
	if got, ok := noCandidates.Metadata["error_count"].(int); !ok || got != 1 {
		t.Fatalf("expected error_count=1, got %+v", noCandidates.Metadata)
	}
}

// TestWebHintCollector_EmitsJudgeDiagnostics verifies the collector records
// candidate discovery and judge outcomes even when nothing is ultimately accepted.
func TestWebHintCollector_EmitsJudgeDiagnostics(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`
			<html>
				<body>
					<a href="https://portal.example-ops.com/contact">Contact our operations team</a>
				</body>
			</html>
		`))
	}))
	defer server.Close()

	collector := NewWebHintCollector()
	collector.client = server.Client()
	collector.judge = &stubWebHintJudge{
		hints: []webhint.Decision{
			{
				Root:       "example-ops.com",
				Collect:    false,
				Confidence: 0.22,
				Reason:     "Separate vendor portal.",
				Explicit:   true,
			},
		},
	}
	collector.buildTargets = func(domain string) []webFetchTarget {
		return []webFetchTarget{{URL: server.URL, Kind: "homepage"}}
	}

	pCtx := &models.PipelineContext{
		Seeds: []models.Seed{
			{ID: "seed-1", CompanyName: "Example Corp", Domains: []string{"example.com"}},
		},
	}
	listener := &recordingMutationListener{}
	pCtx.SetMutationListener(listener)
	pCtx.InitializeSeedFrontier(1)

	if _, err := collector.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected collector to succeed, got %v", err)
	}

	candidatesEvent, ok := findExecutionEvent(listener.Events(), "web_hint_candidates_discovered")
	if !ok {
		t.Fatalf("expected web_hint_candidates_discovered event, got %+v", listener.Events())
	}
	if got, ok := candidatesEvent.Metadata["candidate_roots"].([]string); !ok || len(got) != 1 || got[0] != "example-ops.com" {
		t.Fatalf("expected example-ops.com candidate root, got %+v", candidatesEvent.Metadata)
	}

	judgeEvent, ok := findExecutionEvent(listener.Events(), "web_hint_judge_completed")
	if !ok {
		t.Fatalf("expected web_hint_judge_completed event, got %+v", listener.Events())
	}
	if got, ok := judgeEvent.Metadata["accepted_count"].(int); !ok || got != 0 {
		t.Fatalf("expected accepted_count=0, got %+v", judgeEvent.Metadata)
	}
	if got, ok := judgeEvent.Metadata["discarded_count"].(int); !ok || got != 1 {
		t.Fatalf("expected discarded_count=1, got %+v", judgeEvent.Metadata)
	}

	if _, ok := findExecutionEvent(listener.Events(), "web_hint_no_accepted_candidates"); !ok {
		t.Fatalf("expected web_hint_no_accepted_candidates event, got %+v", listener.Events())
	}
}

func TestWebHintCollector_SkipsCrossRootRedirectWithoutJudge(t *testing.T) {
	collector := NewWebHintCollector()
	collector.client = &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("<html></html>")),
				Request: &http.Request{
					URL: &url.URL{Scheme: "https", Host: "company.atlassian.net"},
				},
			}, nil
		}),
	}
	collector.judge = nil
	collector.buildTargets = func(domain string) []webFetchTarget {
		return []webFetchTarget{{URL: "https://example.com", Kind: "homepage"}}
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

	if assetExists(pCtx.Assets, "atlassian.net") {
		t.Fatalf("expected cross-root redirect to stay judge-gated, got %+v", pCtx.Assets)
	}
}

func TestWebHintCollector_PromotesJudgeApprovedRedirect(t *testing.T) {
	judge := &stubWebHintJudge{
		hints: []webhint.Decision{
			{
				Root:       "atlassian.net",
				Kind:       "llm_redirect",
				Confidence: 0.96,
			},
		},
	}

	collector := NewWebHintCollector()
	collector.client = &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("<html></html>")),
				Request: &http.Request{
					URL: &url.URL{Scheme: "https", Host: "company.atlassian.net"},
				},
			}, nil
		}),
	}
	collector.judge = judge
	collector.buildTargets = func(domain string) []webFetchTarget {
		return []webFetchTarget{{URL: "https://example.com", Kind: "homepage"}}
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

	if !seedExists(pCtx.Seeds, "atlassian.net") {
		t.Fatalf("expected judge-approved redirect root to be promoted, got %+v", pCtx.Seeds)
	}
	if len(judge.seen) != 1 || judge.seen[0].Root != "atlassian.net" {
		t.Fatalf("expected judge to receive redirect candidate, got %+v", judge.seen)
	}
}

func TestWebHintCollector_SkipsLowConfidenceJudgeDecision(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`
			<html>
				<body>
					<a href="https://example-low-confidence.com/about">About our group</a>
				</body>
			</html>
		`))
	}))
	defer server.Close()

	judge := &stubWebHintJudge{
		hints: []webhint.Decision{
			{
				Root:       "example-low-confidence.com",
				Kind:       "llm_link",
				Confidence: 0.49,
			},
		},
	}

	collector := NewWebHintCollector()
	collector.client = server.Client()
	collector.judge = judge
	collector.buildTargets = func(domain string) []webFetchTarget {
		return []webFetchTarget{{URL: server.URL, Kind: "homepage"}}
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
		t.Fatalf("expected low-confidence web hint root to stay out of assets, got %+v", pCtx.Assets)
	}
	if seedExists(pCtx.Seeds, "example-low-confidence.com") {
		t.Fatalf("expected low-confidence web hint root to stay out of seeds, got %+v", pCtx.Seeds)
	}
}

func TestWebHintCollector_PromotesManualModeLowConfidenceJudgeDecision(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`
			<html>
				<body>
					<a href="https://example-manual-review.com/about">About our group</a>
				</body>
			</html>
		`))
	}))
	defer server.Close()

	confidence := ownership.ManualReviewConfidenceThreshold + 0.01
	judge := &stubWebHintJudge{
		hints: []webhint.Decision{
			{
				Root:       "example-manual-review.com",
				Kind:       "llm_link",
				Confidence: confidence,
			},
		},
	}

	collector := NewWebHintCollector()
	collector.client = server.Client()
	collector.judge = judge
	collector.buildTargets = func(domain string) []webFetchTarget {
		return []webFetchTarget{{URL: server.URL, Kind: "homepage"}}
	}

	pCtx := &models.PipelineContext{
		Seeds: []models.Seed{
			{ID: "seed-1", CompanyName: "Example Corp", Domains: []string{"example.com"}},
		},
	}
	pCtx.SetCandidatePromotionConfidenceThreshold(ownership.ManualReviewConfidenceThreshold)
	pCtx.InitializeSeedFrontier(1)

	if _, err := collector.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected collector to succeed, got %v", err)
	}

	if !assetExists(pCtx.Assets, "example-manual-review.com") {
		t.Fatalf("expected manual-mode low-confidence web hint root to be added as an asset, got %+v", pCtx.Assets)
	}
	if !seedExists(pCtx.Seeds, "example-manual-review.com") {
		t.Fatalf("expected manual-mode low-confidence web hint root to be promoted into seeds, got %+v", pCtx.Seeds)
	}
}

func TestReverseRegistrationCollector_PromotesValidatedCandidate(t *testing.T) {
	collector := NewReverseRegistrationCollector()
	collector.judge = &stubOwnershipJudge{
		decisions: []ownership.Decision{
			{
				Root:       "example-holdings.com",
				Kind:       "ownership_judged",
				Confidence: 0.93,
			},
		},
	}
	collector.searchCT = func(ctx context.Context, term string) ([]string, error) {
		return []string{"portal.example-holdings.com"}, nil
	}
	collector.lookupDomain = func(ctx context.Context, domain string) (*models.RDAPData, error) {
		switch domain {
		case "example.com":
			return &models.RDAPData{
				RegistrantOrg: "Example Corp",
				NameServers:   []string{"ns1.example-dns.com"},
			}, nil
		case "example-holdings.com":
			return &models.RDAPData{
				RegistrantOrg: "Example Corp",
				NameServers:   []string{"ns1.example-dns.com"},
			}, nil
		default:
			return nil, nil
		}
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

	if !seedExists(pCtx.Seeds, "example-holdings.com") {
		t.Fatalf("expected validated reverse-registration domain to be promoted, got %+v", pCtx.Seeds)
	}
}

func TestReverseRegistrationCollector_SkipsUnvalidatedCandidate(t *testing.T) {
	collector := NewReverseRegistrationCollector()
	judge := &stubOwnershipJudge{
		decisions: []ownership.Decision{
			{
				Root:       "example-marketplace.com",
				Kind:       "ownership_judged",
				Confidence: 0.93,
			},
		},
	}
	collector.judge = judge
	collector.searchCT = func(ctx context.Context, term string) ([]string, error) {
		return []string{"portal.example-marketplace.com"}, nil
	}
	collector.lookupDomain = func(ctx context.Context, domain string) (*models.RDAPData, error) {
		if domain == "example.com" {
			return &models.RDAPData{
				RegistrantOrg: "Example Corp",
				NameServers:   []string{"ns1.example-dns.com"},
			}, nil
		}
		return &models.RDAPData{
			RegistrantOrg: "Another Org",
			NameServers:   []string{"ns1.shared-hosting.net"},
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

	if seedExists(pCtx.Seeds, "example-marketplace.com") {
		t.Fatalf("expected unvalidated candidate to stay out of the frontier, got %+v", pCtx.Seeds)
	}
	if len(judge.seen) != 0 {
		t.Fatalf("expected unvalidated candidate to be filtered before judge evaluation, got %+v", judge.seen)
	}
}

func TestReverseRegistrationCollector_DoesNotTreatCollapsedOrganizationNamesAsValidation(t *testing.T) {
	collector := NewReverseRegistrationCollector()
	judge := &stubOwnershipJudge{}
	collector.judge = judge
	collector.searchCT = func(ctx context.Context, term string) ([]string, error) {
		return []string{"portal.example-holdings.com"}, nil
	}
	collector.lookupDomain = func(ctx context.Context, domain string) (*models.RDAPData, error) {
		switch domain {
		case "example.com":
			return &models.RDAPData{
				RegistrantOrg: "Example Group",
				NameServers:   []string{"ns1.example-dns.com"},
			}, nil
		case "example-holdings.com":
			return &models.RDAPData{
				RegistrantOrg: "Example Holdings",
				NameServers:   []string{"ns1.shared-hosting.net"},
			}, nil
		default:
			return nil, nil
		}
	}

	pCtx := &models.PipelineContext{
		Seeds: []models.Seed{
			{ID: "seed-1", CompanyName: "Example Group", Domains: []string{"example.com"}},
		},
	}
	pCtx.InitializeSeedFrontier(1)

	if _, err := collector.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected collector to succeed, got %v", err)
	}

	if len(judge.seen) != 0 {
		t.Fatalf("expected distinct legal names to be filtered before judge evaluation, got %+v", judge.seen)
	}
}

func TestHackerTargetCollector_RejectsQuotaMessage(t *testing.T) {
	collector := NewHackerTargetCollector()
	collector.client = &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body: io.NopCloser(strings.NewReader(
					"API count exceeded - Increase Quota with Membership",
				)),
				Request: req,
			}, nil
		}),
	}

	pCtx := &models.PipelineContext{
		Seeds: []models.Seed{
			{ID: "seed-1", CompanyName: "Example Corp", Domains: []string{"example.com"}},
		},
	}
	pCtx.InitializeSeedFrontier(1)

	if _, err := collector.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected collector to complete with recorded errors, got %v", err)
	}

	if len(pCtx.Assets) != 0 {
		t.Fatalf("expected quota payload to produce no assets, got %+v", pCtx.Assets)
	}
	if len(pCtx.Errors) == 0 {
		t.Fatalf("expected quota payload to be recorded as an error")
	}
}

func TestHackerTargetCollector_RetriesTransientStatus(t *testing.T) {
	attempts := 0
	collector := NewHackerTargetCollector()
	collector.client = &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			attempts++
			if attempts == 1 {
				return &http.Response{
					StatusCode: http.StatusServiceUnavailable,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader("busy")),
					Request:    req,
				}, nil
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("www.example.com,203.0.113.10\n")),
				Request:    req,
			}, nil
		}),
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

	if attempts != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempts)
	}
	if !assetExists(pCtx.Assets, "www.example.com") {
		t.Fatalf("expected retried response to produce the domain asset, got %+v", pCtx.Assets)
	}
	if !assetExists(pCtx.Assets, "203.0.113.10") {
		t.Fatalf("expected retried response to produce the IP asset, got %+v", pCtx.Assets)
	}
}

func seedExists(seeds []models.Seed, domain string) bool {
	for _, seed := range seeds {
		for _, candidate := range seed.Domains {
			if candidate == domain {
				return true
			}
		}
	}
	return false
}

func assetExists(assets []models.Asset, identifier string) bool {
	for _, asset := range assets {
		if asset.Identifier == identifier {
			return true
		}
	}
	return false
}

// findExecutionEvent returns the first execution event with the requested kind.
func findExecutionEvent(events []models.ExecutionEvent, kind string) (models.ExecutionEvent, bool) {
	for _, event := range events {
		if event.Kind == kind {
			return event, true
		}
	}
	return models.ExecutionEvent{}, false
}

type stubWebHintJudge struct {
	hints []webhint.Decision
	err   error
	seen  []webhint.Candidate
}

func (s *stubWebHintJudge) EvaluateAnchorRoots(ctx context.Context, seed models.Seed, baseDomain string, candidates []webhint.Candidate) ([]webhint.Decision, error) {
	s.seen = append([]webhint.Candidate(nil), candidates...)
	if s.err != nil {
		return nil, s.err
	}

	decisionByRoot := make(map[string]webhint.Decision, len(s.hints))
	for _, decision := range s.hints {
		root := strings.TrimSpace(strings.ToLower(decision.Root))
		if root == "" {
			continue
		}
		if !decision.Collect && !decision.Explicit && (decision.Kind != "" || decision.Confidence > 0 || decision.Reason != "") {
			decision.Collect = true
		}
		if !decision.Explicit {
			decision.Explicit = true
		}
		decision.Root = root
		decisionByRoot[root] = decision
	}

	decisions := make([]webhint.Decision, 0, len(candidates))
	seenRoots := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		root := strings.TrimSpace(strings.ToLower(candidate.Root))
		if root == "" {
			continue
		}
		if _, exists := seenRoots[root]; exists {
			continue
		}
		seenRoots[root] = struct{}{}

		decision, exists := decisionByRoot[root]
		if !exists {
			decisions = append(decisions, webhint.Decision{Root: root})
			continue
		}
		decisions = append(decisions, decision)
	}

	return decisions, nil
}

type stubOwnershipJudge struct {
	decisions []ownership.Decision
	err       error
	seen      []ownership.Request
}

func (s *stubOwnershipJudge) EvaluateCandidates(ctx context.Context, request ownership.Request) ([]ownership.Decision, error) {
	s.seen = append(s.seen, request)
	if s.err != nil {
		return nil, s.err
	}

	decisionByRoot := make(map[string]ownership.Decision, len(s.decisions))
	for _, decision := range s.decisions {
		root := strings.TrimSpace(strings.ToLower(decision.Root))
		if root == "" {
			continue
		}
		if !decision.Collect && !decision.Explicit && (decision.Kind != "" || decision.Confidence > 0 || decision.Reason != "") {
			decision.Collect = true
		}
		if !decision.Explicit {
			decision.Explicit = true
		}
		decision.Root = root
		decisionByRoot[root] = decision
	}

	decisions := make([]ownership.Decision, 0, len(request.Candidates))
	seenRoots := make(map[string]struct{}, len(request.Candidates))
	for _, candidate := range request.Candidates {
		root := strings.TrimSpace(strings.ToLower(candidate.Root))
		if root == "" {
			continue
		}
		if _, exists := seenRoots[root]; exists {
			continue
		}
		seenRoots[root] = struct{}{}

		decision, exists := decisionByRoot[root]
		if !exists {
			decisions = append(decisions, ownership.Decision{Root: root})
			continue
		}
		decisions = append(decisions, decision)
	}

	return decisions, nil
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
