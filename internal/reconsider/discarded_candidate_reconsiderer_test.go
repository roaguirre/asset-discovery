package reconsider

import (
	"context"
	"strings"
	"testing"

	"asset-discovery/internal/models"
	"asset-discovery/internal/ownership"
)

func TestDiscardedCandidateReconsiderer_MergesDiscardedOutcomesAndPromotesAcceptedSeed(t *testing.T) {
	judge := &stubOwnershipJudge{
		decisions: []ownership.Decision{
			{
				Root:       "example-store.com",
				Collect:    true,
				Explicit:   true,
				Kind:       "ownership_judged",
				Confidence: 0.94,
				Reason:     "The run already contains first-party assets under this root.",
			},
		},
	}

	reconsiderer := NewDiscardedCandidateReconsiderer(
		WithDiscardedCandidateReconsidererJudge(judge),
		WithDiscardedCandidateReconsidererMaxPromptChars(100000),
	)

	pCtx := sampleReconsiderationContext()
	pCtx.InitializeSeedFrontier(0)
	pCtx.AdvanceSeedFrontier()
	pCtx.ReserveExtraCollectionWave()

	if _, err := reconsiderer.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected reconsiderer to succeed, got %v", err)
	}

	if len(judge.seen) != 1 {
		t.Fatalf("expected one reconsideration judge request, got %+v", judge.seen)
	}
	if len(judge.seen[0].Candidates) != 1 || judge.seen[0].Candidates[0].Root != "example-store.com" {
		t.Fatalf("expected merged discarded candidate to be reconsidered once, got %+v", judge.seen[0].Candidates)
	}
	if !candidateEvidenceContains(judge.seen[0].Candidates[0], "https://example-store.com/ [store]") {
		t.Fatalf("expected prior support from web hints to be preserved, got %+v", judge.seen[0].Candidates[0].Evidence)
	}
	if !candidateEvidenceContains(judge.seen[0].Candidates[0], "MX-derived registrable root") {
		t.Fatalf("expected prior support from ownership evidence to be preserved, got %+v", judge.seen[0].Candidates[0].Evidence)
	}
	if !candidateEvidenceContains(judge.seen[0].Candidates[0], "portal.example-store.com") {
		t.Fatalf("expected run-level asset context for the candidate root, got %+v", judge.seen[0].Candidates[0].Evidence)
	}

	if !seedHasDomain(pCtx.Seeds, "example-store.com") {
		t.Fatalf("expected reconsidered root to be promoted into seeds, got %+v", pCtx.Seeds)
	}
	if !hasJudgeEvaluation(pCtx.JudgeEvaluations, reconsiderationCollector, "example-store.com", true) {
		t.Fatalf("expected reconsideration decision to be recorded, got %+v", pCtx.JudgeEvaluations)
	}
	if advanced := pCtx.AdvanceSeedFrontier(); !advanced {
		t.Fatalf("expected reconsidered seed to schedule the extra frontier")
	}
}

func TestDiscardedCandidateReconsiderer_IncludesImplicitDiscards(t *testing.T) {
	judge := &stubOwnershipJudge{}
	reconsiderer := NewDiscardedCandidateReconsiderer(
		WithDiscardedCandidateReconsidererJudge(judge),
		WithDiscardedCandidateReconsidererMaxPromptChars(100000),
	)

	pCtx := &models.PipelineContext{
		Seeds: []models.Seed{
			{ID: "seed-1", CompanyName: "Example Corp", Domains: []string{"example.com"}},
		},
		JudgeEvaluations: []models.JudgeEvaluation{
			{
				Collector:   "dns_collector",
				SeedID:      "seed-1",
				SeedLabel:   "Example Corp",
				SeedDomains: []string{"example.com"},
				Scenario:    "dns root variant pivot",
				Outcomes: []models.JudgeCandidateOutcome{
					{
						Root:     "example-labs.com",
						Collect:  false,
						Explicit: false,
						Support:  []string{"Observed as a shared DNS target"},
					},
				},
			},
		},
	}
	pCtx.InitializeSeedFrontier(0)
	pCtx.AdvanceSeedFrontier()
	pCtx.ReserveExtraCollectionWave()

	if _, err := reconsiderer.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected reconsiderer to succeed, got %v", err)
	}

	if len(judge.seen) != 1 {
		t.Fatalf("expected implicit discard to be reconsidered, got %+v", judge.seen)
	}
	if len(judge.seen[0].Candidates) != 1 || judge.seen[0].Candidates[0].Root != "example-labs.com" {
		t.Fatalf("expected implicit discard candidate to reach the judge, got %+v", judge.seen[0].Candidates)
	}
}

func TestDiscardedCandidateReconsiderer_SkipsOversizedPrompt(t *testing.T) {
	judge := &stubOwnershipJudge{}
	reconsiderer := NewDiscardedCandidateReconsiderer(
		WithDiscardedCandidateReconsidererJudge(judge),
		WithDiscardedCandidateReconsidererMaxPromptChars(32),
	)

	pCtx := sampleReconsiderationContext()
	pCtx.InitializeSeedFrontier(0)
	pCtx.AdvanceSeedFrontier()
	pCtx.ReserveExtraCollectionWave()

	if _, err := reconsiderer.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected reconsiderer to succeed, got %v", err)
	}

	if len(judge.seen) != 0 {
		t.Fatalf("expected oversized reconsideration pass to skip before any judge call, got %+v", judge.seen)
	}
	if len(pCtx.Errors) == 0 || !strings.Contains(pCtx.Errors[len(pCtx.Errors)-1].Error(), "post-run reconsideration skipped") {
		t.Fatalf("expected oversized reconsideration skip to be recorded as a non-fatal error, got %+v", pCtx.Errors)
	}
}

func TestDiscardedCandidateReconsiderer_LowConfidenceCollectDoesNotSuppressReconsideration(t *testing.T) {
	judge := &stubOwnershipJudge{}
	reconsiderer := NewDiscardedCandidateReconsiderer(
		WithDiscardedCandidateReconsidererJudge(judge),
		WithDiscardedCandidateReconsidererMaxPromptChars(100000),
	)

	pCtx := &models.PipelineContext{
		Seeds: []models.Seed{
			{ID: "seed-1", CompanyName: "Example Corp", Domains: []string{"example.com"}},
		},
		JudgeEvaluations: []models.JudgeEvaluation{
			{
				Collector:   "dns_collector",
				SeedID:      "seed-1",
				SeedLabel:   "Example Corp",
				SeedDomains: []string{"example.com"},
				Scenario:    "dns root variant pivot",
				Outcomes: []models.JudgeCandidateOutcome{
					{
						Root:       "example-store.com",
						Collect:    true,
						Explicit:   true,
						Confidence: 0.31,
						Reason:     "Weak ownership signal only.",
						Support:    []string{"Observed as a related DNS root"},
					},
				},
			},
			{
				Collector:   "web_hint_collector",
				SeedID:      "seed-1",
				SeedLabel:   "Example Corp",
				SeedDomains: []string{"example.com"},
				Scenario:    "web ownership hints from example.com",
				Outcomes: []models.JudgeCandidateOutcome{
					{
						Root:       "example-store.com",
						Collect:    false,
						Explicit:   true,
						Confidence: 0.82,
						Reason:     "Homepage evidence alone stayed ambiguous.",
						Support:    []string{"https://example-store.com/ [store]"},
					},
				},
			},
		},
	}
	pCtx.InitializeSeedFrontier(0)
	pCtx.AdvanceSeedFrontier()
	pCtx.ReserveExtraCollectionWave()

	if _, err := reconsiderer.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected reconsiderer to succeed, got %v", err)
	}

	if len(judge.seen) != 1 {
		t.Fatalf("expected low-confidence collect root to still be reconsidered, got %+v", judge.seen)
	}
	if len(judge.seen[0].Candidates) != 1 || judge.seen[0].Candidates[0].Root != "example-store.com" {
		t.Fatalf("expected low-confidence collect root to reach reconsideration judge, got %+v", judge.seen[0].Candidates)
	}
	if candidateEvidenceContains(judge.seen[0].Candidates[0], "Roots already accepted earlier in this run") {
		t.Fatalf("expected low-confidence collect root not to be described as already accepted, got %+v", judge.seen[0].Candidates[0].Evidence)
	}
}

func sampleReconsiderationContext() *models.PipelineContext {
	return &models.PipelineContext{
		Seeds: []models.Seed{
			{
				ID:          "seed-1",
				CompanyName: "Example Corp",
				Domains:     []string{"example.com"},
				Industry:    "software",
			},
		},
		Assets: []models.Asset{
			{
				ID:         "asset-1",
				Type:       models.AssetTypeDomain,
				Identifier: "portal.example-store.com",
				Source:     "crt.sh",
			},
			{
				ID:         "asset-2",
				Type:       models.AssetTypeDomain,
				Identifier: "example-store.com",
				Source:     "crawler_collector",
				DomainDetails: &models.DomainDetails{
					RDAP: &models.RDAPData{
						RegistrantOrg: "Example Corp",
						RegistrarName: "Example Registrar",
						NameServers:   []string{"ns1.example-store.com"},
					},
				},
			},
			{
				ID:         "asset-3",
				Type:       models.AssetTypeIP,
				Identifier: "203.0.113.25",
				Source:     "ip_enricher",
				IPDetails: &models.IPDetails{
					PTR: "vpn.example-store.com.",
				},
			},
		},
		JudgeEvaluations: []models.JudgeEvaluation{
			{
				Collector:   "web_hint_collector",
				SeedID:      "seed-1",
				SeedLabel:   "Example Corp",
				SeedDomains: []string{"example.com"},
				Scenario:    "web ownership hints from example.com",
				Outcomes: []models.JudgeCandidateOutcome{
					{
						Root:       "example-store.com",
						Collect:    false,
						Explicit:   true,
						Confidence: 0.88,
						Reason:     "Insufficient first-party evidence from the homepage alone.",
						Support:    []string{"https://example-store.com/ [store]"},
					},
				},
			},
			{
				Collector:   "dns_collector",
				SeedID:      "seed-1",
				SeedLabel:   "Example Corp",
				SeedDomains: []string{"example.com"},
				Scenario:    "dns root variant pivot",
				Outcomes: []models.JudgeCandidateOutcome{
					{
						Root:       "example-store.com",
						Collect:    false,
						Explicit:   false,
						Confidence: 0.0,
						Support:    []string{"Observed as an MX-derived registrable root from the seed DNS records"},
					},
				},
			},
		},
	}
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
		decision.Root = root
		decisionByRoot[root] = decision
	}

	decisions := make([]ownership.Decision, 0, len(request.Candidates))
	for _, candidate := range request.Candidates {
		root := strings.TrimSpace(strings.ToLower(candidate.Root))
		if root == "" {
			continue
		}
		if decision, exists := decisionByRoot[root]; exists {
			decisions = append(decisions, decision)
			continue
		}
		decisions = append(decisions, ownership.Decision{Root: root})
	}

	return decisions, nil
}

func candidateEvidenceContains(candidate ownership.Candidate, fragment string) bool {
	for _, item := range candidate.Evidence {
		if strings.Contains(item.Summary, fragment) {
			return true
		}
	}
	return false
}

func seedHasDomain(seeds []models.Seed, domain string) bool {
	for _, seed := range seeds {
		for _, candidate := range seed.Domains {
			if candidate == domain {
				return true
			}
		}
	}
	return false
}

func hasJudgeEvaluation(evaluations []models.JudgeEvaluation, collector, root string, accepted bool) bool {
	for _, evaluation := range evaluations {
		if evaluation.Collector != collector {
			continue
		}
		for _, outcome := range evaluation.Outcomes {
			if outcome.Root == root && outcome.Collect == accepted {
				return true
			}
		}
	}
	return false
}
