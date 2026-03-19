package models

import "testing"

func TestPipelineContext_InitializeSeedFrontierMergesDuplicateDomainSeeds(t *testing.T) {
	pCtx := &PipelineContext{
		Seeds: []Seed{
			{
				ID:          "seed-1",
				CompanyName: "Alpha Corp",
				Domains:     []string{"example.com"},
				ASN:         []int{64500},
				CIDR:        []string{"203.0.113.0/24"},
				Tags:        []string{"east"},
				Evidence: []SeedEvidence{
					{Source: "manual", Kind: "company_name", Value: "Alpha Corp"},
				},
			},
			{
				ID:          "seed-2",
				CompanyName: "Beta Corp",
				Domains:     []string{"example.com"},
				ASN:         []int{64501},
				CIDR:        []string{"198.51.100.0/24"},
				Tags:        []string{"west"},
				Evidence: []SeedEvidence{
					{Source: "manual", Kind: "company_name", Value: "Beta Corp"},
				},
			},
		},
	}
	pCtx.InitializeSeedFrontier(1)

	if got := len(pCtx.Seeds); got != 1 {
		t.Fatalf("expected duplicate domain seeds to merge, got %d seeds", got)
	}

	merged := pCtx.CollectionSeeds()[0]
	if !containsInt(merged.ASN, 64500) || !containsInt(merged.ASN, 64501) {
		t.Fatalf("expected ASN vectors to be merged, got %+v", merged.ASN)
	}
	if !containsString(merged.CIDR, "203.0.113.0/24") || !containsString(merged.CIDR, "198.51.100.0/24") {
		t.Fatalf("expected CIDR vectors to be merged, got %+v", merged.CIDR)
	}
	if !containsString(merged.Tags, "east") || !containsString(merged.Tags, "west") {
		t.Fatalf("expected tags to be merged, got %+v", merged.Tags)
	}
	if !containsSeedEvidence(merged.Evidence, "manual", "company_name", "alpha corp") || !containsSeedEvidence(merged.Evidence, "manual", "company_name", "beta corp") {
		t.Fatalf("expected seed evidence to be preserved, got %+v", merged.Evidence)
	}
	if !containsSeedEvidence(merged.Evidence, "seed_merge", "company_name", "beta corp") {
		t.Fatalf("expected conflicting company name to be preserved as merge evidence, got %+v", merged.Evidence)
	}
}

func TestPipelineContext_EnqueueSeedCandidateRequiresTwoSignals(t *testing.T) {
	pCtx := &PipelineContext{
		Seeds: []Seed{
			{ID: "seed-1", CompanyName: "Example Corp", Domains: []string{"example.com"}},
		},
	}
	pCtx.InitializeSeedFrontier(1)

	candidate := Seed{
		ID:          "seed-2",
		CompanyName: "Example Corp",
		Domains:     []string{"example-security.com"},
	}

	if promoted := pCtx.EnqueueSeedCandidate(candidate, SeedEvidence{
		Source:     "web_hint_collector",
		Kind:       "legal_link",
		Value:      "example-security.com",
		Confidence: 0.72,
	}); promoted {
		t.Fatalf("expected a single weak signal to stay pending")
	}

	if len(pCtx.Seeds) != 1 {
		t.Fatalf("expected only the original seed to be registered, got %d", len(pCtx.Seeds))
	}

	if promoted := pCtx.EnqueueSeedCandidate(candidate, SeedEvidence{
		Source:     "web_hint_collector",
		Kind:       "securitytxt",
		Value:      "example-security.com",
		Confidence: 0.62,
	}); !promoted {
		t.Fatalf("expected the second distinct signal to promote the seed")
	}

	if len(pCtx.Seeds) != 2 {
		t.Fatalf("expected promoted seed to be registered, got %d", len(pCtx.Seeds))
	}
}

func TestPipelineContext_EnqueueSeedCandidatePromotesStrongSignal(t *testing.T) {
	pCtx := &PipelineContext{
		Seeds: []Seed{
			{ID: "seed-1", CompanyName: "Example Corp", Domains: []string{"example.com"}},
		},
	}
	pCtx.InitializeSeedFrontier(1)

	promoted := pCtx.EnqueueSeedCandidate(Seed{
		ID:          "seed-2",
		CompanyName: "Example Corp",
		Domains:     []string{"example-holdings.com"},
	}, SeedEvidence{
		Source:     "reverse_registration_collector",
		Kind:       "registrant_org",
		Value:      "example-holdings.com",
		Confidence: 0.93,
	})
	if !promoted {
		t.Fatalf("expected a strong registration signal to promote immediately")
	}

	if len(pCtx.Seeds) != 2 {
		t.Fatalf("expected promoted seed to be registered, got %d", len(pCtx.Seeds))
	}
}

func TestPipelineContext_EnqueueSeedMergesDuplicateDomainVectors(t *testing.T) {
	pCtx := &PipelineContext{
		Seeds: []Seed{
			{
				ID:          "seed-1",
				CompanyName: "Alpha Corp",
				Domains:     []string{"example.com"},
				ASN:         []int{64500},
				Tags:        []string{"east"},
			},
		},
	}
	pCtx.InitializeSeedFrontier(1)

	if promoted := pCtx.EnqueueSeed(Seed{
		ID:          "seed-2",
		CompanyName: "Beta Corp",
		Domains:     []string{"example.com"},
		ASN:         []int{64501},
		CIDR:        []string{"198.51.100.0/24"},
		Tags:        []string{"west"},
	}); promoted {
		t.Fatalf("expected duplicate seed to merge instead of enqueueing a second frontier seed")
	}

	if got := len(pCtx.Seeds); got != 1 {
		t.Fatalf("expected one canonical seed after merge, got %d", got)
	}

	merged := pCtx.CollectionSeeds()[0]
	if !containsInt(merged.ASN, 64500) || !containsInt(merged.ASN, 64501) {
		t.Fatalf("expected ASN vectors to be merged, got %+v", merged.ASN)
	}
	if !containsString(merged.CIDR, "198.51.100.0/24") {
		t.Fatalf("expected CIDR vector to be merged, got %+v", merged.CIDR)
	}
	if !containsString(merged.Tags, "east") || !containsString(merged.Tags, "west") {
		t.Fatalf("expected tags to be merged, got %+v", merged.Tags)
	}
	if !containsSeedEvidence(merged.Evidence, "seed_merge", "company_name", "beta corp") {
		t.Fatalf("expected conflicting company name to be preserved as merge evidence, got %+v", merged.Evidence)
	}
}

func TestPipelineContext_EnqueueSeedCandidatePromotesReasonedDecision(t *testing.T) {
	pCtx := &PipelineContext{
		Seeds: []Seed{
			{ID: "seed-1", CompanyName: "Example Corp", Domains: []string{"example.com"}},
		},
	}
	pCtx.InitializeSeedFrontier(1)

	promoted := pCtx.EnqueueSeedCandidate(Seed{
		ID:          "seed-2",
		CompanyName: "Example Corp",
		Domains:     []string{"example-ops.com"},
	}, SeedEvidence{
		Source:     "ownership_judge",
		Kind:       "ownership_judged",
		Value:      "example-ops.com",
		Confidence: 0.61,
		Reasoned:   true,
	})
	if !promoted {
		t.Fatalf("expected a reasoned decision to promote immediately")
	}

	if len(pCtx.Seeds) != 2 {
		t.Fatalf("expected promoted seed to be registered, got %d", len(pCtx.Seeds))
	}
}

func containsInt(values []int, target int) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func containsSeedEvidence(values []SeedEvidence, source, kind, value string) bool {
	for _, evidence := range values {
		if evidence.Source == source && evidence.Kind == kind && evidence.Value == value {
			return true
		}
	}
	return false
}
