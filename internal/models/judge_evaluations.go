package models

import "strings"

// RecordJudgeEvaluation appends structured judge analysis for the current run.
func (p *PipelineContext) RecordJudgeEvaluation(evaluation JudgeEvaluation) {
	if len(evaluation.Outcomes) == 0 {
		return
	}

	evaluation.Collector = strings.TrimSpace(strings.ToLower(evaluation.Collector))
	evaluation.SeedID = strings.TrimSpace(evaluation.SeedID)
	evaluation.SeedLabel = strings.TrimSpace(evaluation.SeedLabel)
	evaluation.Scenario = strings.TrimSpace(evaluation.Scenario)
	evaluation.SeedDomains = uniqueNormalizedStrings(evaluation.SeedDomains)

	outcomes := make([]JudgeCandidateOutcome, 0, len(evaluation.Outcomes))
	for _, outcome := range evaluation.Outcomes {
		outcome.Root = strings.TrimSpace(strings.ToLower(outcome.Root))
		outcome.Kind = strings.TrimSpace(strings.ToLower(outcome.Kind))
		outcome.Reason = strings.TrimSpace(outcome.Reason)
		outcome.Support = uniqueJudgeSupport(outcome.Support)
		if outcome.Root == "" {
			continue
		}
		outcomes = append(outcomes, outcome)
	}
	if len(outcomes) == 0 {
		return
	}
	evaluation.Outcomes = outcomes

	p.Lock()
	p.JudgeEvaluations = append(p.JudgeEvaluations, evaluation)
	listener := p.mutationListener
	p.Unlock()

	if listener != nil {
		listener.OnJudgeEvaluationRecorded(evaluation)
	}
}
