package nodes

import (
	"testing"

	"asset-discovery/internal/models"
	"asset-discovery/internal/ownership"
	"asset-discovery/internal/webhint"
)

func TestRecordOwnershipJudgeEvaluation_CapturesAcceptedAndImplicitDiscardedCandidates(t *testing.T) {
	pCtx := &models.PipelineContext{}

	recordOwnershipJudgeEvaluation(pCtx, "reverse_registration_collector", ownership.Request{
		Scenario: "registration pivot",
		Seed: models.Seed{
			ID:          "seed-1",
			CompanyName: "Example Corp",
			Domains:     []string{"example.com"},
		},
		Candidates: []ownership.Candidate{
			{
				Root: "example-holdings.com",
				Evidence: []ownership.EvidenceItem{
					{Kind: "nameserver_overlap", Summary: "Shares registrable nameserver roots with the seed baseline: example-dns.com"},
				},
			},
			{
				Root: "cloudflare.com",
				Evidence: []ownership.EvidenceItem{
					{Kind: "record_target", Summary: "Observed as the CNAME target for one seed domain"},
				},
			},
		},
	}, []ownership.Decision{
		{
			Root:       "example-holdings.com",
			Collect:    true,
			Kind:       "ownership_judged",
			Confidence: 0.94,
			Reason:     "Registration overlap and nameserver evidence point to the same organization.",
			Explicit:   true,
		},
	})

	if len(pCtx.JudgeEvaluations) != 1 {
		t.Fatalf("expected one judge evaluation, got %+v", pCtx.JudgeEvaluations)
	}

	evaluation := pCtx.JudgeEvaluations[0]
	if evaluation.Collector != "reverse_registration_collector" {
		t.Fatalf("expected collector to be preserved, got %+v", evaluation)
	}
	if evaluation.SeedLabel != "Example Corp" {
		t.Fatalf("expected seed label to be populated, got %+v", evaluation)
	}
	if len(evaluation.Outcomes) != 2 {
		t.Fatalf("expected both requested candidates to be recorded, got %+v", evaluation.Outcomes)
	}

	accepted := judgeOutcomeByRoot(evaluation.Outcomes, "example-holdings.com")
	if accepted == nil || !accepted.Collect || !accepted.Explicit {
		t.Fatalf("expected accepted candidate to be recorded as explicit collect=true, got %+v", accepted)
	}
	if len(accepted.Support) != 1 || accepted.Support[0] == "" {
		t.Fatalf("expected accepted candidate support to be preserved, got %+v", accepted)
	}

	discarded := judgeOutcomeByRoot(evaluation.Outcomes, "cloudflare.com")
	if discarded == nil || discarded.Collect || discarded.Explicit {
		t.Fatalf("expected missing LLM decision to be recorded as implicit discard, got %+v", discarded)
	}
}

func TestRecordWebHintJudgeEvaluation_CapturesExplicitDiscardedCandidates(t *testing.T) {
	pCtx := &models.PipelineContext{}

	recordWebHintJudgeEvaluation(pCtx, "web_hint_collector", models.Seed{
		ID:          "seed-1",
		CompanyName: "Example Corp",
		Domains:     []string{"example.com"},
	}, "example.com", []webhint.Candidate{
		{
			Root: "example-store.com",
			Samples: []webhint.LinkSample{
				{Href: "https://example-store.com/", Text: "Store"},
			},
		},
		{
			Root: "facebook.com",
			Samples: []webhint.LinkSample{
				{Href: "https://facebook.com/example", Text: "Follow us"},
			},
		},
	}, []webhint.Decision{
		{
			Root:       "example-store.com",
			Collect:    true,
			Kind:       "llm_link",
			Confidence: 0.95,
			Reason:     "The external storefront appears to be a first-party property.",
			Explicit:   true,
		},
		{
			Root:       "facebook.com",
			Collect:    false,
			Confidence: 0.98,
			Reason:     "This is a third-party social platform profile, not a first-party root.",
			Explicit:   true,
		},
	})

	if len(pCtx.JudgeEvaluations) != 1 {
		t.Fatalf("expected one judge evaluation, got %+v", pCtx.JudgeEvaluations)
	}

	evaluation := pCtx.JudgeEvaluations[0]
	if evaluation.Scenario != "web ownership hints from example.com" {
		t.Fatalf("expected base-domain scenario to be preserved, got %+v", evaluation)
	}

	accepted := judgeOutcomeByRoot(evaluation.Outcomes, "example-store.com")
	if accepted == nil || !accepted.Collect {
		t.Fatalf("expected accepted web hint candidate to be preserved, got %+v", accepted)
	}

	discarded := judgeOutcomeByRoot(evaluation.Outcomes, "facebook.com")
	if discarded == nil || discarded.Collect || !discarded.Explicit {
		t.Fatalf("expected explicit discard to be preserved, got %+v", discarded)
	}
	if discarded.Reason == "" || len(discarded.Support) != 1 {
		t.Fatalf("expected discard reason and support to be preserved, got %+v", discarded)
	}
}

func judgeOutcomeByRoot(outcomes []models.JudgeCandidateOutcome, root string) *models.JudgeCandidateOutcome {
	for i := range outcomes {
		if outcomes[i].Root == root {
			return &outcomes[i]
		}
	}
	return nil
}
