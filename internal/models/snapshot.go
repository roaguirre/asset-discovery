package models

// SnapshotReadModel returns a deep-cloned pipeline snapshot so concurrent
// exporters and live projections can read it safely without copying the live
// mutex-bearing runtime context by value.
func (p *PipelineContext) SnapshotReadModel() *PipelineContext {
	p.Lock()
	defer p.Unlock()

	return &PipelineContext{
		Seeds:                 cloneSeeds(p.Seeds),
		Enumerations:          cloneEnumerations(p.Enumerations),
		Assets:                cloneAssets(p.Assets),
		Observations:          cloneObservations(p.Observations),
		Relations:             cloneRelations(p.Relations),
		JudgeEvaluations:      cloneJudgeEvaluations(p.JudgeEvaluations),
		DNSVariantSweepLabels: append([]string(nil), p.DNSVariantSweepLabels...),
	}
}

func cloneSeeds(values []Seed) []Seed {
	if len(values) == 0 {
		return nil
	}

	cloned := make([]Seed, 0, len(values))
	for _, value := range values {
		item := value
		item.Domains = append([]string(nil), value.Domains...)
		item.Evidence = append([]SeedEvidence(nil), value.Evidence...)
		item.ASN = append([]int(nil), value.ASN...)
		item.CIDR = append([]string(nil), value.CIDR...)
		item.Tags = append([]string(nil), value.Tags...)
		cloned = append(cloned, item)
	}
	return cloned
}

func cloneEnumerations(values []Enumeration) []Enumeration {
	if len(values) == 0 {
		return nil
	}
	return append([]Enumeration(nil), values...)
}

func cloneAssets(values []Asset) []Asset {
	if len(values) == 0 {
		return nil
	}

	cloned := make([]Asset, 0, len(values))
	for _, value := range values {
		cloned = append(cloned, cloneAsset(value))
	}
	return cloned
}

func cloneObservations(values []AssetObservation) []AssetObservation {
	if len(values) == 0 {
		return nil
	}

	cloned := make([]AssetObservation, 0, len(values))
	for _, value := range values {
		item := value
		item.DomainDetails = cloneDomainDetails(value.DomainDetails)
		item.IPDetails = cloneIPDetails(value.IPDetails)
		item.EnrichmentData = cloneEnrichmentData(value.EnrichmentData)
		item.EnrichmentStates = cloneEnrichmentStates(value.EnrichmentStates)
		cloned = append(cloned, item)
	}
	return cloned
}

func cloneRelations(values []AssetRelation) []AssetRelation {
	if len(values) == 0 {
		return nil
	}
	return append([]AssetRelation(nil), values...)
}

func cloneJudgeEvaluations(values []JudgeEvaluation) []JudgeEvaluation {
	if len(values) == 0 {
		return nil
	}

	cloned := make([]JudgeEvaluation, 0, len(values))
	for _, value := range values {
		item := value
		item.SeedDomains = append([]string(nil), value.SeedDomains...)
		item.Outcomes = cloneJudgeCandidateOutcomes(value.Outcomes)
		cloned = append(cloned, item)
	}
	return cloned
}

func cloneJudgeCandidateOutcomes(values []JudgeCandidateOutcome) []JudgeCandidateOutcome {
	if len(values) == 0 {
		return nil
	}

	cloned := make([]JudgeCandidateOutcome, 0, len(values))
	for _, value := range values {
		item := value
		item.Support = append([]string(nil), value.Support...)
		cloned = append(cloned, item)
	}
	return cloned
}
