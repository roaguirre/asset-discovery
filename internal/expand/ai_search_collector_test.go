package expand

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"asset-discovery/internal/models"
	"asset-discovery/internal/ownership"
	"asset-discovery/internal/search"
)

type recordingMutationListener struct {
	mu     sync.Mutex
	events []models.ExecutionEvent
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

type stubSearchProvider struct {
	mu        sync.Mutex
	results   []search.SearchResult
	err       error
	calls     int
	summaries []search.ContextSummary
}

func (p *stubSearchProvider) Search(ctx context.Context, summary search.ContextSummary) (search.SearchResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.calls++
	p.summaries = append(p.summaries, summary)
	if p.err != nil {
		return search.SearchResult{}, p.err
	}
	if len(p.results) == 0 {
		return search.SearchResult{}, nil
	}
	result := p.results[0]
	if len(p.results) > 1 {
		p.results = p.results[1:]
	}
	return result, nil
}

type stubOwnershipJudge struct {
	mu              sync.Mutex
	decisions       []ownership.Decision
	decisionBatches [][]ownership.Decision
	errs            []error
	requests        []ownership.Request
	calls           int
}

func (j *stubOwnershipJudge) EvaluateCandidates(ctx context.Context, request ownership.Request) ([]ownership.Decision, error) {
	j.mu.Lock()
	defer j.mu.Unlock()

	j.requests = append(j.requests, request)
	callIndex := j.calls
	j.calls++

	var err error
	if callIndex < len(j.errs) {
		err = j.errs[callIndex]
	}
	if err != nil {
		return nil, err
	}

	if callIndex < len(j.decisionBatches) {
		return append([]ownership.Decision(nil), j.decisionBatches[callIndex]...), nil
	}
	return append([]ownership.Decision(nil), j.decisions...), nil
}

// TestAISearchCollector_EmitsDisabledEvent verifies the expander explains why
// no AI-search work happened when no provider is configured.
func TestAISearchCollector_EmitsDisabledEvent(t *testing.T) {
	collector := NewAISearchCollector(
		WithAISearchProvider(nil),
		WithAISearchJudge(&stubOwnershipJudge{}),
	)
	pCtx := newAISearchPipelineContext()
	listener := &recordingMutationListener{}
	pCtx.SetMutationListener(listener)

	if _, err := collector.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected collector to skip cleanly, got %v", err)
	}

	event, ok := findExecutionEvent(listener.Events(), "ai_search_disabled")
	if !ok {
		t.Fatalf("expected ai_search_disabled event, got %+v", listener.Events())
	}
	if event.Metadata["collector"] != aiSearchCollectorSource {
		t.Fatalf("expected collector metadata to be preserved, got %+v", event)
	}
}

// TestAISearchCollector_PromotesJudgeApprovedCandidates verifies structured
// search output becomes judge input, an accepted asset, a relation, and a
// promoted follow-up seed.
func TestAISearchCollector_PromotesJudgeApprovedCandidates(t *testing.T) {
	provider := &stubSearchProvider{
		results: []search.SearchResult{
			{
				Queries: []string{"example corp brands"},
				Candidates: []search.SearchCandidate{
					{
						Root:    "example-security.com",
						Summary: "The careers and legal pages identify this root as a first-party security brand.",
						Evidence: []search.SearchEvidence{
							{
								Title:   "Careers",
								URL:     "https://example-security.com/careers",
								Snippet: "Join Example Security, part of Example Corp.",
							},
						},
					},
				},
			},
		},
	}
	judge := &stubOwnershipJudge{
		decisions: []ownership.Decision{
			{
				Root:       "example-security.com",
				Collect:    true,
				Kind:       "ownership_judged",
				Confidence: 0.93,
				Reason:     "The cited careers and legal pages point to the same organization.",
				Explicit:   true,
			},
		},
	}
	now := time.Date(2026, time.April, 1, 12, 0, 0, 0, time.UTC)
	collector := NewAISearchCollector(
		WithAISearchProvider(provider),
		WithAISearchJudge(judge),
		WithAISearchNow(func() time.Time { return now }),
	)
	pCtx := newAISearchPipelineContext()
	pCtx.AppendAssets(models.Asset{
		ID:            "asset-root",
		EnumerationID: "enum-seed-1",
		Type:          models.AssetTypeDomain,
		Identifier:    "example.com",
		Source:        "dns_collector",
		DiscoveryDate: now,
		DomainDetails: &models.DomainDetails{},
	})

	if _, err := collector.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected collector to succeed, got %v", err)
	}

	if len(judge.requests) != 1 {
		t.Fatalf("expected one ownership-judge request, got %d", len(judge.requests))
	}
	if len(judge.requests[0].Candidates) != 1 || judge.requests[0].Candidates[0].Root != "example-security.com" {
		t.Fatalf("expected structured candidate to be forwarded to the judge, got %+v", judge.requests[0].Candidates)
	}
	if !seedExists(pCtx.Seeds, "example-security.com") {
		t.Fatalf("expected accepted root to be promoted into the seed set, got %+v", pCtx.Seeds)
	}
	if !assetExists(pCtx.Assets, "example-security.com") {
		t.Fatalf("expected accepted root asset to be materialized, got %+v", pCtx.Assets)
	}
	if !relationExists(pCtx.Relations, "example.com", "example-security.com", aiSearchRelationKind) {
		t.Fatalf("expected accepted root to retain search-result provenance, got %+v", pCtx.Relations)
	}
	if !pCtx.HasAISearchExecutedRoot("example.com") {
		t.Fatalf("expected focus root to be marked as executed")
	}
	if len(pCtx.JudgeEvaluations) != 1 || pCtx.JudgeEvaluations[0].Collector != aiSearchCollectorSource {
		t.Fatalf("expected AI search judge evaluation to be recorded, got %+v", pCtx.JudgeEvaluations)
	}
}

// TestAISearchCollector_FiltersKnownDiscardedAndDenylistedRoots verifies the
// expander never spends judge budget on roots that are already known, already
// discarded for the same seed, or clearly third-party platforms.
func TestAISearchCollector_FiltersKnownDiscardedAndDenylistedRoots(t *testing.T) {
	provider := &stubSearchProvider{
		results: []search.SearchResult{
			{
				Queries: []string{"example corp domains"},
				Candidates: []search.SearchCandidate{
					{Root: "example.com", Summary: "Already known.", Evidence: []search.SearchEvidence{{Title: "Home", URL: "https://example.com", Snippet: "Known root"}}},
					{Root: "example-old.com", Summary: "Previously discarded.", Evidence: []search.SearchEvidence{{Title: "About", URL: "https://example-old.com/about", Snippet: "Legacy brand"}}},
					{Root: "facebook.com", Summary: "Third-party social page.", Evidence: []search.SearchEvidence{{Title: "Facebook", URL: "https://facebook.com/example", Snippet: "Follow Example"}}},
					{Root: "example-store.com", Summary: "The storefront identifies itself as an Example Corp brand.", Evidence: []search.SearchEvidence{{Title: "Store", URL: "https://example-store.com", Snippet: "Official Example store"}}},
				},
			},
		},
	}
	judge := &stubOwnershipJudge{
		decisions: []ownership.Decision{
			{
				Root:       "example-store.com",
				Collect:    true,
				Kind:       "ownership_judged",
				Confidence: 0.91,
				Explicit:   true,
			},
		},
	}
	collector := NewAISearchCollector(
		WithAISearchProvider(provider),
		WithAISearchJudge(judge),
	)
	pCtx := newAISearchPipelineContext()
	pCtx.RecordJudgeEvaluation(models.JudgeEvaluation{
		Collector: aiSearchCollectorSource,
		SeedID:    "seed-1",
		Scenario:  "prior AI search",
		Outcomes: []models.JudgeCandidateOutcome{
			{
				Root:    "example-old.com",
				Collect: false,
			},
		},
	})

	if _, err := collector.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected collector to succeed, got %v", err)
	}
	if len(judge.requests) != 1 {
		t.Fatalf("expected one filtered request, got %d", len(judge.requests))
	}
	if len(judge.requests[0].Candidates) != 1 || judge.requests[0].Candidates[0].Root != "example-store.com" {
		t.Fatalf("expected only the judgeable root to remain, got %+v", judge.requests[0].Candidates)
	}
}

// TestAISearchCollector_UsesPerRunExecutionCache verifies repeated expander
// passes do not re-run the same expensive search root within a single run.
func TestAISearchCollector_UsesPerRunExecutionCache(t *testing.T) {
	provider := &stubSearchProvider{
		results: []search.SearchResult{
			{
				Candidates: []search.SearchCandidate{
					{
						Root:    "example-store.com",
						Summary: "Official Example store.",
						Evidence: []search.SearchEvidence{
							{Title: "Store", URL: "https://example-store.com", Snippet: "Official Example store"},
						},
					},
				},
			},
		},
	}
	collector := NewAISearchCollector(
		WithAISearchProvider(provider),
		WithAISearchJudge(&stubOwnershipJudge{}),
	)
	pCtx := newAISearchPipelineContext()

	if _, err := collector.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected first collector pass to succeed, got %v", err)
	}
	if _, err := collector.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected second collector pass to succeed, got %v", err)
	}
	if provider.calls != 1 {
		t.Fatalf("expected search provider to run only once, got %d calls", provider.calls)
	}
}

// TestAISearchCollector_BatchesJudgeRequestsUnderPromptBudget verifies the
// per-seed candidate cap and per-request prompt cap are both enforced.
func TestAISearchCollector_BatchesJudgeRequestsUnderPromptBudget(t *testing.T) {
	candidates := make([]search.SearchCandidate, 0, 13)
	for index := 0; index < 13; index++ {
		candidates = append(candidates, search.SearchCandidate{
			Root:    buildRoot(index),
			Summary: "This candidate includes enough descriptive evidence to push prompt estimation over a tight test budget.",
			Evidence: []search.SearchEvidence{
				{
					Title:   "Result",
					URL:     "https://" + buildRoot(index) + "/about",
					Snippet: "Official Example-operated property with legal and careers references.",
				},
			},
		})
	}

	provider := &stubSearchProvider{
		results: []search.SearchResult{{Candidates: candidates}},
	}
	judge := &stubOwnershipJudge{}
	collector := NewAISearchCollector(
		WithAISearchProvider(provider),
		WithAISearchJudge(judge),
		WithAISearchMaxPromptChars(2400),
		WithAISearchMaxJudgedCandidates(12),
	)
	pCtx := newAISearchPipelineContext()

	if _, err := collector.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected collector to succeed, got %v", err)
	}

	totalCandidates := 0
	for _, request := range judge.requests {
		totalCandidates += len(request.Candidates)
		if ownership.EstimatePromptSize(request) > 2400 {
			t.Fatalf("expected batched request to respect prompt ceiling, got %d", ownership.EstimatePromptSize(request))
		}
	}
	if len(judge.requests) < 2 {
		t.Fatalf("expected tight prompt ceiling to force batching, got %d request(s)", len(judge.requests))
	}
	if totalCandidates != 12 {
		t.Fatalf("expected per-seed candidate cap to stop at 12, got %d", totalCandidates)
	}
}

// TestAISearchCollector_PreservesProviderOrderAcrossCapAndPromptSkipping
// verifies the collector caps candidates in provider order and reports
// truncation, prompt-skipped, and selected roots separately.
func TestAISearchCollector_PreservesProviderOrderAcrossCapAndPromptSkipping(t *testing.T) {
	providerOrderedCandidates := []search.SearchCandidate{
		newAISearchCandidate("zulu-example.com", "Official Zulu brand."),
		newAISearchCandidate("alpha-example.com", "Official Alpha brand."),
		newAISearchCandidateWithSummary("oversized-example.com", oversizedAISearchSummary()),
		newAISearchCandidate("mango-example.com", "Official Mango brand."),
	}
	seed := newAISearchPipelineContext().Seeds[0]
	focusRoot := "example.com"
	oversizedRequest := ownership.Request{
		Scenario:   "AI web search expansion from " + focusRoot,
		Seed:       seed,
		Candidates: []ownership.Candidate{ownershipCandidateFromSearch(providerOrderedCandidates[2])},
	}
	regularRequest := ownership.Request{
		Scenario:   "AI web search expansion from " + focusRoot,
		Seed:       seed,
		Candidates: []ownership.Candidate{ownershipCandidateFromSearch(providerOrderedCandidates[0])},
	}
	limit := ownership.EstimatePromptSize(oversizedRequest) - 1
	if limit <= ownership.EstimatePromptSize(regularRequest) {
		t.Fatalf("expected oversized candidate to require a larger prompt than regular candidates")
	}

	provider := &stubSearchProvider{
		results: []search.SearchResult{{
			Candidates: providerOrderedCandidates,
		}},
	}
	judge := &stubOwnershipJudge{}
	collector := NewAISearchCollector(
		WithAISearchProvider(provider),
		WithAISearchJudge(judge),
		WithAISearchMaxPromptChars(limit),
		WithAISearchMaxJudgedCandidates(3),
	)
	pCtx := newAISearchPipelineContext()
	listener := &recordingMutationListener{}
	pCtx.SetMutationListener(listener)

	if _, err := collector.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected collector to succeed, got %v", err)
	}

	if len(judge.requests) == 0 {
		t.Fatalf("expected at least one judge request after prompt skipping")
	}
	gotRoots := make([]string, 0)
	for _, request := range judge.requests {
		gotRoots = append(gotRoots, ownershipRequestRoots(request)...)
	}
	wantRoots := []string{"zulu-example.com", "alpha-example.com"}
	if !stringSlicesEqual(gotRoots, wantRoots) {
		t.Fatalf("expected provider order to be preserved, got %v want %v", gotRoots, wantRoots)
	}

	discoveredEvent, ok := findExecutionEvent(listener.Events(), "ai_search_candidates_discovered")
	if !ok {
		t.Fatalf("expected ai_search_candidates_discovered event, got %+v", listener.Events())
	}
	if got := intMetadataValue(t, discoveredEvent, "candidate_count"); got != 4 {
		t.Fatalf("expected four discovered candidates, got %d", got)
	}
	if got := stringSliceMetadataValue(t, discoveredEvent, "candidate_roots"); !stringSlicesEqual(got, []string{"zulu-example.com", "alpha-example.com", "oversized-example.com", "mango-example.com"}) {
		t.Fatalf("unexpected discovered roots: %v", got)
	}

	truncatedEvent, ok := findExecutionEvent(listener.Events(), "ai_search_candidate_truncated")
	if !ok {
		t.Fatalf("expected ai_search_candidate_truncated event, got %+v", listener.Events())
	}
	if got := stringSliceMetadataValue(t, truncatedEvent, "truncated_roots"); !stringSlicesEqual(got, []string{"mango-example.com"}) {
		t.Fatalf("unexpected truncated roots: %v", got)
	}

	promptSkippedEvent, ok := findExecutionEvent(listener.Events(), "ai_search_prompt_skipped")
	if !ok {
		t.Fatalf("expected ai_search_prompt_skipped event, got %+v", listener.Events())
	}
	if got := stringSliceMetadataValue(t, promptSkippedEvent, "skipped_roots"); !stringSlicesEqual(got, []string{"oversized-example.com"}) {
		t.Fatalf("unexpected prompt-skipped roots: %v", got)
	}

	selectedEvent, ok := findExecutionEvent(listener.Events(), "ai_search_candidates_selected")
	if !ok {
		t.Fatalf("expected ai_search_candidates_selected event, got %+v", listener.Events())
	}
	if got := intMetadataValue(t, selectedEvent, "selected_count"); got != 2 {
		t.Fatalf("expected two selected candidates, got %d", got)
	}
	if got := stringSliceMetadataValue(t, selectedEvent, "selected_roots"); !stringSlicesEqual(got, wantRoots) {
		t.Fatalf("unexpected selected roots: %v", got)
	}
}

// TestAISearchCollector_JudgeTelemetryTracksSuccessfulRequestsOnly verifies
// the completion event reports only the roots from successful judge requests.
func TestAISearchCollector_JudgeTelemetryTracksSuccessfulRequestsOnly(t *testing.T) {
	candidates := []search.SearchCandidate{
		newAISearchCandidate("alpha-example.com", "Official Alpha brand."),
		newAISearchCandidate("beta-example.com", "Official Beta brand."),
		newAISearchCandidate("gamma-example.com", "Official Gamma brand."),
	}
	seed := newAISearchPipelineContext().Seeds[0]
	focusRoot := "example.com"
	singleCandidateRequest := ownership.Request{
		Scenario:   "AI web search expansion from " + focusRoot,
		Seed:       seed,
		Candidates: []ownership.Candidate{ownershipCandidateFromSearch(candidates[0])},
	}
	doubleCandidateRequest := ownership.Request{
		Scenario: "AI web search expansion from " + focusRoot,
		Seed:     seed,
		Candidates: []ownership.Candidate{
			ownershipCandidateFromSearch(candidates[0]),
			ownershipCandidateFromSearch(candidates[1]),
		},
	}
	limit := ownership.EstimatePromptSize(doubleCandidateRequest) - 1
	if limit <= ownership.EstimatePromptSize(singleCandidateRequest) {
		t.Fatalf("expected one candidate to fit when two do not")
	}

	provider := &stubSearchProvider{
		results: []search.SearchResult{{Candidates: candidates}},
	}
	judge := &stubOwnershipJudge{
		decisionBatches: [][]ownership.Decision{
			{
				{
					Root:       "alpha-example.com",
					Collect:    true,
					Kind:       "ownership_judged",
					Confidence: 0.91,
					Explicit:   true,
				},
			},
		},
		errs: []error{
			nil,
			errors.New("judge request failed"),
			errors.New("judge request failed"),
		},
	}
	collector := NewAISearchCollector(
		WithAISearchProvider(provider),
		WithAISearchJudge(judge),
		WithAISearchMaxPromptChars(limit),
	)
	pCtx := newAISearchPipelineContext()
	listener := &recordingMutationListener{}
	pCtx.SetMutationListener(listener)

	if _, err := collector.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected collector to succeed, got %v", err)
	}

	if len(judge.requests) < 2 {
		t.Fatalf("expected prompt ceiling to force multiple judge requests, got %d", len(judge.requests))
	}

	selectedEvent, ok := findExecutionEvent(listener.Events(), "ai_search_candidates_selected")
	if !ok {
		t.Fatalf("expected ai_search_candidates_selected event, got %+v", listener.Events())
	}
	if got := intMetadataValue(t, selectedEvent, "selected_count"); got != 3 {
		t.Fatalf("expected three selected candidates, got %d", got)
	}

	judgeCompletedEvent, ok := findExecutionEvent(listener.Events(), "ai_search_judge_completed")
	if !ok {
		t.Fatalf("expected ai_search_judge_completed event, got %+v", listener.Events())
	}
	if got := intMetadataValue(t, judgeCompletedEvent, "judged_count"); got != 1 {
		t.Fatalf("expected one successfully judged candidate, got %d", got)
	}
	if got := stringSliceMetadataValue(t, judgeCompletedEvent, "judged_roots"); !stringSlicesEqual(got, []string{"alpha-example.com"}) {
		t.Fatalf("unexpected judged roots: %v", got)
	}
	if got := intMetadataValue(t, judgeCompletedEvent, "accepted_count"); got != 1 {
		t.Fatalf("expected one accepted candidate, got %d", got)
	}
	if got := intMetadataValue(t, judgeCompletedEvent, "discarded_count"); got != 0 {
		t.Fatalf("expected no discarded candidates among successful requests, got %d", got)
	}
	if got := stringSliceMetadataValue(t, judgeCompletedEvent, "discarded_roots"); len(got) != 0 {
		t.Fatalf("expected no discarded roots, got %v", got)
	}
	if len(pCtx.Errors) != len(judge.requests)-1 {
		t.Fatalf("expected failed judge requests to be recorded as pipeline errors, got %d errors for %d requests", len(pCtx.Errors), len(judge.requests))
	}
}

// TestAISearchCollector_BuildsContextSummaryFromCurrentRun verifies the
// provider receives accepted, discarded, observed-host, and registration facts
// from the already-enriched runtime graph.
func TestAISearchCollector_BuildsContextSummaryFromCurrentRun(t *testing.T) {
	provider := &stubSearchProvider{
		results: []search.SearchResult{{}},
	}
	collector := NewAISearchCollector(
		WithAISearchProvider(provider),
		WithAISearchJudge(&stubOwnershipJudge{}),
	)
	pCtx := newAISearchPipelineContext()
	pCtx.RecordJudgeEvaluation(models.JudgeEvaluation{
		Collector: "dns_collector",
		SeedID:    "seed-1",
		Outcomes: []models.JudgeCandidateOutcome{
			{Root: "example-security.com", Collect: true},
			{Root: "example-old.com", Collect: false},
		},
	})
	pCtx.AppendAssets(models.Asset{
		ID:            "asset-root",
		EnumerationID: "enum-seed-1",
		Type:          models.AssetTypeDomain,
		Identifier:    "portal.example.com",
		Source:        "dns_collector",
		DomainDetails: &models.DomainDetails{
			RDAP: &models.RDAPData{
				RegistrantOrg:   "Example Corp",
				RegistrantEmail: "legal@example.com",
				RegistrarName:   "MarkMonitor",
				NameServers:     []string{"ns1.example.net"},
			},
		},
	})

	if _, err := collector.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected collector to succeed, got %v", err)
	}
	if len(provider.summaries) != 1 {
		t.Fatalf("expected one summary to reach the provider, got %d", len(provider.summaries))
	}
	summary := provider.summaries[0]
	if !containsString(summary.AcceptedRoots, "example-security.com") {
		t.Fatalf("expected accepted roots in the summary, got %+v", summary)
	}
	if !containsString(summary.DiscardedRoots, "example-old.com") {
		t.Fatalf("expected discarded roots in the summary, got %+v", summary)
	}
	if !containsString(summary.ObservedHosts, "portal.example.com") {
		t.Fatalf("expected observed hosts in the summary, got %+v", summary)
	}
	if !containsSubstring(summary.RegistrationFacts, "Registrant org for example.com: Example Corp") {
		t.Fatalf("expected RDAP facts in the summary, got %+v", summary)
	}
}

func newAISearchPipelineContext() *models.PipelineContext {
	pCtx := &models.PipelineContext{
		Seeds: []models.Seed{
			{
				ID:          "seed-1",
				CompanyName: "Example Corp",
				Domains:     []string{"example.com"},
				Industry:    "security",
				ASN:         []int{64512},
				CIDR:        []string{"203.0.113.0/24"},
			},
		},
		Enumerations: []models.Enumeration{
			{ID: "enum-seed-1", SeedID: "seed-1", Status: "running"},
		},
	}
	pCtx.InitializeSeedFrontier(1)
	return pCtx
}

func findExecutionEvent(events []models.ExecutionEvent, kind string) (models.ExecutionEvent, bool) {
	for _, event := range events {
		if event.Kind == kind {
			return event, true
		}
	}
	return models.ExecutionEvent{}, false
}

func seedExists(seeds []models.Seed, root string) bool {
	for _, seed := range seeds {
		for _, domain := range seed.Domains {
			if domain == root {
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

func relationExists(relations []models.AssetRelation, from string, to string, kind string) bool {
	for _, relation := range relations {
		if relation.FromIdentifier == from && relation.ToIdentifier == to && relation.Kind == kind {
			return true
		}
	}
	return false
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsSubstring(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func buildRoot(index int) string {
	return "example-batch-" + string(rune('a'+index)) + ".com"
}

func ownershipRequestRoots(request ownership.Request) []string {
	roots := make([]string, 0, len(request.Candidates))
	for _, candidate := range request.Candidates {
		roots = append(roots, candidate.Root)
	}
	return roots
}

func newAISearchCandidate(root string, summary string) search.SearchCandidate {
	return search.SearchCandidate{
		Root:    root,
		Summary: summary,
		Evidence: []search.SearchEvidence{
			{
				Title:   "Official Site",
				URL:     "https://" + root + "/about",
				Snippet: "Official property for " + root + ".",
			},
		},
	}
}

func newAISearchCandidateWithSummary(root string, summary string) search.SearchCandidate {
	candidate := newAISearchCandidate(root, summary)
	candidate.Evidence[0].Snippet = oversizedAISearchSummary()
	return candidate
}

func oversizedAISearchSummary() string {
	return "This candidate includes extensive legal, careers, and brand-copy evidence that should exceed the tight prompt budget used in the test while still normalizing cleanly into a single search candidate."
}

func intMetadataValue(t *testing.T, event models.ExecutionEvent, key string) int {
	t.Helper()

	value, exists := event.Metadata[key]
	if !exists {
		t.Fatalf("expected metadata key %q in %+v", key, event)
	}
	typed, ok := value.(int)
	if !ok {
		t.Fatalf("expected metadata key %q to be an int, got %T (%v)", key, value, value)
	}
	return typed
}

func stringSliceMetadataValue(t *testing.T, event models.ExecutionEvent, key string) []string {
	t.Helper()

	value, exists := event.Metadata[key]
	if !exists {
		t.Fatalf("expected metadata key %q in %+v", key, event)
	}
	typed, ok := value.([]string)
	if !ok {
		t.Fatalf("expected metadata key %q to be []string, got %T (%v)", key, value, value)
	}
	return typed
}

func stringSlicesEqual(got []string, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
