package nodes

import (
	"strings"

	"asset-discovery/internal/discovery"
	"asset-discovery/internal/models"
	"asset-discovery/internal/ownership"
	"asset-discovery/internal/webhint"
)

func recordOwnershipJudgeEvaluation(pCtx *models.PipelineContext, collector string, request ownership.Request, decisions []ownership.Decision) {
	if pCtx == nil || len(request.Candidates) == 0 {
		return
	}

	decisionByRoot := make(map[string]ownership.Decision, len(decisions))
	for _, decision := range decisions {
		root := discovery.RegistrableDomain(decision.Root)
		if root == "" {
			continue
		}
		decision.Root = root
		decisionByRoot[root] = decision
	}

	outcomes := make([]models.JudgeCandidateOutcome, 0, len(request.Candidates))
	seen := make(map[string]struct{}, len(request.Candidates))
	for _, candidate := range request.Candidates {
		root := discovery.RegistrableDomain(candidate.Root)
		if root == "" {
			continue
		}
		if _, exists := seen[root]; exists {
			continue
		}
		seen[root] = struct{}{}

		decision := decisionByRoot[root]
		outcomes = append(outcomes, models.JudgeCandidateOutcome{
			Root:       root,
			Collect:    decision.Collect,
			Confidence: decision.Confidence,
			Kind:       decision.Kind,
			Reason:     decision.Reason,
			Explicit:   decision.Explicit,
			Support:    ownershipJudgeSupport(candidate),
		})
	}

	pCtx.RecordJudgeEvaluation(models.JudgeEvaluation{
		Collector:   collector,
		SeedID:      strings.TrimSpace(request.Seed.ID),
		SeedLabel:   judgeSeedLabel(request.Seed),
		SeedDomains: append([]string(nil), request.Seed.Domains...),
		Scenario:    strings.TrimSpace(request.Scenario),
		Outcomes:    outcomes,
	})
}

func recordWebHintJudgeEvaluation(pCtx *models.PipelineContext, collector string, seed models.Seed, baseDomain string, candidates []webhint.Candidate, decisions []webhint.Decision) {
	if pCtx == nil || len(candidates) == 0 {
		return
	}

	decisionByRoot := make(map[string]webhint.Decision, len(decisions))
	for _, decision := range decisions {
		root := discovery.RegistrableDomain(decision.Root)
		if root == "" {
			continue
		}
		decision.Root = root
		decisionByRoot[root] = decision
	}

	scenario := "web ownership hints"
	if normalizedBase := discovery.NormalizeDomainIdentifier(baseDomain); normalizedBase != "" {
		scenario = "web ownership hints from " + normalizedBase
	}

	outcomes := make([]models.JudgeCandidateOutcome, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		root := discovery.RegistrableDomain(candidate.Root)
		if root == "" {
			continue
		}
		if _, exists := seen[root]; exists {
			continue
		}
		seen[root] = struct{}{}

		decision := decisionByRoot[root]
		outcomes = append(outcomes, models.JudgeCandidateOutcome{
			Root:       root,
			Collect:    decision.Collect,
			Confidence: decision.Confidence,
			Kind:       decision.Kind,
			Reason:     decision.Reason,
			Explicit:   decision.Explicit,
			Support:    webHintJudgeSupport(candidate),
		})
	}

	pCtx.RecordJudgeEvaluation(models.JudgeEvaluation{
		Collector:   collector,
		SeedID:      strings.TrimSpace(seed.ID),
		SeedLabel:   judgeSeedLabel(seed),
		SeedDomains: append([]string(nil), seed.Domains...),
		Scenario:    scenario,
		Outcomes:    outcomes,
	})
}

func judgeSeedLabel(seed models.Seed) string {
	if companyName := strings.TrimSpace(seed.CompanyName); companyName != "" {
		return companyName
	}

	for _, domain := range seed.Domains {
		if normalized := discovery.NormalizeDomainIdentifier(domain); normalized != "" {
			return normalized
		}
	}

	return strings.TrimSpace(seed.ID)
}

func ownershipJudgeSupport(candidate ownership.Candidate) []string {
	if len(candidate.Evidence) == 0 {
		return nil
	}

	values := make([]string, 0, len(candidate.Evidence))
	for _, item := range candidate.Evidence {
		summary := strings.TrimSpace(item.Summary)
		if summary == "" {
			summary = strings.ReplaceAll(strings.TrimSpace(item.Kind), "_", " ")
		}
		if summary == "" {
			continue
		}
		values = append(values, summary)
	}

	return uniqueJudgeSupportStrings(values)
}

func webHintJudgeSupport(candidate webhint.Candidate) []string {
	if len(candidate.Samples) == 0 {
		return nil
	}

	values := make([]string, 0, len(candidate.Samples))
	for _, sample := range candidate.Samples {
		href := strings.TrimSpace(sample.Href)
		text := strings.TrimSpace(sample.Text)
		switch {
		case href != "" && text != "":
			values = append(values, href+" ["+text+"]")
		case href != "":
			values = append(values, href)
		case text != "":
			values = append(values, text)
		}
	}

	return uniqueJudgeSupportStrings(values)
}

func uniqueJudgeSupportStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}

	return out
}
