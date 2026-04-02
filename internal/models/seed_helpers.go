package models

import (
	"sort"
	"strings"
)

func seedKey(seed Seed) string {
	company := strings.ToLower(strings.TrimSpace(seed.CompanyName))
	domains := make([]string, 0, len(seed.Domains))

	for _, domain := range seed.Domains {
		normalized := strings.ToLower(strings.TrimSpace(domain))
		if normalized == "" {
			continue
		}
		domains = append(domains, normalized)
	}

	sort.Strings(domains)

	if len(domains) > 0 {
		return strings.Join(domains, ",")
	}

	if company != "" {
		return company
	}

	return strings.TrimSpace(seed.ID)
}

func normalizeSeed(seed Seed) Seed {
	seed.ID = strings.TrimSpace(seed.ID)
	seed.CompanyName = strings.TrimSpace(seed.CompanyName)
	seed.Address = strings.TrimSpace(seed.Address)
	seed.Industry = strings.TrimSpace(seed.Industry)
	seed.Domains = uniqueNormalizedStrings(seed.Domains)
	seed.ASN = uniqueInts(seed.ASN)
	seed.CIDR = uniqueNormalizedStrings(seed.CIDR)
	seed.Tags = uniqueNormalizedStrings(seed.Tags)
	seed.Evidence = normalizeSeedEvidenceSlice(seed.Evidence)
	return seed
}

func normalizeSeedEvidence(evidence SeedEvidence) SeedEvidence {
	evidence.Source = strings.TrimSpace(strings.ToLower(evidence.Source))
	evidence.Kind = strings.TrimSpace(strings.ToLower(evidence.Kind))
	evidence.Value = strings.TrimSpace(strings.ToLower(evidence.Value))
	return evidence
}

func mergeSeeds(existing, incoming Seed) Seed {
	if existing.ID == "" {
		existing.ID = incoming.ID
	}
	if existing.CompanyName == "" {
		existing.CompanyName = incoming.CompanyName
	} else if incoming.CompanyName != "" && !strings.EqualFold(existing.CompanyName, incoming.CompanyName) {
		existing.Evidence = append(existing.Evidence, SeedEvidence{
			Source: "seed_merge",
			Kind:   "company_name",
			Value:  strings.ToLower(strings.TrimSpace(incoming.CompanyName)),
		})
	}
	if existing.Address == "" {
		existing.Address = incoming.Address
	}
	if existing.Industry == "" {
		existing.Industry = incoming.Industry
	}
	if incoming.Confidence > existing.Confidence {
		existing.Confidence = incoming.Confidence
	}
	existing.ASN = append(existing.ASN, incoming.ASN...)
	existing.Domains = append(existing.Domains, incoming.Domains...)
	existing.CIDR = append(existing.CIDR, incoming.CIDR...)
	existing.Tags = append(existing.Tags, incoming.Tags...)
	existing.Evidence = mergeSeedEvidence(existing.Evidence, incoming.Evidence...)
	existing.ASN = uniqueInts(existing.ASN)
	existing.Domains = uniqueNormalizedStrings(existing.Domains)
	existing.CIDR = uniqueNormalizedStrings(existing.CIDR)
	existing.Tags = uniqueNormalizedStrings(existing.Tags)
	return existing
}

func (p *PipelineContext) mergeSeedAcrossSlicesLocked(seed Seed, evidence ...SeedEvidence) {
	key := seedKey(seed)
	p.mergeSeedIntoSlice(p.Seeds, key, seed, evidence...)
	p.mergeSeedIntoSlice(p.collectionSeeds, key, seed, evidence...)
	p.mergeSeedIntoSlice(p.pendingSeeds, key, seed, evidence...)

	if candidate, exists := p.candidateSeeds[key]; exists {
		candidate.seed = mergeSeeds(candidate.seed, seed)
		candidate.evidence = mergeSeedEvidence(candidate.evidence, seed.Evidence...)
		candidate.evidence = mergeSeedEvidence(candidate.evidence, evidence...)
		if seed.Confidence > candidate.maxConfidence {
			candidate.maxConfidence = seed.Confidence
		}
		if hasReasonedSeedEvidence(seed.Evidence) {
			candidate.reasoned = true
		}
		if hasReasonedSeedEvidence(evidence) {
			candidate.reasoned = true
		}
	}
}

func (p *PipelineContext) materializeSeedCandidateLocked(key string, candidate *seedCandidate, schedule bool) bool {
	if candidate == nil {
		return false
	}

	promoted := candidate.seed
	promoted.Confidence = candidate.maxConfidence
	promoted.Evidence = append([]SeedEvidence(nil), candidate.evidence...)

	delete(p.candidateSeeds, key)
	p.knownSeedKeys[key] = struct{}{}
	p.Seeds = append(p.Seeds, promoted)
	if schedule {
		p.pendingSeeds = append(p.pendingSeeds, promoted)
		return true
	}

	return false
}

func (p *PipelineContext) mergeSeedIntoSlice(seeds []Seed, key string, incoming Seed, evidence ...SeedEvidence) {
	for i := range seeds {
		if seedKey(seeds[i]) == key {
			seeds[i] = mergeSeeds(seeds[i], incoming)
			seeds[i].Evidence = mergeSeedEvidence(seeds[i].Evidence, evidence...)
		}
	}
}

func mergeSeedEvidence(existing []SeedEvidence, incoming ...SeedEvidence) []SeedEvidence {
	if len(incoming) == 0 {
		return existing
	}

	index := make(map[string]int, len(existing))
	for i, evidence := range existing {
		index[seedEvidenceKey(evidence)] = i
	}

	for _, evidence := range incoming {
		evidence = normalizeSeedEvidence(evidence)
		if evidence.Source == "" && evidence.Kind == "" && evidence.Value == "" {
			continue
		}

		key := seedEvidenceKey(evidence)
		if idx, exists := index[key]; exists {
			if evidence.Confidence > existing[idx].Confidence {
				existing[idx].Confidence = evidence.Confidence
			}
			if evidence.Reasoned {
				existing[idx].Reasoned = true
			}
			if existing[idx].Value == "" {
				existing[idx].Value = evidence.Value
			}
			continue
		}

		index[key] = len(existing)
		existing = append(existing, evidence)
	}

	return existing
}

func seedEvidenceKey(evidence SeedEvidence) string {
	return strings.Join([]string{
		strings.ToLower(strings.TrimSpace(evidence.Source)),
		strings.ToLower(strings.TrimSpace(evidence.Kind)),
		strings.ToLower(strings.TrimSpace(evidence.Value)),
	}, "|")
}

func hasReasonedSeedEvidence(evidence []SeedEvidence) bool {
	for _, item := range evidence {
		if item.Reasoned {
			return true
		}
	}
	return false
}

func normalizeSeedEvidenceSlice(evidence []SeedEvidence) []SeedEvidence {
	if len(evidence) == 0 {
		return nil
	}

	normalized := make([]SeedEvidence, 0, len(evidence))
	for _, item := range evidence {
		item = normalizeSeedEvidence(item)
		if item.Source == "" && item.Kind == "" && item.Value == "" {
			continue
		}
		normalized = append(normalized, item)
	}
	return mergeSeedEvidence(nil, normalized...)
}

func uniqueNormalizedStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		normalized := strings.ToLower(strings.TrimSpace(value))
		if normalized == "" {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	sort.Strings(out)
	return out
}

func uniqueInts(values []int) []int {
	if len(values) == 0 {
		return nil
	}

	seen := make(map[int]struct{}, len(values))
	out := make([]int, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Ints(out)
	return out
}

func uniqueJudgeSupport(values []string) []string {
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
	sort.Strings(out)
	return out
}
