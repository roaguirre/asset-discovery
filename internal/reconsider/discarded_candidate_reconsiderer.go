package reconsider

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"asset-discovery/internal/discovery"
	"asset-discovery/internal/models"
	"asset-discovery/internal/ownership"
	"asset-discovery/internal/tracing/lineage"
	"asset-discovery/internal/tracing/telemetry"
)

const (
	defaultMaxPromptChars     = 1200000
	reconsiderationCollector  = "run_reconsideration"
	reconsiderationScenario   = "post-run discarded candidate reconsideration"
	reconsiderationSeedTag    = "post-run-reconsideration"
	reconsiderationSeedSource = "post_run_reconsideration"
)

type DiscardedCandidateReconsiderer struct {
	judge          ownership.Judge
	maxPromptChars int
}

type Option func(*DiscardedCandidateReconsiderer)

func WithDiscardedCandidateReconsidererJudge(judge ownership.Judge) Option {
	return func(r *DiscardedCandidateReconsiderer) {
		r.judge = judge
	}
}

// WithDiscardedCandidateReconsidererMaxPromptChars sets the max estimated
// prompt size for each reconsideration request group.
func WithDiscardedCandidateReconsidererMaxPromptChars(limit int) Option {
	return func(r *DiscardedCandidateReconsiderer) {
		r.maxPromptChars = limit
	}
}

func NewDiscardedCandidateReconsiderer(options ...Option) *DiscardedCandidateReconsiderer {
	reconsiderer := &DiscardedCandidateReconsiderer{
		judge:          ownership.NewDefaultJudge(),
		maxPromptChars: defaultMaxPromptChars,
	}

	for _, option := range options {
		if option != nil {
			option(reconsiderer)
		}
	}

	return reconsiderer
}

func (r *DiscardedCandidateReconsiderer) Process(ctx context.Context, pCtx *models.PipelineContext) (*models.PipelineContext, error) {
	telemetry.Info(ctx, "[Discarded Candidate Reconsiderer] Reviewing discarded candidates...")
	if pCtx != nil {
		pCtx.EnsureAssetState()
	}

	if pCtx == nil || r.judge == nil || len(pCtx.JudgeEvaluations) == 0 {
		return pCtx, nil
	}

	seedIndex := buildSeedIndex(pCtx.Seeds)
	knownRoots := buildKnownSeedRoots(pCtx.Seeds)
	acceptedRoots := buildAcceptedRootSet(pCtx.JudgeEvaluations, seedIndex, knownRoots)
	rootFacts := buildRunRootFacts(pCtx.Assets)
	aggregates := aggregateDiscardedCandidates(pCtx.JudgeEvaluations, seedIndex, knownRoots, acceptedRoots)
	if len(aggregates) == 0 {
		return pCtx, nil
	}

	requestGroups := buildRequestGroups(aggregates, rootFacts, acceptedRoots, pCtx)
	if len(requestGroups) == 0 {
		return pCtx, nil
	}

	totalCandidates := 0
	for _, group := range requestGroups {
		totalCandidates += len(group.request.Candidates)
	}

	promoted := 0
	skippedGroups := 0
	for _, group := range requestGroups {
		promptChars := ownership.EstimatePromptSize(group.request)
		if r.maxPromptChars > 0 && promptChars > r.maxPromptChars {
			err := fmt.Errorf(
				"post-run reconsideration skipped request group %q: prompt size %d exceeds per-request limit %d",
				groupLabel(group),
				promptChars,
				r.maxPromptChars,
			)
			appendPipelineError(pCtx, err)
			telemetry.Infof(ctx, "[Discarded Candidate Reconsiderer] %v.", err)
			skippedGroups++
			continue
		}

		decisions, err := r.judge.EvaluateCandidates(ctx, group.request)
		if err != nil {
			appendPipelineError(pCtx, err)
			continue
		}

		lineage.RecordOwnershipJudgeEvaluation(pCtx, reconsiderationCollector, group.request, decisions)

		for _, decision := range decisions {
			if !decision.Collect {
				continue
			}
			if !ownership.IsHighConfidence(decision.Confidence) {
				telemetry.Infof(
					ctx,
					"[Discarded Candidate Reconsiderer] Skipping %s due to low-confidence judge decision %.2f.",
					decision.Root,
					decision.Confidence,
				)
				continue
			}

			aggregate, exists := group.byRoot[decision.Root]
			if !exists {
				continue
			}

			discoveredSeed := discovery.BuildDiscoveredSeed(aggregate.parentSeed, decision.Root, reconsiderationSeedTag)
			if pCtx.EnqueueSeedCandidate(discoveredSeed, models.SeedEvidence{
				Source:     reconsiderationSeedSource,
				Kind:       firstNonEmpty(decision.Kind, "ownership_judged"),
				Value:      decision.Root,
				Confidence: decision.Confidence,
				Reasoned:   true,
			}) {
				promoted++
				telemetry.Infof(
					ctx,
					"[Discarded Candidate Reconsiderer] Promoted %s for one final collection wave.",
					decision.Root,
				)
			}
		}
	}

	telemetry.Infof(
		ctx,
		"[Discarded Candidate Reconsiderer] Reviewed %d discarded candidates across %d parent seeds; promoted %d; skipped %d oversized request groups.",
		totalCandidates,
		len(requestGroups),
		promoted,
		skippedGroups,
	)

	return pCtx, nil
}

type seedIndex struct {
	byID      map[string]models.Seed
	byDomains map[string]models.Seed
	byLabel   map[string]models.Seed
}

func buildSeedIndex(seeds []models.Seed) seedIndex {
	index := seedIndex{
		byID:      make(map[string]models.Seed, len(seeds)),
		byDomains: make(map[string]models.Seed, len(seeds)),
		byLabel:   make(map[string]models.Seed, len(seeds)),
	}

	for _, seed := range seeds {
		if seed.ID != "" {
			index.byID[strings.TrimSpace(seed.ID)] = seed
		}
		if key := domainsKey(seed.Domains); key != "" {
			index.byDomains[key] = seed
		}
		if label := strings.ToLower(strings.TrimSpace(seed.CompanyName)); label != "" {
			index.byLabel[label] = seed
		}
	}

	return index
}

func buildKnownSeedRoots(seeds []models.Seed) map[string]struct{} {
	roots := make(map[string]struct{})
	for _, seed := range seeds {
		for _, domain := range seed.Domains {
			if root := discovery.RegistrableDomain(domain); root != "" {
				roots[root] = struct{}{}
			}
		}
	}
	return roots
}

func buildAcceptedRootSet(
	evaluations []models.JudgeEvaluation,
	seeds seedIndex,
	knownRoots map[string]struct{},
) map[string]map[string]struct{} {
	accepted := make(map[string]map[string]struct{})
	for _, evaluation := range evaluations {
		parentSeed := resolveParentSeed(evaluation, seeds)
		parentKey := canonicalParentKey(parentSeed, evaluation)
		if parentKey == "" {
			continue
		}

		for _, outcome := range evaluation.Outcomes {
			if !outcome.Collect {
				continue
			}
			root := discovery.RegistrableDomain(outcome.Root)
			if root == "" {
				continue
			}
			if _, exists := knownRoots[root]; !exists {
				continue
			}
			if _, exists := accepted[parentKey]; !exists {
				accepted[parentKey] = make(map[string]struct{})
			}
			accepted[parentKey][root] = struct{}{}
		}
	}

	return accepted
}

type rootFacts struct {
	assetCount      int
	provenanceCount int
	identifiers     []string
	sources         []string
	ptrHosts        []string
	registrantOrgs  []string
	registrars      []string
	nameServers     []string
}

func buildRunRootFacts(assets []models.Asset) map[string]*rootFacts {
	factsByRoot := make(map[string]*rootFacts)

	ensure := func(root string) *rootFacts {
		if root == "" {
			return nil
		}
		facts := factsByRoot[root]
		if facts == nil {
			facts = &rootFacts{}
			factsByRoot[root] = facts
		}
		return facts
	}

	for _, asset := range assets {
		switch asset.Type {
		case models.AssetTypeDomain:
			root := discovery.RegistrableDomain(asset.Identifier)
			facts := ensure(root)
			if facts == nil {
				continue
			}
			facts.assetCount++
			facts.provenanceCount += maxInt(1, len(asset.Provenance))
			facts.identifiers = appendUniqueValue(facts.identifiers, discovery.NormalizeDomainIdentifier(asset.Identifier))
			facts.sources = appendUniqueValues(facts.sources, splitSources(asset.Source)...)
			if asset.DomainDetails != nil && asset.DomainDetails.RDAP != nil {
				facts.registrantOrgs = appendUniqueValue(facts.registrantOrgs, strings.TrimSpace(asset.DomainDetails.RDAP.RegistrantOrg))
				facts.registrars = appendUniqueValue(facts.registrars, strings.TrimSpace(asset.DomainDetails.RDAP.RegistrarName))
				facts.nameServers = appendUniqueValues(facts.nameServers, asset.DomainDetails.RDAP.NameServers...)
			}
		case models.AssetTypeIP:
			if asset.IPDetails == nil {
				continue
			}
			ptr := discovery.NormalizeDomainIdentifier(asset.IPDetails.PTR)
			root := discovery.RegistrableDomain(ptr)
			facts := ensure(root)
			if facts == nil {
				continue
			}
			facts.ptrHosts = appendUniqueValue(facts.ptrHosts, ptr)
			facts.sources = appendUniqueValues(facts.sources, splitSources(asset.Source)...)
			facts.provenanceCount += maxInt(1, len(asset.Provenance))
		}
	}

	return factsByRoot
}

type candidateAggregate struct {
	parentKey     string
	parentSeed    models.Seed
	root          string
	collectors    []string
	scenarios     []string
	support       []string
	reasons       []string
	explicitCount int
	implicitCount int
}

func aggregateDiscardedCandidates(
	evaluations []models.JudgeEvaluation,
	seeds seedIndex,
	knownRoots map[string]struct{},
	accepted map[string]map[string]struct{},
) []*candidateAggregate {
	aggregates := make(map[string]*candidateAggregate)

	for _, evaluation := range evaluations {
		parentSeed := resolveParentSeed(evaluation, seeds)
		parentKey := canonicalParentKey(parentSeed, evaluation)
		if parentKey == "" {
			continue
		}

		for _, outcome := range evaluation.Outcomes {
			if outcome.Collect {
				continue
			}

			root := discovery.RegistrableDomain(outcome.Root)
			if root == "" {
				continue
			}
			if _, exists := knownRoots[root]; exists {
				continue
			}
			if acceptedForParent, exists := accepted[parentKey]; exists {
				if _, alreadyAccepted := acceptedForParent[root]; alreadyAccepted {
					continue
				}
			}

			key := parentKey + "\x00" + root
			aggregate, exists := aggregates[key]
			if !exists {
				aggregate = &candidateAggregate{
					parentKey:  parentKey,
					parentSeed: parentSeed,
					root:       root,
				}
				aggregates[key] = aggregate
			}

			aggregate.collectors = appendUniqueValue(aggregate.collectors, evaluation.Collector)
			aggregate.scenarios = appendUniqueValue(aggregate.scenarios, evaluation.Scenario)
			aggregate.support = appendUniqueValues(aggregate.support, outcome.Support...)
			if outcome.Reason != "" {
				aggregate.reasons = appendUniqueValue(aggregate.reasons, outcome.Reason)
			}
			if outcome.Explicit {
				aggregate.explicitCount++
			} else {
				aggregate.implicitCount++
			}
		}
	}

	keys := make([]string, 0, len(aggregates))
	for key := range aggregates {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	out := make([]*candidateAggregate, 0, len(keys))
	for _, key := range keys {
		out = append(out, aggregates[key])
	}
	return out
}

type requestGroup struct {
	parentKey string
	request   ownership.Request
	byRoot    map[string]*candidateAggregate
}

func buildRequestGroups(
	aggregates []*candidateAggregate,
	factsByRoot map[string]*rootFacts,
	acceptedByParent map[string]map[string]struct{},
	pCtx *models.PipelineContext,
) []*requestGroup {
	if len(aggregates) == 0 {
		return nil
	}

	groupsByParent := make(map[string]*requestGroup)
	for _, aggregate := range aggregates {
		group := groupsByParent[aggregate.parentKey]
		if group == nil {
			group = &requestGroup{
				parentKey: aggregate.parentKey,
				request: ownership.Request{
					Scenario: reconsiderationScenario,
					Seed:     aggregate.parentSeed,
				},
				byRoot: make(map[string]*candidateAggregate),
			}
			groupsByParent[aggregate.parentKey] = group
		}

		evidence := buildCandidateEvidence(
			aggregate,
			factsByRoot[aggregate.root],
			filterAcceptedRoots(mapKeys(acceptedByParent[aggregate.parentKey]), aggregate.root),
			pCtx,
		)

		group.request.Candidates = append(group.request.Candidates, ownership.Candidate{
			Root:     aggregate.root,
			Evidence: evidence,
		})
		group.byRoot[aggregate.root] = aggregate
	}

	parentKeys := make([]string, 0, len(groupsByParent))
	for key := range groupsByParent {
		parentKeys = append(parentKeys, key)
	}
	sort.Strings(parentKeys)

	groups := make([]*requestGroup, 0, len(parentKeys))
	for _, key := range parentKeys {
		group := groupsByParent[key]
		sort.Slice(group.request.Candidates, func(i, j int) bool {
			return group.request.Candidates[i].Root < group.request.Candidates[j].Root
		})
		groups = append(groups, group)
	}

	return groups
}

func buildCandidateEvidence(
	aggregate *candidateAggregate,
	facts *rootFacts,
	acceptedRoots []string,
	pCtx *models.PipelineContext,
) []ownership.EvidenceItem {
	items := []ownership.EvidenceItem{
		{
			Kind: "prior_discard_summary",
			Summary: fmt.Sprintf(
				"Previously discarded %d time(s) in this run; explicit discards=%d, implicit non-decisions=%d.",
				aggregate.explicitCount+aggregate.implicitCount,
				aggregate.explicitCount,
				aggregate.implicitCount,
			),
		},
	}

	if len(aggregate.collectors) > 0 || len(aggregate.scenarios) > 0 {
		items = append(items, ownership.EvidenceItem{
			Kind: "prior_judge_context",
			Summary: fmt.Sprintf(
				"Prior judge context came from collectors [%s] and scenarios [%s].",
				strings.Join(limitStrings(aggregate.collectors, 5), ", "),
				strings.Join(limitStrings(aggregate.scenarios, 5), ", "),
			),
		})
	}
	if len(aggregate.support) > 0 {
		items = append(items, ownership.EvidenceItem{
			Kind:    "prior_discard_support",
			Summary: "Prior support: " + strings.Join(limitStrings(aggregate.support, 6), " | "),
		})
	}
	if len(aggregate.reasons) > 0 {
		items = append(items, ownership.EvidenceItem{
			Kind:    "prior_discard_reasons",
			Summary: "Prior discard reasons: " + strings.Join(limitStrings(aggregate.reasons, 4), " | "),
		})
	}
	if len(acceptedRoots) > 0 {
		items = append(items, ownership.EvidenceItem{
			Kind:    "accepted_run_roots",
			Summary: "Roots already accepted earlier in this run for the same seed: " + strings.Join(limitStrings(acceptedRoots, 6), ", "),
		})
	}
	if facts != nil {
		if facts.assetCount > 0 {
			items = append(items, ownership.EvidenceItem{
				Kind: "run_assets",
				Summary: fmt.Sprintf(
					"Current run already discovered %d assets under this candidate root: %s.",
					facts.assetCount,
					strings.Join(limitStrings(facts.identifiers, 6), ", "),
				),
			})
		}
		if len(facts.sources) > 0 {
			items = append(items, ownership.EvidenceItem{
				Kind:    "run_asset_sources",
				Summary: "Current run sources for this root: " + strings.Join(limitStrings(facts.sources, 6), ", "),
			})
		}
		if len(facts.ptrHosts) > 0 {
			items = append(items, ownership.EvidenceItem{
				Kind:    "run_ptr_hosts",
				Summary: "PTR hosts already observed under this root: " + strings.Join(limitStrings(facts.ptrHosts, 5), ", "),
			})
		}
		registration := summarizeRegistrationFacts(facts)
		if registration != "" {
			items = append(items, ownership.EvidenceItem{
				Kind:    "run_registration",
				Summary: registration,
			})
		}
		if facts.provenanceCount > 0 {
			items = append(items, ownership.EvidenceItem{
				Kind:    "run_provenance",
				Summary: fmt.Sprintf("Current run preserved %d provenance observations touching this root.", facts.provenanceCount),
			})
		}
	}

	items = append(items, ownership.EvidenceItem{
		Kind: "run_summary",
		Summary: fmt.Sprintf(
			"Run summary so far: %d seeds, %d assets, %d judge evaluations.",
			len(pCtx.Seeds),
			len(pCtx.Assets),
			len(pCtx.JudgeEvaluations),
		),
	})

	return items
}

func summarizeRegistrationFacts(facts *rootFacts) string {
	if facts == nil {
		return ""
	}

	parts := make([]string, 0, 3)
	if len(facts.registrantOrgs) > 0 {
		parts = append(parts, "registrant orgs="+strings.Join(limitStrings(facts.registrantOrgs, 4), ", "))
	}
	if len(facts.registrars) > 0 {
		parts = append(parts, "registrars="+strings.Join(limitStrings(facts.registrars, 4), ", "))
	}
	if len(facts.nameServers) > 0 {
		parts = append(parts, "name servers="+strings.Join(limitStrings(facts.nameServers, 5), ", "))
	}
	if len(parts) == 0 {
		return ""
	}
	return "Registration facts already present in this run: " + strings.Join(parts, " | ")
}

func resolveParentSeed(evaluation models.JudgeEvaluation, seeds seedIndex) models.Seed {
	if seed, exists := seeds.byID[strings.TrimSpace(evaluation.SeedID)]; exists {
		return seed
	}
	if key := domainsKey(evaluation.SeedDomains); key != "" {
		if seed, exists := seeds.byDomains[key]; exists {
			return seed
		}
	}
	if label := strings.ToLower(strings.TrimSpace(evaluation.SeedLabel)); label != "" {
		if seed, exists := seeds.byLabel[label]; exists {
			return seed
		}
	}

	return models.Seed{
		ID:          strings.TrimSpace(evaluation.SeedID),
		CompanyName: strings.TrimSpace(evaluation.SeedLabel),
		Domains:     append([]string(nil), evaluation.SeedDomains...),
	}
}

func canonicalParentKey(seed models.Seed, evaluation models.JudgeEvaluation) string {
	if id := strings.TrimSpace(seed.ID); id != "" {
		return "id:" + id
	}
	if key := domainsKey(seed.Domains); key != "" {
		return "domains:" + key
	}
	if key := domainsKey(evaluation.SeedDomains); key != "" {
		return "domains:" + key
	}
	if label := strings.ToLower(strings.TrimSpace(seed.CompanyName)); label != "" {
		return "label:" + label
	}
	if label := strings.ToLower(strings.TrimSpace(evaluation.SeedLabel)); label != "" {
		return "label:" + label
	}
	return ""
}

func domainsKey(domains []string) string {
	normalized := discovery.UniqueLowerStrings(domains)
	if len(normalized) == 0 {
		return ""
	}
	return strings.Join(normalized, ",")
}

func groupLabel(group *requestGroup) string {
	if group == nil {
		return ""
	}

	if label := strings.TrimSpace(group.request.Seed.CompanyName); label != "" {
		return label
	}
	if key := domainsKey(group.request.Seed.Domains); key != "" {
		return key
	}
	return strings.TrimSpace(group.parentKey)
}

func filterAcceptedRoots(roots []string, current string) []string {
	filtered := make([]string, 0, len(roots))
	for _, root := range roots {
		root = discovery.RegistrableDomain(root)
		if root == "" || root == current {
			continue
		}
		filtered = appendUniqueValue(filtered, root)
	}
	sort.Strings(filtered)
	return filtered
}

func splitSources(raw string) []string {
	parts := strings.Split(raw, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		values = append(values, part)
	}
	return values
}

func limitStrings(values []string, limit int) []string {
	if len(values) == 0 || limit <= 0 {
		return nil
	}

	limited := append([]string(nil), values...)
	sort.Strings(limited)
	if len(limited) > limit {
		limited = limited[:limit]
	}
	return limited
}

func appendUniqueValues(existing []string, incoming ...string) []string {
	for _, value := range incoming {
		existing = appendUniqueValue(existing, value)
	}
	return existing
}

func appendUniqueValue(existing []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return existing
	}
	for _, current := range existing {
		if strings.EqualFold(current, value) {
			return existing
		}
	}
	return append(existing, value)
}

func mapKeys(values map[string]struct{}) []string {
	if len(values) == 0 {
		return nil
	}

	keys := make([]string, 0, len(values))
	for value := range values {
		keys = append(keys, value)
	}
	return keys
}

func appendPipelineError(pCtx *models.PipelineContext, err error) {
	if pCtx == nil || err == nil {
		return
	}
	pCtx.Lock()
	defer pCtx.Unlock()
	pCtx.Errors = append(pCtx.Errors, err)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}
