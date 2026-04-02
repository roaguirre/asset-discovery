package expand

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"asset-discovery/internal/discovery"
	"asset-discovery/internal/models"
	"asset-discovery/internal/ownership"
	"asset-discovery/internal/search"
	"asset-discovery/internal/tracing/lineage"
	"asset-discovery/internal/tracing/telemetry"
)

const (
	aiSearchCollectorSource         = "ai_search_collector"
	aiSearchRelationKind            = "search_result"
	aiSearchSeedTag                 = "ai-search-pivot"
	aiSearchDefaultMaxPromptChars   = 120000
	aiSearchDefaultMaxJudgeCount    = 12
	aiSearchMaxKnownRoots           = 12
	aiSearchMaxRelatedRoots         = 10
	aiSearchMaxRegistrationFacts    = 12
	aiSearchMaxObservedHosts        = 12
	aiSearchMaxObservedRoots        = 12
	aiSearchMaxSupportLineLength    = 280
	aiSearchMaxRelationReasonLength = 320
)

var aiSearchRootDenylist = map[string]struct{}{
	"akamai.net":     {},
	"amazonaws.com":  {},
	"apple.com":      {},
	"atlassian.net":  {},
	"azure.com":      {},
	"cloudflare.com": {},
	"facebook.com":   {},
	"github.com":     {},
	"github.io":      {},
	"gitlab.com":     {},
	"google.com":     {},
	"googleapis.com": {},
	"hubspot.com":    {},
	"instagram.com":  {},
	"linkedin.com":   {},
	"microsoft.com":  {},
	"myshopify.com":  {},
	"office.com":     {},
	"okta.com":       {},
	"salesforce.com": {},
	"shopify.com":    {},
	"slack.com":      {},
	"whatsapp.com":   {},
	"x.com":          {},
	"youtube.com":    {},
	"zendesk.com":    {},
}

// AISearchCollector uses web-backed search plus the ownership judge to
// conservatively propose additional registrable roots after enrichment.
type AISearchCollector struct {
	provider            search.Provider
	judge               ownership.Judge
	now                 func() time.Time
	maxPromptChars      int
	maxJudgedCandidates int
}

// AISearchCollectorOption configures AISearchCollector behavior.
type AISearchCollectorOption func(*AISearchCollector)

// WithAISearchProvider injects the bounded web-search provider.
func WithAISearchProvider(provider search.Provider) AISearchCollectorOption {
	return func(collector *AISearchCollector) {
		collector.provider = provider
	}
}

// WithAISearchJudge injects the final ownership judge used after web search.
func WithAISearchJudge(judge ownership.Judge) AISearchCollectorOption {
	return func(collector *AISearchCollector) {
		collector.judge = judge
	}
}

// WithAISearchNow overrides the wall clock used for emitted assets and
// relations. Tests use this to make timestamps deterministic.
func WithAISearchNow(now func() time.Time) AISearchCollectorOption {
	return func(collector *AISearchCollector) {
		if now != nil {
			collector.now = now
		}
	}
}

// WithAISearchMaxPromptChars sets the maximum estimated prompt size for each
// ownership-judge request emitted by the collector.
func WithAISearchMaxPromptChars(limit int) AISearchCollectorOption {
	return func(collector *AISearchCollector) {
		collector.maxPromptChars = limit
	}
}

// WithAISearchMaxJudgedCandidates caps how many filtered search candidates the
// collector will send to the ownership judge for each seed root.
func WithAISearchMaxJudgedCandidates(limit int) AISearchCollectorOption {
	return func(collector *AISearchCollector) {
		collector.maxJudgedCandidates = limit
	}
}

// NewAISearchCollector constructs the post-enrichment AI search expander.
func NewAISearchCollector(options ...AISearchCollectorOption) *AISearchCollector {
	collector := &AISearchCollector{
		provider:            nil,
		judge:               ownership.NewDefaultJudge(),
		now:                 time.Now,
		maxPromptChars:      aiSearchDefaultMaxPromptChars,
		maxJudgedCandidates: aiSearchDefaultMaxJudgeCount,
	}

	for _, option := range options {
		if option != nil {
			option(collector)
		}
	}

	return collector
}

// Process runs one bounded AI-search pass for each active frontier root that
// has not already been searched during the current run.
func (c *AISearchCollector) Process(ctx context.Context, pCtx *models.PipelineContext) (*models.PipelineContext, error) {
	telemetry.Info(ctx, "[AI Search Collector] Reviewing enriched frontier roots...")
	if pCtx == nil {
		return nil, nil
	}
	pCtx.EnsureAssetState()

	if c.provider == nil {
		pCtx.EmitExecutionEvent(models.ExecutionEvent{
			Kind:    "ai_search_disabled",
			Message: "AI search collector is disabled; no web-search expansion will run.",
			Metadata: map[string]interface{}{
				"collector": aiSearchCollectorSource,
			},
		})
		return pCtx, nil
	}
	if c.judge == nil {
		pCtx.EmitExecutionEvent(models.ExecutionEvent{
			Kind:    "ai_search_judge_disabled",
			Message: "AI search collector skipped because the ownership judge is disabled.",
			Metadata: map[string]interface{}{
				"collector": aiSearchCollectorSource,
				"judge":     "ownership",
			},
		})
		return pCtx, nil
	}

	for _, seed := range pCtx.CollectionSeeds() {
		for _, focusRoot := range uniqueSeedRoots(seed) {
			if pCtx.HasAISearchExecutedRoot(focusRoot) {
				telemetry.Infof(ctx, "[AI Search Collector] Skipping %s because this root already ran earlier in the run.", focusRoot)
				continue
			}

			pCtx.MarkAISearchExecutedRoot(focusRoot)
			enum := models.Enumeration{
				ID:        models.NewID("enum-ai-search"),
				SeedID:    seed.ID,
				Status:    "running",
				CreatedAt: c.now(),
				StartedAt: c.now(),
			}
			appendEnumeration(pCtx, enum)

			snapshot := pCtx.SnapshotReadModel()
			summary := buildAISearchSummary(seed, focusRoot, snapshot)
			result, err := c.provider.Search(ctx, summary)
			if err != nil {
				appendPipelineError(pCtx, err)
				pCtx.EmitExecutionEvent(models.ExecutionEvent{
					Kind:    "ai_search_failed",
					Message: fmt.Sprintf("AI search collector failed for %s.", focusRoot),
					Metadata: map[string]interface{}{
						"collector":  aiSearchCollectorSource,
						"focus_root": focusRoot,
						"error":      err.Error(),
					},
				})
				continue
			}

			filteredCandidates := filterAISearchCandidates(seed, focusRoot, result.Candidates, snapshot, pCtx)
			if len(filteredCandidates) == 0 {
				pCtx.EmitExecutionEvent(models.ExecutionEvent{
					Kind:    "ai_search_no_candidates",
					Message: fmt.Sprintf("AI search collector found no judgeable candidates for %s.", focusRoot),
					Metadata: map[string]interface{}{
						"collector":           aiSearchCollectorSource,
						"focus_root":          focusRoot,
						"query_count":         len(result.Queries),
						"raw_candidate_count": len(result.Candidates),
					},
				})
				continue
			}

			batchPlan := c.buildJudgeRequests(seed, focusRoot, filteredCandidates)

			pCtx.EmitExecutionEvent(models.ExecutionEvent{
				Kind:    "ai_search_candidates_discovered",
				Message: fmt.Sprintf("AI search collector found %d candidate root(s) for %s.", len(filteredCandidates), focusRoot),
				Metadata: map[string]interface{}{
					"collector":       aiSearchCollectorSource,
					"focus_root":      focusRoot,
					"query_count":     len(result.Queries),
					"candidate_count": len(filteredCandidates),
					"candidate_roots": searchCandidateRoots(filteredCandidates),
					"queries":         append([]string(nil), result.Queries...),
				},
			})
			if len(batchPlan.truncatedRoots) > 0 {
				pCtx.EmitExecutionEvent(models.ExecutionEvent{
					Kind:    "ai_search_candidate_truncated",
					Message: fmt.Sprintf("AI search collector truncated %d candidate root(s) for %s due to the per-seed judge cap.", len(batchPlan.truncatedRoots), focusRoot),
					Metadata: map[string]interface{}{
						"collector":             aiSearchCollectorSource,
						"focus_root":            focusRoot,
						"max_judged_candidates": c.maxJudgedCandidates,
						"truncated_roots":       append([]string(nil), batchPlan.truncatedRoots...),
					},
				})
			}
			if len(batchPlan.promptSkippedRoots) > 0 {
				pCtx.EmitExecutionEvent(models.ExecutionEvent{
					Kind:    "ai_search_prompt_skipped",
					Message: fmt.Sprintf("AI search collector skipped %d oversized candidate root(s) for %s.", len(batchPlan.promptSkippedRoots), focusRoot),
					Metadata: map[string]interface{}{
						"collector":      aiSearchCollectorSource,
						"focus_root":     focusRoot,
						"skipped_roots":  append([]string(nil), batchPlan.promptSkippedRoots...),
						"prompt_ceiling": c.maxPromptChars,
					},
				})
			}
			if len(batchPlan.selectedRoots) > 0 {
				pCtx.EmitExecutionEvent(models.ExecutionEvent{
					Kind:    "ai_search_candidates_selected",
					Message: fmt.Sprintf("AI search collector selected %d candidate root(s) for judging for %s.", len(batchPlan.selectedRoots), focusRoot),
					Metadata: map[string]interface{}{
						"collector":      aiSearchCollectorSource,
						"focus_root":     focusRoot,
						"request_count":  len(batchPlan.requests),
						"selected_count": len(batchPlan.selectedRoots),
						"selected_roots": append([]string(nil), batchPlan.selectedRoots...),
					},
				})
			}
			if len(batchPlan.requests) == 0 {
				continue
			}

			candidateByRoot := make(map[string]search.SearchCandidate, len(filteredCandidates))
			for _, candidate := range filteredCandidates {
				candidateByRoot[candidate.Root] = candidate
			}

			acceptedRoots := make([]string, 0)
			judgedRoots := make([]string, 0)
			lowConfidenceRoots := make([]string, 0)
			promotedRoots := make([]string, 0)

			for _, group := range batchPlan.requests {
				decisions, err := c.judge.EvaluateCandidates(ctx, group.request)
				if err != nil {
					appendPipelineError(pCtx, err)
					continue
				}

				judgedRoots = append(judgedRoots, group.roots...)
				lineage.RecordOwnershipJudgeEvaluation(pCtx, aiSearchCollectorSource, group.request, decisions)

				for _, decision := range decisions {
					if !decision.Collect {
						continue
					}

					candidate, exists := candidateByRoot[decision.Root]
					if !exists {
						continue
					}

					acceptedRoots = appendUniqueString(acceptedRoots, decision.Root)

					if !ownership.IsConfidenceAtLeast(
						decision.Confidence,
						pCtx.CandidatePromotionConfidenceThreshold(),
					) {
						lowConfidenceRoots = appendUniqueString(lowConfidenceRoots, decision.Root)
						continue
					}

					discoveredSeed := discovery.BuildDiscoveredSeed(seed, decision.Root, aiSearchSeedTag)
					promotion := pCtx.PromoteSeedCandidate(discoveredSeed, models.SeedEvidence{
						Source:     aiSearchCollectorSource,
						Kind:       firstNonEmpty(decision.Kind, "ownership_judged"),
						Value:      decision.Root,
						Confidence: decision.Confidence,
						Reasoned:   true,
					})
					if promotion.Decision == models.CandidatePromotionAccepted {
						pCtx.AppendAssets(models.Asset{
							ID:            models.NewID("dom-ai-search"),
							EnumerationID: enum.ID,
							Type:          models.AssetTypeDomain,
							Identifier:    decision.Root,
							Source:        aiSearchCollectorSource,
							DiscoveryDate: c.now(),
							DomainDetails: &models.DomainDetails{},
						})
						pCtx.AppendAssetRelations(models.AssetRelation{
							ID:             models.NewID("rel-ai-search"),
							EnumerationID:  enum.ID,
							FromAssetType:  models.AssetTypeDomain,
							FromIdentifier: focusRoot,
							ToAssetType:    models.AssetTypeDomain,
							ToIdentifier:   decision.Root,
							Source:         aiSearchCollectorSource,
							Kind:           aiSearchRelationKind,
							Label:          "Search Result",
							Reason:         buildSearchRelationReason(result.Queries, candidate),
							DiscoveryDate:  c.now(),
						})
					}

					if promotion.Scheduled {
						promotedRoots = appendUniqueString(promotedRoots, decision.Root)
						telemetry.Infof(ctx, "[AI Search Collector] Promoted %s from judged web search evidence.", decision.Root)
					}
				}
			}

			sort.Strings(acceptedRoots)
			judgedRoots = uniquePreservingOrder(judgedRoots)
			sort.Strings(lowConfidenceRoots)
			sort.Strings(promotedRoots)
			discardedRoots := differencePreservingOrder(judgedRoots, acceptedRoots)

			pCtx.EmitExecutionEvent(models.ExecutionEvent{
				Kind:    "ai_search_judge_completed",
				Message: fmt.Sprintf("AI search judge evaluated %d candidate root(s) for %s.", len(judgedRoots), focusRoot),
				Metadata: map[string]interface{}{
					"collector":       aiSearchCollectorSource,
					"focus_root":      focusRoot,
					"judged_count":    len(judgedRoots),
					"judged_roots":    judgedRoots,
					"accepted_count":  len(acceptedRoots),
					"accepted_roots":  acceptedRoots,
					"discarded_count": len(discardedRoots),
					"discarded_roots": discardedRoots,
					"promoted_count":  len(promotedRoots),
					"promoted_roots":  promotedRoots,
				},
			})
			if len(lowConfidenceRoots) > 0 {
				pCtx.EmitExecutionEvent(models.ExecutionEvent{
					Kind:    "ai_search_low_confidence_skipped",
					Message: fmt.Sprintf("AI search collector skipped %d accepted candidate root(s) for %s due to the promotion threshold.", len(lowConfidenceRoots), focusRoot),
					Metadata: map[string]interface{}{
						"collector":            aiSearchCollectorSource,
						"focus_root":           focusRoot,
						"confidence_threshold": pCtx.CandidatePromotionConfidenceThreshold(),
						"skipped_roots":        lowConfidenceRoots,
					},
				})
			}
		}
	}

	return pCtx, nil
}

type judgeRequestGroup struct {
	request ownership.Request
	roots   []string
}

// judgeBatchPlan captures the candidates that survive each collector-side
// pruning step before the ownership judge runs.
type judgeBatchPlan struct {
	requests           []judgeRequestGroup
	selectedRoots      []string
	truncatedRoots     []string
	promptSkippedRoots []string
}

func (c *AISearchCollector) buildJudgeRequests(
	seed models.Seed,
	focusRoot string,
	candidates []search.SearchCandidate,
) judgeBatchPlan {
	ordered := append([]search.SearchCandidate(nil), candidates...)
	plan := judgeBatchPlan{}
	if c.maxJudgedCandidates > 0 && len(ordered) > c.maxJudgedCandidates {
		plan.truncatedRoots = searchCandidateRoots(ordered[c.maxJudgedCandidates:])
		ordered = ordered[:c.maxJudgedCandidates]
	}

	current := ownership.Request{
		Scenario: "AI web search expansion from " + focusRoot,
		Seed:     seed,
	}
	currentRoots := make([]string, 0)

	appendCurrent := func() {
		if len(current.Candidates) == 0 {
			return
		}
		plan.requests = append(plan.requests, judgeRequestGroup{
			request: current,
			roots:   append([]string(nil), currentRoots...),
		})
		current = ownership.Request{
			Scenario: "AI web search expansion from " + focusRoot,
			Seed:     seed,
		}
		currentRoots = currentRoots[:0]
	}

	for _, candidate := range ordered {
		ownershipCandidate := ownershipCandidateFromSearch(candidate)
		trial := ownership.Request{
			Scenario:   current.Scenario,
			Seed:       current.Seed,
			Candidates: append(append([]ownership.Candidate(nil), current.Candidates...), ownershipCandidate),
		}
		if c.maxPromptChars > 0 && ownership.EstimatePromptSize(trial) > c.maxPromptChars {
			if len(current.Candidates) == 0 {
				plan.promptSkippedRoots = append(plan.promptSkippedRoots, candidate.Root)
				continue
			}
			appendCurrent()
			trial = ownership.Request{
				Scenario:   current.Scenario,
				Seed:       current.Seed,
				Candidates: []ownership.Candidate{ownershipCandidate},
			}
			if c.maxPromptChars > 0 && ownership.EstimatePromptSize(trial) > c.maxPromptChars {
				plan.promptSkippedRoots = append(plan.promptSkippedRoots, candidate.Root)
				continue
			}
		}
		current.Candidates = trial.Candidates
		currentRoots = append(currentRoots, candidate.Root)
		plan.selectedRoots = append(plan.selectedRoots, candidate.Root)
	}

	appendCurrent()
	return plan
}

func buildAISearchSummary(
	seed models.Seed,
	focusRoot string,
	snapshot *models.PipelineContext,
) search.ContextSummary {
	acceptedRoots, discardedRoots := judgeRootsForSeed(seed, snapshot)
	knownRoots := unionStringSets(
		uniqueSeedRoots(seed),
		acceptedRoots,
	)

	registrationFacts, observedHosts, observedRoots := assetFactsForSeed(seed, focusRoot, snapshot)

	return search.ContextSummary{
		SeedLabel:         aiSearchSeedLabel(seed),
		SeedDomains:       append([]string(nil), seed.Domains...),
		FocusRoot:         focusRoot,
		Industry:          strings.TrimSpace(seed.Industry),
		ASN:               append([]int(nil), seed.ASN...),
		CIDR:              append([]string(nil), seed.CIDR...),
		KnownRoots:        limitStrings(knownRoots, aiSearchMaxKnownRoots),
		AcceptedRoots:     limitStrings(acceptedRoots, aiSearchMaxRelatedRoots),
		DiscardedRoots:    limitStrings(discardedRoots, aiSearchMaxRelatedRoots),
		RegistrationFacts: limitStrings(registrationFacts, aiSearchMaxRegistrationFacts),
		ObservedHosts:     limitStrings(observedHosts, aiSearchMaxObservedHosts),
		ObservedRoots:     limitStrings(observedRoots, aiSearchMaxObservedRoots),
	}
}

func filterAISearchCandidates(
	seed models.Seed,
	focusRoot string,
	candidates []search.SearchCandidate,
	snapshot *models.PipelineContext,
	pCtx *models.PipelineContext,
) []search.SearchCandidate {
	acceptedRoots, discardedRoots := judgeRootsForSeed(seed, snapshot)
	knownRoots := make(map[string]struct{}, len(seed.Domains)+len(acceptedRoots)+1)
	for _, root := range uniqueSeedRoots(seed) {
		knownRoots[root] = struct{}{}
	}
	for _, root := range acceptedRoots {
		knownRoots[root] = struct{}{}
	}

	discardedSet := toStringSet(discardedRoots)
	filtered := make([]search.SearchCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		root := discovery.RegistrableDomain(candidate.Root)
		if root == "" || root == focusRoot {
			continue
		}
		if _, exists := knownRoots[root]; exists {
			continue
		}
		if _, exists := discardedSet[root]; exists {
			continue
		}
		if _, denied := aiSearchRootDenylist[root]; denied {
			continue
		}
		if pCtx != nil && pCtx.HasAISearchExecutedRoot(root) {
			continue
		}
		filtered = append(filtered, candidate)
	}
	return filtered
}

func ownershipCandidateFromSearch(candidate search.SearchCandidate) ownership.Candidate {
	evidence := make([]ownership.EvidenceItem, 0, len(candidate.Evidence)+1)
	evidence = append(evidence, ownership.EvidenceItem{
		Kind:    "ai_search_summary",
		Summary: truncateString(candidate.Summary, aiSearchMaxSupportLineLength),
	})
	for _, item := range candidate.Evidence {
		evidence = append(evidence, ownership.EvidenceItem{
			Kind: "search_result",
			Summary: truncateString(
				fmt.Sprintf("%s (%s): %s", item.Title, item.URL, item.Snippet),
				aiSearchMaxSupportLineLength,
			),
		})
	}

	return ownership.Candidate{
		Root:     candidate.Root,
		Evidence: evidence,
	}
}

func judgeRootsForSeed(seed models.Seed, snapshot *models.PipelineContext) ([]string, []string) {
	if snapshot == nil {
		return nil, nil
	}

	acceptedSet := make(map[string]struct{})
	discardedSet := make(map[string]struct{})
	for _, evaluation := range snapshot.JudgeEvaluations {
		if !judgeEvaluationMatchesSeed(seed, evaluation) {
			continue
		}
		for _, outcome := range evaluation.Outcomes {
			root := discovery.RegistrableDomain(outcome.Root)
			if root == "" {
				continue
			}
			if outcome.Collect {
				acceptedSet[root] = struct{}{}
				continue
			}
			discardedSet[root] = struct{}{}
		}
	}

	return sortedSetKeys(acceptedSet), sortedSetKeys(discardedSet)
}

func judgeEvaluationMatchesSeed(seed models.Seed, evaluation models.JudgeEvaluation) bool {
	if seed.ID != "" && strings.TrimSpace(evaluation.SeedID) != "" {
		return seed.ID == strings.TrimSpace(evaluation.SeedID)
	}
	if len(seed.Domains) > 0 && len(evaluation.SeedDomains) > 0 {
		return discovery.RootsOverlap(seed.Domains, evaluation.SeedDomains)
	}
	return strings.EqualFold(seed.CompanyName, evaluation.SeedLabel)
}

func assetFactsForSeed(
	seed models.Seed,
	focusRoot string,
	snapshot *models.PipelineContext,
) ([]string, []string, []string) {
	if snapshot == nil {
		return nil, nil, nil
	}

	enumToSeedID := make(map[string]string, len(snapshot.Enumerations))
	for _, enumeration := range snapshot.Enumerations {
		if enumeration.ID != "" && enumeration.SeedID != "" {
			enumToSeedID[enumeration.ID] = enumeration.SeedID
		}
	}

	registrationFacts := make([]string, 0)
	observedHosts := make([]string, 0)
	observedRoots := make([]string, 0)
	registrationSeen := make(map[string]struct{})
	hostSeen := make(map[string]struct{})
	rootSeen := make(map[string]struct{})

	for _, asset := range snapshot.Assets {
		if asset.Type != models.AssetTypeDomain || !assetBelongsToSeed(asset, seed, enumToSeedID) {
			continue
		}

		root := discovery.RegistrableDomain(asset.Identifier)
		if root == "" {
			continue
		}

		if root == focusRoot && asset.Identifier != focusRoot {
			appendUniqueLimitedString(&observedHosts, hostSeen, asset.Identifier, aiSearchMaxObservedHosts)
		}
		if root != focusRoot {
			appendUniqueLimitedString(&observedRoots, rootSeen, root, aiSearchMaxObservedRoots)
		}

		if asset.DomainDetails == nil || asset.DomainDetails.RDAP == nil {
			continue
		}
		rdap := asset.DomainDetails.RDAP
		appendUniqueLimitedString(&registrationFacts, registrationSeen, rdapFact(root, "Registrant org", rdap.RegistrantOrg), aiSearchMaxRegistrationFacts)
		appendUniqueLimitedString(&registrationFacts, registrationSeen, rdapFact(root, "Registrant email", rdap.RegistrantEmail), aiSearchMaxRegistrationFacts)
		appendUniqueLimitedString(&registrationFacts, registrationSeen, rdapFact(root, "Registrar", rdap.RegistrarName), aiSearchMaxRegistrationFacts)
		for _, nameServer := range rdap.NameServers {
			nameServerRoot := discovery.RegistrableDomain(nameServer)
			if nameServerRoot == "" {
				continue
			}
			appendUniqueLimitedString(
				&registrationFacts,
				registrationSeen,
				truncateString(fmt.Sprintf("Name server root for %s: %s", root, nameServerRoot), aiSearchMaxSupportLineLength),
				aiSearchMaxRegistrationFacts,
			)
		}
	}

	sort.Strings(registrationFacts)
	sort.Strings(observedHosts)
	sort.Strings(observedRoots)
	return registrationFacts, observedHosts, observedRoots
}

func assetBelongsToSeed(asset models.Asset, seed models.Seed, enumToSeedID map[string]string) bool {
	if seed.ID != "" {
		for _, enumerationID := range assetContributorEnumerationIDs(asset) {
			if enumToSeedID[enumerationID] == seed.ID {
				return true
			}
		}
	}
	if len(seed.Domains) > 0 {
		return discovery.RootsOverlap([]string{asset.Identifier}, seed.Domains)
	}
	return false
}

func assetContributorEnumerationIDs(asset models.Asset) []string {
	values := make([]string, 0, len(asset.Provenance)+1)
	if asset.EnumerationID != "" {
		values = append(values, asset.EnumerationID)
	}
	for _, item := range asset.Provenance {
		if item.EnumerationID != "" {
			values = append(values, item.EnumerationID)
		}
	}
	return uniquePreservingOrder(values)
}

func buildSearchRelationReason(queries []string, candidate search.SearchCandidate) string {
	reason := strings.TrimSpace(candidate.Summary)
	if len(queries) > 0 {
		reason = fmt.Sprintf("Query %q surfaced this root. %s", queries[0], reason)
	}
	return truncateString(reason, aiSearchMaxRelationReasonLength)
}

func searchCandidateRoots(candidates []search.SearchCandidate) []string {
	roots := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		roots = append(roots, candidate.Root)
	}
	return uniquePreservingOrder(roots)
}

func uniqueSeedRoots(seed models.Seed) []string {
	roots := make([]string, 0, len(seed.Domains))
	seen := make(map[string]struct{}, len(seed.Domains))
	for _, domain := range seed.Domains {
		root := discovery.RegistrableDomain(domain)
		if root == "" {
			continue
		}
		if _, exists := seen[root]; exists {
			continue
		}
		seen[root] = struct{}{}
		roots = append(roots, root)
	}
	sort.Strings(roots)
	return roots
}

func aiSearchSeedLabel(seed models.Seed) string {
	if company := strings.TrimSpace(seed.CompanyName); company != "" {
		return company
	}
	if len(seed.Domains) > 0 {
		return seed.Domains[0]
	}
	return strings.TrimSpace(seed.ID)
}

func rdapFact(root string, label string, value string) string {
	value = strings.TrimSpace(value)
	if root == "" || label == "" || value == "" {
		return ""
	}
	return truncateString(fmt.Sprintf("%s for %s: %s", label, root, value), aiSearchMaxSupportLineLength)
}

func unionStringSets(groups ...[]string) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0)
	for _, group := range groups {
		for _, value := range group {
			value = strings.TrimSpace(strings.ToLower(value))
			if value == "" {
				continue
			}
			if _, exists := seen[value]; exists {
				continue
			}
			seen[value] = struct{}{}
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

func toStringSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		normalized := strings.TrimSpace(strings.ToLower(value))
		if normalized == "" {
			continue
		}
		set[normalized] = struct{}{}
	}
	return set
}

func sortedSetKeys(values map[string]struct{}) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func appendPipelineError(pCtx *models.PipelineContext, err error) {
	if pCtx == nil || err == nil {
		return
	}
	pCtx.Lock()
	defer pCtx.Unlock()
	pCtx.Errors = append(pCtx.Errors, err)
}

func appendEnumeration(pCtx *models.PipelineContext, enumeration models.Enumeration) {
	if pCtx == nil {
		return
	}
	pCtx.Lock()
	defer pCtx.Unlock()
	pCtx.Enumerations = append(pCtx.Enumerations, enumeration)
}

func limitStrings(values []string, limit int) []string {
	if limit <= 0 || len(values) <= limit {
		return append([]string(nil), values...)
	}
	return append([]string(nil), values[:limit]...)
}

func appendUniqueLimitedString(target *[]string, seen map[string]struct{}, value string, limit int) {
	value = strings.TrimSpace(value)
	if value == "" || (limit > 0 && len(*target) >= limit) {
		return
	}
	key := strings.ToLower(value)
	if _, exists := seen[key]; exists {
		return
	}
	seen[key] = struct{}{}
	*target = append(*target, value)
}

func appendUniqueString(values []string, candidate string) []string {
	for _, value := range values {
		if value == candidate {
			return values
		}
	}
	return append(values, candidate)
}

func differencePreservingOrder(values []string, excluded []string) []string {
	if len(values) == 0 {
		return nil
	}

	excludedSet := toStringSet(excluded)
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, exists := excludedSet[strings.ToLower(strings.TrimSpace(value))]; exists {
			continue
		}
		out = append(out, value)
	}
	return uniquePreservingOrder(out)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func truncateString(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len(value) <= limit {
		return value
	}
	if limit <= 3 {
		return value[:limit]
	}
	return value[:limit-3] + "..."
}

func uniquePreservingOrder(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
