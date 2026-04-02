package models

import "strings"

// Lock acquires the mutex for safe concurrent mutation of the context.
func (p *PipelineContext) Lock() {
	p.mu.Lock()
}

// Unlock releases the mutex.
func (p *PipelineContext) Unlock() {
	p.mu.Unlock()
}

// SetCandidatePromotionHandler installs the handler that decides whether a
// discovered seed is accepted immediately, queued for review, or rejected.
func (p *PipelineContext) SetCandidatePromotionHandler(handler CandidatePromotionHandler) {
	p.Lock()
	defer p.Unlock()

	p.candidateHandler = handler
}

// SetMutationListener installs the observer that receives live updates as the
// pipeline appends assets, observations, relations, judge evaluations, and
// execution events.
func (p *PipelineContext) SetMutationListener(listener MutationListener) {
	p.Lock()
	defer p.Unlock()

	p.mutationListener = listener
}

// EmitExecutionEvent forwards a runtime activity update to the installed live
// observer when the event carries meaningful content.
func (p *PipelineContext) EmitExecutionEvent(event ExecutionEvent) {
	if p == nil {
		return
	}

	event.Kind = strings.TrimSpace(event.Kind)
	event.Message = strings.TrimSpace(event.Message)
	if event.Kind == "" || event.Message == "" {
		return
	}

	p.Lock()
	listener := p.mutationListener
	p.Unlock()

	if listener != nil {
		listener.OnExecutionEvent(event)
	}
}

// SetCandidatePromotionConfidenceThreshold configures the minimum confidence
// required before judge-approved candidates are promoted into the seed frontier.
func (p *PipelineContext) SetCandidatePromotionConfidenceThreshold(threshold float64) {
	p.Lock()
	defer p.Unlock()

	switch {
	case threshold < 0:
		threshold = 0
	case threshold > 1:
		threshold = 1
	}

	p.candidatePromotionThreshold = threshold
	p.candidatePromotionPolicySet = true
}

// CandidatePromotionConfidenceThreshold returns the minimum confidence required
// for judge-approved candidates to enter the promotion flow for this run.
func (p *PipelineContext) CandidatePromotionConfidenceThreshold() float64 {
	p.Lock()
	defer p.Unlock()

	if !p.candidatePromotionPolicySet {
		return defaultCandidatePromotionConfidenceThreshold
	}
	return p.candidatePromotionThreshold
}

// SnapshotSchedulerState captures the scheduler-owned runtime fields needed to
// resume collection waves without persisting internal mutex state.
func (p *PipelineContext) SnapshotSchedulerState() SchedulerState {
	p.Lock()
	defer p.Unlock()

	return SchedulerState{
		CollectionSeeds:               append([]Seed(nil), p.collectionSeeds...),
		PendingSeeds:                  append([]Seed(nil), p.pendingSeeds...),
		CollectionDepth:               p.collectionDepth,
		MaxCollectionDepth:            p.maxCollectionDepth,
		ExtraCollectionWaveReserved:   p.extraCollectionWaveReserved,
		ExtraCollectionWaveInProgress: p.extraCollectionWaveInProgress,
		AISearchExecutedRoots:         append([]string(nil), p.AISearchExecutedRoots...),
	}
}

// RestoreSchedulerState rehydrates the scheduler frontier bookkeeping from a
// checkpoint snapshot before execution resumes.
func (p *PipelineContext) RestoreSchedulerState(state SchedulerState) {
	p.Lock()
	defer p.Unlock()

	p.collectionSeeds = append([]Seed(nil), state.CollectionSeeds...)
	p.pendingSeeds = append([]Seed(nil), state.PendingSeeds...)
	p.collectionDepth = state.CollectionDepth
	p.maxCollectionDepth = state.MaxCollectionDepth
	p.extraCollectionWaveReserved = state.ExtraCollectionWaveReserved
	p.extraCollectionWaveInProgress = state.ExtraCollectionWaveInProgress
	p.AISearchExecutedRoots = append([]string(nil), state.AISearchExecutedRoots...)
	p.knownSeedKeys = make(map[string]struct{}, len(p.Seeds))
	for _, seed := range p.Seeds {
		p.knownSeedKeys[seedKey(seed)] = struct{}{}
	}
	p.candidateSeeds = make(map[string]*seedCandidate)
}
