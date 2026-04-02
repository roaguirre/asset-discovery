package models

// InitializeSeedFrontier prepares the collection scheduler with the initial
// seed frontier and deduplicates equivalent submitted seeds up front.
func (p *PipelineContext) InitializeSeedFrontier(maxDepth int) {
	if maxDepth < 0 {
		maxDepth = 0
	}

	p.Lock()
	defer p.Unlock()

	p.maxCollectionDepth = maxDepth
	p.collectionDepth = 0
	p.pendingSeeds = nil
	p.collectionSeeds = nil
	p.extraCollectionWaveReserved = false
	p.extraCollectionWaveInProgress = false
	p.knownSeedKeys = make(map[string]struct{}, len(p.Seeds))
	p.candidateSeeds = make(map[string]*seedCandidate)

	mergedSeeds := make(map[string]Seed, len(p.Seeds))
	seedOrder := make([]string, 0, len(p.Seeds))
	for _, seed := range p.Seeds {
		seed = normalizeSeed(seed)
		key := seedKey(seed)
		if existing, exists := mergedSeeds[key]; exists {
			mergedSeeds[key] = mergeSeeds(existing, seed)
			continue
		}

		mergedSeeds[key] = seed
		seedOrder = append(seedOrder, key)
	}

	p.Seeds = make([]Seed, 0, len(seedOrder))
	p.collectionSeeds = make([]Seed, 0, len(seedOrder))
	for _, key := range seedOrder {
		seed := mergedSeeds[key]
		p.knownSeedKeys[key] = struct{}{}
		p.Seeds = append(p.Seeds, seed)
		p.collectionSeeds = append(p.collectionSeeds, seed)
	}
}

// CollectionSeeds returns the active seed frontier for the current collection wave.
func (p *PipelineContext) CollectionSeeds() []Seed {
	p.Lock()
	defer p.Unlock()

	return append([]Seed(nil), p.collectionSeeds...)
}

// ReserveExtraCollectionWave allows one scheduler-owned collection wave after
// the normal collection depth has been exhausted.
func (p *PipelineContext) ReserveExtraCollectionWave() bool {
	p.Lock()
	defer p.Unlock()

	if p.extraCollectionWaveReserved || p.extraCollectionWaveInProgress {
		return false
	}

	p.extraCollectionWaveReserved = true
	return true
}

// EnqueueSeed schedules a newly discovered seed for the next collection wave.
func (p *PipelineContext) EnqueueSeed(seed Seed) bool {
	p.Lock()
	defer p.Unlock()

	mode := p.seedSchedulingModeLocked()
	if mode == seedSchedulingReject {
		return false
	}

	seed = normalizeSeed(seed)
	key := seedKey(seed)
	if _, exists := p.knownSeedKeys[key]; exists {
		p.mergeSeedAcrossSlicesLocked(seed)
		return false
	}

	delete(p.candidateSeeds, key)
	p.knownSeedKeys[key] = struct{}{}
	p.Seeds = append(p.Seeds, seed)
	if mode == seedSchedulingNextWave {
		p.pendingSeeds = append(p.pendingSeeds, seed)
		return true
	}

	return false
}

// PromoteSeedCandidate records evidence for an auto-discovered seed and
// reports whether it was accepted, rejected, or deferred plus whether that
// acceptance opened another collection frontier.
func (p *PipelineContext) PromoteSeedCandidate(seed Seed, evidence SeedEvidence) CandidatePromotionResult {
	p.Lock()
	defer p.Unlock()

	mode := p.seedSchedulingModeLocked()
	if mode == seedSchedulingReject {
		return CandidatePromotionResult{}
	}

	seed = normalizeSeed(seed)
	evidence = normalizeSeedEvidence(evidence)

	key := seedKey(seed)
	if _, exists := p.knownSeedKeys[key]; exists {
		p.mergeSeedAcrossSlicesLocked(seed, evidence)
		return CandidatePromotionResult{}
	}

	candidate, exists := p.candidateSeeds[key]
	if !exists {
		candidate = &seedCandidate{
			seed:       seed,
			evidence:   append([]SeedEvidence(nil), seed.Evidence...),
			signalKeys: make(map[string]struct{}),
		}
		candidate.maxConfidence = seed.Confidence
		if hasReasonedSeedEvidence(seed.Evidence) {
			candidate.reasoned = true
		}
		p.candidateSeeds[key] = candidate
	} else {
		candidate.seed = mergeSeeds(candidate.seed, seed)
		candidate.evidence = mergeSeedEvidence(candidate.evidence, seed.Evidence...)
		if seed.Confidence > candidate.maxConfidence {
			candidate.maxConfidence = seed.Confidence
		}
		if hasReasonedSeedEvidence(seed.Evidence) {
			candidate.reasoned = true
		}
	}

	candidate.evidence = mergeSeedEvidence(candidate.evidence, evidence)
	if evidence.Source != "" && evidence.Kind != "" {
		signalKey := evidence.Source + "|" + evidence.Kind
		if _, seen := candidate.signalKeys[signalKey]; !seen {
			candidate.signalKeys[signalKey] = struct{}{}
		}
		if evidence.Confidence > candidate.maxConfidence {
			candidate.maxConfidence = evidence.Confidence
		}
	}
	if evidence.Reasoned {
		candidate.reasoned = true
	}

	if mode == seedSchedulingRegisterOnly && !shouldPromoteSeedCandidate(candidate) {
		p.materializeSeedCandidateLocked(key, candidate, false)
		return CandidatePromotionResult{}
	}

	if !shouldPromoteSeedCandidate(candidate) {
		return CandidatePromotionResult{}
	}

	if p.candidateHandler != nil {
		promotion := CandidatePromotionRequest{
			Key:        key,
			Seed:       normalizeSeed(candidate.seed),
			Evidence:   append([]SeedEvidence(nil), candidate.evidence...),
			Confidence: candidate.maxConfidence,
			Reasoned:   candidate.reasoned,
		}
		decision := p.candidateHandler.HandleCandidatePromotion(promotion)
		switch decision {
		case CandidatePromotionAccepted:
			return CandidatePromotionResult{
				Decision:  CandidatePromotionAccepted,
				Scheduled: p.materializeSeedCandidateLocked(key, candidate, mode == seedSchedulingNextWave),
			}
		case CandidatePromotionPendingReview, CandidatePromotionRejected:
			delete(p.candidateSeeds, key)
			return CandidatePromotionResult{Decision: decision}
		}
	}

	return CandidatePromotionResult{
		Decision:  CandidatePromotionAccepted,
		Scheduled: p.materializeSeedCandidateLocked(key, candidate, mode == seedSchedulingNextWave),
	}
}

// EnqueueSeedCandidate records evidence for an auto-discovered seed and
// promotes it once it has either a reasoned approval, one strong signal, or at
// least two distinct weaker signals.
func (p *PipelineContext) EnqueueSeedCandidate(seed Seed, evidence SeedEvidence) bool {
	return p.PromoteSeedCandidate(seed, evidence).Scheduled
}

// AdvanceSeedFrontier moves newly discovered seeds into the next collection wave.
func (p *PipelineContext) AdvanceSeedFrontier() bool {
	p.Lock()
	defer p.Unlock()

	if len(p.pendingSeeds) == 0 {
		if p.extraCollectionWaveReserved && !p.extraCollectionWaveInProgress {
			p.extraCollectionWaveReserved = false
		}
		p.collectionSeeds = nil
		return false
	}

	if p.extraCollectionWaveReserved && !p.extraCollectionWaveInProgress {
		p.extraCollectionWaveReserved = false
		p.extraCollectionWaveInProgress = true
		p.collectionSeeds = append([]Seed(nil), p.pendingSeeds...)
		p.pendingSeeds = nil
		return true
	}

	if p.extraCollectionWaveInProgress {
		p.pendingSeeds = nil
		p.collectionSeeds = nil
		return false
	}

	p.collectionDepth++
	p.collectionSeeds = append([]Seed(nil), p.pendingSeeds...)
	p.pendingSeeds = nil

	return true
}

type seedSchedulingMode int

const (
	seedSchedulingReject seedSchedulingMode = iota
	seedSchedulingNextWave
	seedSchedulingRegisterOnly
)

func (p *PipelineContext) seedSchedulingModeLocked() seedSchedulingMode {
	switch {
	case p.extraCollectionWaveInProgress:
		return seedSchedulingRegisterOnly
	case p.extraCollectionWaveReserved:
		return seedSchedulingNextWave
	case p.collectionDepth < p.maxCollectionDepth:
		return seedSchedulingNextWave
	default:
		return seedSchedulingReject
	}
}

func shouldPromoteSeedCandidate(candidate *seedCandidate) bool {
	if candidate == nil {
		return false
	}

	if candidate.reasoned {
		return true
	}

	if candidate.maxConfidence >= 0.9 {
		return true
	}

	return len(candidate.signalKeys) >= 2
}
