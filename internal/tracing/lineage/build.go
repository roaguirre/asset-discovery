package lineage

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"asset-discovery/internal/models"
)

type visualizerJudgeGroupAggregate struct {
	collector   string
	seedID      string
	seedLabel   string
	seedDomains []string
	scenario    string
	outcomes    map[string]models.JudgeCandidateOutcome
}

type traceSeedGroup struct {
	Key          string
	SeedID       string
	SeedLabel    string
	Seed         models.Seed
	Contributors []TraceContributor
}

func BuildJudgeSummary(evaluations []models.JudgeEvaluation) *JudgeSummary {
	if len(evaluations) == 0 {
		return nil
	}

	groupsByKey := make(map[string]*visualizerJudgeGroupAggregate)
	evaluationCount := 0

	for _, evaluation := range evaluations {
		if len(evaluation.Outcomes) == 0 {
			continue
		}
		evaluationCount++

		key := strings.Join([]string{
			evaluation.Collector,
			evaluation.SeedID,
			evaluation.SeedLabel,
			strings.Join(evaluation.SeedDomains, ","),
			evaluation.Scenario,
		}, "\x00")

		group, exists := groupsByKey[key]
		if !exists {
			group = &visualizerJudgeGroupAggregate{
				collector:   evaluation.Collector,
				seedID:      evaluation.SeedID,
				seedLabel:   evaluation.SeedLabel,
				seedDomains: append([]string(nil), evaluation.SeedDomains...),
				scenario:    evaluation.Scenario,
				outcomes:    make(map[string]models.JudgeCandidateOutcome, len(evaluation.Outcomes)),
			}
			groupsByKey[key] = group
		}

		for _, outcome := range evaluation.Outcomes {
			if outcome.Root == "" {
				continue
			}

			if existing, exists := group.outcomes[outcome.Root]; exists {
				group.outcomes[outcome.Root] = mergeJudgeOutcome(existing, outcome)
				continue
			}
			group.outcomes[outcome.Root] = outcome
		}
	}

	if len(groupsByKey) == 0 {
		return nil
	}

	keys := make([]string, 0, len(groupsByKey))
	for key := range groupsByKey {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		left := groupsByKey[keys[i]]
		right := groupsByKey[keys[j]]
		if left.collector != right.collector {
			return left.collector < right.collector
		}
		if left.seedLabel != right.seedLabel {
			return left.seedLabel < right.seedLabel
		}
		if left.seedID != right.seedID {
			return left.seedID < right.seedID
		}
		return left.scenario < right.scenario
	})

	groups := make([]JudgeGroup, 0, len(keys))
	acceptedCount := 0
	discardedCount := 0
	for _, key := range keys {
		group := groupsByKey[key]
		roots := make([]string, 0, len(group.outcomes))
		for root := range group.outcomes {
			roots = append(roots, root)
		}
		sort.Strings(roots)

		accepted := make([]JudgeCandidate, 0)
		discarded := make([]JudgeCandidate, 0)
		for _, root := range roots {
			outcome := group.outcomes[root]
			candidate := JudgeCandidate{
				Root:       outcome.Root,
				Confidence: outcome.Confidence,
				Kind:       outcome.Kind,
				Reason:     outcome.Reason,
				Explicit:   outcome.Explicit,
				Support:    append([]string(nil), outcome.Support...),
			}
			if outcome.Collect {
				accepted = append(accepted, candidate)
				acceptedCount++
				continue
			}
			discarded = append(discarded, candidate)
			discardedCount++
		}

		groups = append(groups, JudgeGroup{
			Collector:   group.collector,
			SeedID:      group.seedID,
			SeedLabel:   group.seedLabel,
			SeedDomains: append([]string(nil), group.seedDomains...),
			Scenario:    group.scenario,
			Accepted:    accepted,
			Discarded:   discarded,
		})
	}

	return &JudgeSummary{
		EvaluationCount: evaluationCount,
		AcceptedCount:   acceptedCount,
		DiscardedCount:  discardedCount,
		Groups:          groups,
	}
}

func BuildTracePath(runID, assetID string) string {
	return "#trace/" + runID + "/" + assetID
}

func SummarizeObservationSources(observations []models.AssetObservation, fallback string) (string, string, string) {
	all := summarizeObservationSourcesByKind(observations, "")
	discovery := summarizeObservationSourcesByKind(observations, models.ObservationKindDiscovery)
	enrichment := summarizeObservationSourcesByKind(observations, models.ObservationKindEnrichment)
	if all == "" {
		all = strings.TrimSpace(fallback)
	}
	if discovery == "" {
		discovery = strings.TrimSpace(fallback)
	}
	return all, discovery, enrichment
}

func BuildTrace(
	asset models.Asset,
	domainKind string,
	registrableDomain string,
	contributors []TraceContributor,
	observations []models.AssetObservation,
	relations []models.AssetRelation,
	enumByID map[string]models.Enumeration,
	seedByID map[string]models.Seed,
) Trace {
	allSources, discoverySources, enrichmentSources := SummarizeObservationSources(observations, asset.Source)
	trace := Trace{
		AssetID:           asset.ID,
		Identifier:        asset.Identifier,
		AssetType:         string(asset.Type),
		Source:            allSources,
		DiscoveredBy:      discoverySources,
		EnrichedBy:        enrichmentSources,
		EnumerationID:     SummarizeContributorValues(contributors, func(item TraceContributor) string { return item.EnumerationID }),
		SeedID:            SummarizeContributorValues(contributors, func(item TraceContributor) string { return item.SeedID }),
		DomainKind:        domainKind,
		RegistrableDomain: registrableDomain,
		ResolutionStatus:  string(models.DomainResolutionStatusForAsset(asset)),
		Contributors:      contributors,
	}

	identityItems := []string{
		"Asset ID: " + asset.ID,
		"Asset type: " + string(asset.Type),
		"Contributors: " + allSources,
	}
	if discoverySources != "" {
		identityItems = append(identityItems, "Discovered by: "+discoverySources)
	}
	if enrichmentSources != "" {
		identityItems = append(identityItems, "Enriched by: "+enrichmentSources)
	}
	if len(contributors) > 1 {
		identityItems = append(identityItems, fmt.Sprintf("Merged contributors: %d", len(contributors)))
	}
	if !asset.DiscoveryDate.IsZero() {
		identityItems = append(identityItems, "Discovered at: "+FormatDateTime(asset.DiscoveryDate))
	}
	if domainKind != "" {
		identityItems = append(identityItems, "Domain kind: "+formatTraceLabel(domainKind))
	}
	if registrableDomain != "" {
		identityItems = append(identityItems, "Registrable domain: "+registrableDomain)
	}
	if trace.ResolutionStatus != "" {
		identityItems = append(identityItems, "DNS resolution: "+formatTraceLabel(trace.ResolutionStatus))
	}
	trace.Sections = appendTraceSection(trace.Sections, "Result", identityItems)

	trace.Sections = appendTraceSection(trace.Sections, "Contributor Provenance", buildContributorTraceItems(contributors))

	if len(contributors) == 1 {
		contributor := contributors[0]
		enum := enumByID[contributor.EnumerationID]
		seed := seedByID[contributor.SeedID]

		seedItems := make([]string, 0, 5)
		if seed.ID != "" {
			seedItems = append(seedItems, "Seed ID: "+seed.ID)
		}
		if seed.CompanyName != "" {
			seedItems = append(seedItems, "Company: "+seed.CompanyName)
		}
		if len(seed.Domains) > 0 {
			seedItems = append(seedItems, "Seed domains: "+strings.Join(seed.Domains, ", "))
		}
		if len(seed.Tags) > 0 {
			seedItems = append(seedItems, "Seed tags: "+strings.Join(seed.Tags, ", "))
		}
		if evidence := formatSeedEvidence(seed.Evidence); len(evidence) > 0 {
			seedItems = append(seedItems, evidence...)
		}
		trace.Sections = appendTraceSection(trace.Sections, "Seed Context", seedItems)

		enumItems := make([]string, 0, 5)
		if enum.ID != "" {
			enumItems = append(enumItems, "Enumeration ID: "+enum.ID)
		}
		if enum.Status != "" {
			enumItems = append(enumItems, "Status: "+enum.Status)
		}
		if !enum.CreatedAt.IsZero() {
			enumItems = append(enumItems, "Created at: "+FormatDateTime(enum.CreatedAt))
		}
		if !enum.StartedAt.IsZero() {
			enumItems = append(enumItems, "Started at: "+FormatDateTime(enum.StartedAt))
		}
		if !enum.EndedAt.IsZero() {
			enumItems = append(enumItems, "Ended at: "+FormatDateTime(enum.EndedAt))
		}
		trace.Sections = appendTraceSection(trace.Sections, "Enumeration", enumItems)
	}

	if len(contributors) > 1 {
		trace.Sections = appendTraceSection(trace.Sections, "Seed Context", buildMergedSeedTraceItems(contributors, seedByID))
	}

	trace.Sections = appendTraceSection(trace.Sections, "Domain Evidence", buildDomainTraceItems(asset))
	trace.Sections = appendTraceSection(trace.Sections, "Network Evidence", buildIPTraceItems(asset))
	trace.Sections = appendTraceSection(trace.Sections, "Enrichment", buildEnrichmentTraceItems(asset.EnrichmentData))
	trace.RootNodeID, trace.Nodes = buildTraceNodes(asset, allSources, trace.Sections, contributors, observations, relations, enumByID, seedByID)

	return trace
}

func BuildTraceContributors(asset models.Asset, enumByID map[string]models.Enumeration, seedByID map[string]models.Seed) []TraceContributor {
	provenance := append([]models.AssetProvenance(nil), asset.Provenance...)
	if len(provenance) == 0 {
		provenance = append(provenance, models.AssetProvenance{
			AssetID:       asset.ID,
			EnumerationID: asset.EnumerationID,
			Source:        asset.Source,
			DiscoveryDate: asset.DiscoveryDate,
		})
	}

	contributors := make([]TraceContributor, 0, len(provenance))
	for _, item := range provenance {
		enum := enumByID[item.EnumerationID]
		seed := seedByID[enum.SeedID]

		seedLabel := seed.CompanyName
		if seedLabel == "" {
			seedLabel = enum.SeedID
		}

		discoveryDate := item.DiscoveryDate
		if discoveryDate.IsZero() {
			discoveryDate = asset.DiscoveryDate
		}

		contributors = append(contributors, TraceContributor{
			AssetID:       item.AssetID,
			EnumerationID: item.EnumerationID,
			SeedID:        enum.SeedID,
			SeedLabel:     seedLabel,
			Source:        item.Source,
			DiscoveryDate: discoveryDate,
		})
	}

	return contributors
}

func buildTraceNodes(
	asset models.Asset,
	allSources string,
	sections []TraceSection,
	contributors []TraceContributor,
	observations []models.AssetObservation,
	relations []models.AssetRelation,
	enumByID map[string]models.Enumeration,
	seedByID map[string]models.Seed,
) (string, []TraceNode) {
	rootID := "asset:" + asset.ID
	rootDetails := append([]TraceSection(nil), sections...)
	rootDetails = append(rootDetails, TraceSection{
		Title: "Ownership",
		Items: compactTraceValues(
			"Ownership state: "+string(asset.OwnershipState),
			"Inclusion reason: "+asset.InclusionReason,
		),
	})
	nodes := []TraceNode{
		{
			ID:            rootID,
			Kind:          "asset",
			Label:         asset.Identifier,
			Subtitle:      string(asset.Type),
			Badges:        compactTraceValues(string(asset.OwnershipState), allSources),
			LinkedAssetID: asset.ID,
			Details:       rootDetails,
		},
	}

	if len(observations) > 0 {
		groupID := rootID + ":observations"
		nodes = append(nodes, TraceNode{
			ID:            groupID,
			ParentID:      rootID,
			Kind:          "group",
			Label:         "Observations",
			Subtitle:      fmt.Sprintf("%d supporting observations", len(observations)),
			LinkedAssetID: asset.ID,
			Details:       buildObservationGroupTraceDetails(observations),
		})
		sort.SliceStable(observations, func(i, j int) bool {
			if observations[i].DiscoveryDate.Equal(observations[j].DiscoveryDate) {
				return observations[i].ID < observations[j].ID
			}
			if observations[i].DiscoveryDate.IsZero() {
				return false
			}
			if observations[j].DiscoveryDate.IsZero() {
				return true
			}
			return observations[i].DiscoveryDate.Before(observations[j].DiscoveryDate)
		})
		for _, observation := range observations {
			nodes = append(nodes, TraceNode{
				ID:                  "obs:" + observation.ID,
				ParentID:            groupID,
				Kind:                "observation",
				Label:               observation.Source,
				Subtitle:            observation.Identifier,
				Badges:              compactTraceValues(formatTraceLabel(string(observation.Kind)), FormatDateTime(observation.DiscoveryDate), string(observation.OwnershipState)),
				LinkedAssetID:       observation.AssetID,
				LinkedObservationID: observation.ID,
				Details:             buildObservationTraceDetails(observation, enumByID, seedByID),
			})
		}
	}

	if len(contributors) > 0 {
		seedGroups := groupTraceContributorsBySeed(contributors, seedByID)
		groupID := rootID + ":seeds"
		nodes = append(nodes, TraceNode{
			ID:            groupID,
			ParentID:      rootID,
			Kind:          "group",
			Label:         "Seed Context",
			Subtitle:      fmt.Sprintf("%d contributing seeds", len(seedGroups)),
			LinkedAssetID: asset.ID,
			Details:       buildSeedGroupTraceDetails(seedGroups),
		})
		for _, seedGroup := range seedGroups {
			seedID := seedGroup.SeedID
			if seedID == "" {
				seedID = seedGroup.Key
			}
			nodes = append(nodes, TraceNode{
				ID:            "seed:" + seedID,
				ParentID:      groupID,
				Kind:          "seed",
				Label:         seedGroup.SeedLabel,
				Subtitle:      seedGroup.SeedID,
				Badges:        compactTraceValues(strings.Join(seedGroup.Seed.Domains, ", "), fmt.Sprintf("%d contributors", len(seedGroup.Contributors))),
				LinkedAssetID: asset.ID,
				Details:       buildSeedTraceDetails(seedGroup),
			})
			for index, contributor := range seedGroup.Contributors {
				contributorLabel := contributor.Source
				if contributorLabel == "" {
					contributorLabel = "Contributor"
				}
				nodes = append(nodes, TraceNode{
					ID:            fmt.Sprintf("seed:%s:contributor:%d", seedID, index),
					ParentID:      "seed:" + seedID,
					Kind:          "contributor",
					Label:         contributorLabel,
					Subtitle:      contributor.AssetID,
					Badges:        compactTraceValues("Enum "+contributor.EnumerationID, FormatDateTime(contributor.DiscoveryDate)),
					LinkedAssetID: contributor.AssetID,
					Details:       buildContributorTraceDetails(contributor),
				})
			}
		}
	}

	if len(relations) > 0 {
		groupID := rootID + ":relations"
		nodes = append(nodes, TraceNode{
			ID:            groupID,
			ParentID:      rootID,
			Kind:          "group",
			Label:         "Relations",
			Subtitle:      fmt.Sprintf("%d linked discovery edges", len(relations)),
			LinkedAssetID: asset.ID,
			Details:       buildRelationGroupTraceDetails(asset.ID, relations),
		})
		sort.SliceStable(relations, func(i, j int) bool {
			if relations[i].DiscoveryDate.Equal(relations[j].DiscoveryDate) {
				return relations[i].ID < relations[j].ID
			}
			if relations[i].DiscoveryDate.IsZero() {
				return false
			}
			if relations[j].DiscoveryDate.IsZero() {
				return true
			}
			return relations[i].DiscoveryDate.Before(relations[j].DiscoveryDate)
		})
		for _, relation := range relations {
			label := relation.Label
			if label == "" {
				label = relation.Kind
			}
			peer := relation.ToIdentifier
			if relation.FromAssetID == asset.ID {
				peer = relation.ToIdentifier
			} else if relation.ToAssetID == asset.ID {
				peer = relation.FromIdentifier
			}
			nodes = append(nodes, TraceNode{
				ID:               "rel:" + relation.ID,
				ParentID:         groupID,
				Kind:             "relation",
				Label:            label,
				Subtitle:         peer,
				Badges:           compactTraceValues(relation.Kind, relation.Source, FormatDateTime(relation.DiscoveryDate)),
				LinkedAssetID:    asset.ID,
				LinkedRelationID: relation.ID,
				Details: []TraceSection{
					{Title: "Relation", Items: compactTraceValues("Kind: "+relation.Kind, "Source: "+relation.Source, "Reason: "+relation.Reason)},
					{Title: "Endpoints", Items: compactTraceValues("From: "+relation.FromIdentifier, "To: "+relation.ToIdentifier, "Enumeration: "+relation.EnumerationID)},
				},
			})
		}
	}

	if len(asset.EnrichmentStates) > 0 || len(asset.EnrichmentData) > 0 {
		groupID := rootID + ":enrichment"
		nodes = append(nodes, TraceNode{
			ID:            groupID,
			ParentID:      rootID,
			Kind:          "group",
			Label:         "Enrichment",
			Subtitle:      "Runtime cache and enrichment results",
			LinkedAssetID: asset.ID,
			Details:       buildEnrichmentGroupTraceDetails(asset),
		})
		stateKeys := make([]string, 0, len(asset.EnrichmentStates))
		for key := range asset.EnrichmentStates {
			stateKeys = append(stateKeys, key)
		}
		sort.Strings(stateKeys)
		for _, key := range stateKeys {
			state := asset.EnrichmentStates[key]
			nodes = append(nodes, TraceNode{
				ID:            "enrich:" + asset.ID + ":" + key,
				ParentID:      groupID,
				Kind:          "enrichment",
				Label:         key,
				Subtitle:      state.Status,
				Badges:        compactTraceValues(fmt.Sprintf("cached=%t", state.Cached), FormatDateTime(state.UpdatedAt)),
				LinkedAssetID: asset.ID,
				Details: []TraceSection{
					{Title: "Enrichment State", Items: compactTraceValues("Stage: "+key, "Status: "+state.Status, fmt.Sprintf("Cached: %t", state.Cached), "Error: "+state.Error)},
				},
			})
		}
	}

	return rootID, nodes
}

func SummarizeContributorValues(contributors []TraceContributor, value func(TraceContributor) string) string {
	values := uniqueTraceContributorValues(contributors, value)
	return strings.Join(values, ", ")
}

func SummarizeTraceStatus(asset models.Asset, contributors []TraceContributor, enumByID map[string]models.Enumeration) string {
	statuses := make([]string, 0, len(contributors))
	seen := make(map[string]struct{}, len(contributors))
	for _, contributor := range contributors {
		status := strings.TrimSpace(enumByID[contributor.EnumerationID].Status)
		if status == "" {
			continue
		}
		if _, exists := seen[status]; exists {
			continue
		}
		seen[status] = struct{}{}
		statuses = append(statuses, status)
	}
	if len(statuses) == 0 {
		return enumByID[asset.EnumerationID].Status
	}
	return strings.Join(statuses, ", ")
}

func FormatDateTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Format("2006-01-02 15:04:05")
}

func mergeJudgeOutcome(existing, next models.JudgeCandidateOutcome) models.JudgeCandidateOutcome {
	if next.Collect {
		existing.Collect = true
	}
	if next.Confidence > existing.Confidence {
		existing.Confidence = next.Confidence
	}
	if next.Kind != "" && (existing.Kind == "" || next.Collect) {
		existing.Kind = next.Kind
	}
	if next.Reason != "" && (existing.Reason == "" || next.Collect) {
		existing.Reason = next.Reason
	}
	existing.Explicit = existing.Explicit || next.Explicit
	existing.Support = mergeJudgeSupport(existing.Support, next.Support)
	return existing
}

func mergeJudgeSupport(left, right []string) []string {
	if len(left) == 0 && len(right) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(left)+len(right))
	out := make([]string, 0, len(left)+len(right))
	for _, value := range append(append([]string(nil), left...), right...) {
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

func buildContributorTraceItems(contributors []TraceContributor) []string {
	items := make([]string, 0, len(contributors))
	for _, contributor := range contributors {
		parts := make([]string, 0, 6)
		if contributor.AssetID != "" {
			parts = append(parts, "asset "+contributor.AssetID)
		}
		if contributor.Source != "" {
			parts = append(parts, "source "+contributor.Source)
		}
		if contributor.EnumerationID != "" {
			parts = append(parts, "enumeration "+contributor.EnumerationID)
		}
		if contributor.SeedID != "" {
			seed := contributor.SeedID
			if contributor.SeedLabel != "" && contributor.SeedLabel != contributor.SeedID {
				seed += " (" + contributor.SeedLabel + ")"
			}
			parts = append(parts, "seed "+seed)
		}
		if !contributor.DiscoveryDate.IsZero() {
			parts = append(parts, "discovered "+FormatDateTime(contributor.DiscoveryDate))
		}
		if len(parts) == 0 {
			continue
		}
		items = append(items, strings.Join(parts, " | "))
	}
	return items
}

func buildMergedSeedTraceItems(contributors []TraceContributor, seedByID map[string]models.Seed) []string {
	items := make([]string, 0, len(contributors)*3)
	seen := make(map[string]struct{}, len(contributors))

	for _, contributor := range contributors {
		seedID := strings.TrimSpace(contributor.SeedID)
		if seedID == "" {
			continue
		}
		if _, exists := seen[seedID]; exists {
			continue
		}
		seen[seedID] = struct{}{}

		seed := seedByID[seedID]
		label := seedID
		if seed.CompanyName != "" {
			label += " (" + seed.CompanyName + ")"
		}

		items = append(items, "Seed "+label)
		if len(seed.Domains) > 0 {
			items = append(items, "Seed "+label+" | domains "+strings.Join(seed.Domains, ", "))
		}
		if len(seed.Tags) > 0 {
			items = append(items, "Seed "+label+" | tags "+strings.Join(seed.Tags, ", "))
		}
		if evidence := formatSeedEvidence(seed.Evidence); len(evidence) > 0 {
			for _, item := range evidence {
				items = append(items, "Seed "+label+" | "+item)
			}
		}
	}

	return items
}

func buildObservationGroupTraceDetails(observations []models.AssetObservation) []TraceSection {
	if len(observations) == 0 {
		return nil
	}

	discoveryCount := 0
	enrichmentCount := 0
	firstSeen := time.Time{}
	lastSeen := time.Time{}
	enumerations := make([]string, 0, len(observations))
	seenEnumerations := make(map[string]struct{}, len(observations))

	for _, observation := range observations {
		switch observation.Kind {
		case models.ObservationKindEnrichment:
			enrichmentCount++
		default:
			discoveryCount++
		}

		if !observation.DiscoveryDate.IsZero() {
			if firstSeen.IsZero() || observation.DiscoveryDate.Before(firstSeen) {
				firstSeen = observation.DiscoveryDate
			}
			if lastSeen.IsZero() || observation.DiscoveryDate.After(lastSeen) {
				lastSeen = observation.DiscoveryDate
			}
		}

		if observation.EnumerationID == "" {
			continue
		}
		if _, exists := seenEnumerations[observation.EnumerationID]; exists {
			continue
		}
		seenEnumerations[observation.EnumerationID] = struct{}{}
		enumerations = append(enumerations, observation.EnumerationID)
	}

	sort.Strings(enumerations)
	return []TraceSection{
		{
			Title: "Observation Summary",
			Items: compactTraceValues(
				fmt.Sprintf("Total observations: %d", len(observations)),
				fmt.Sprintf("Discovery observations: %d", discoveryCount),
				fmt.Sprintf("Enrichment observations: %d", enrichmentCount),
				"Sources: "+summarizeObservationSourcesByKind(observations, ""),
				"Enumerations: "+strings.Join(enumerations, ", "),
				"First seen: "+FormatDateTime(firstSeen),
				"Last seen: "+FormatDateTime(lastSeen),
			),
		},
	}
}

func groupTraceContributorsBySeed(contributors []TraceContributor, seedByID map[string]models.Seed) []traceSeedGroup {
	order := make([]string, 0, len(contributors))
	groups := make(map[string]*traceSeedGroup, len(contributors))

	for _, contributor := range contributors {
		key := strings.TrimSpace(contributor.SeedID)
		if key == "" {
			key = strings.TrimSpace(contributor.SeedLabel)
		}
		if key == "" {
			key = strings.TrimSpace(contributor.AssetID)
		}
		if key == "" {
			key = fmt.Sprintf("unknown-%d", len(order))
		}

		group, exists := groups[key]
		if !exists {
			order = append(order, key)
			group = &traceSeedGroup{
				Key:       key,
				SeedID:    contributor.SeedID,
				SeedLabel: contributor.SeedLabel,
				Seed:      seedByID[contributor.SeedID],
			}
			if group.SeedLabel == "" {
				switch {
				case group.Seed.CompanyName != "":
					group.SeedLabel = group.Seed.CompanyName
				case contributor.SeedID != "":
					group.SeedLabel = contributor.SeedID
				default:
					group.SeedLabel = "Unknown Seed"
				}
			}
			groups[key] = group
		}

		if group.SeedID == "" {
			group.SeedID = contributor.SeedID
		}
		if group.Seed.ID == "" && contributor.SeedID != "" {
			group.Seed = seedByID[contributor.SeedID]
		}
		if group.SeedLabel == "" && contributor.SeedLabel != "" {
			group.SeedLabel = contributor.SeedLabel
		}
		group.Contributors = append(group.Contributors, contributor)
	}

	out := make([]traceSeedGroup, 0, len(order))
	for _, key := range order {
		out = append(out, *groups[key])
	}
	return out
}

func buildSeedGroupTraceDetails(seedGroups []traceSeedGroup) []TraceSection {
	if len(seedGroups) == 0 {
		return nil
	}

	contributorCount := 0
	enumerations := make([]string, 0, len(seedGroups))
	seenEnumerations := make(map[string]struct{}, len(seedGroups))
	sources := make([]string, 0, len(seedGroups))
	seenSources := make(map[string]struct{}, len(seedGroups))

	for _, group := range seedGroups {
		contributorCount += len(group.Contributors)
		for _, contributor := range group.Contributors {
			if contributor.EnumerationID != "" {
				if _, exists := seenEnumerations[contributor.EnumerationID]; !exists {
					seenEnumerations[contributor.EnumerationID] = struct{}{}
					enumerations = append(enumerations, contributor.EnumerationID)
				}
			}
			if contributor.Source != "" {
				if _, exists := seenSources[contributor.Source]; !exists {
					seenSources[contributor.Source] = struct{}{}
					sources = append(sources, contributor.Source)
				}
			}
		}
	}

	sort.Strings(enumerations)
	sort.Strings(sources)
	return []TraceSection{
		{
			Title: "Seed Summary",
			Items: compactTraceValues(
				fmt.Sprintf("Unique seeds: %d", len(seedGroups)),
				fmt.Sprintf("Contributors: %d", contributorCount),
				fmt.Sprintf("Enumerations represented: %d", len(enumerations)),
				"Enumerations: "+strings.Join(enumerations, ", "),
				"Sources: "+strings.Join(sources, ", "),
			),
		},
	}
}

func buildSeedTraceDetails(group traceSeedGroup) []TraceSection {
	sections := []TraceSection{
		{
			Title: "Seed",
			Items: compactTraceValues(
				"Seed ID: "+group.SeedID,
				"Company: "+group.Seed.CompanyName,
				"Domains: "+strings.Join(group.Seed.Domains, ", "),
				"Tags: "+strings.Join(group.Seed.Tags, ", "),
			),
		},
	}

	if evidence := formatSeedEvidence(group.Seed.Evidence); len(evidence) > 0 {
		sections = append(sections, TraceSection{
			Title: "Evidence",
			Items: evidence,
		})
	}

	enumerations := uniqueTraceContributorValues(group.Contributors, func(item TraceContributor) string { return item.EnumerationID })
	sort.Strings(enumerations)
	sources := uniqueTraceContributorValues(group.Contributors, func(item TraceContributor) string { return item.Source })
	sort.Strings(sources)
	firstSeen, lastSeen := traceContributorTimeBounds(group.Contributors)

	sections = appendTraceSection(sections, "Contribution Summary", compactTraceValues(
		fmt.Sprintf("Contributors: %d", len(group.Contributors)),
		fmt.Sprintf("Enumerations represented: %d", len(enumerations)),
		"Enumerations: "+strings.Join(enumerations, ", "),
		"Sources: "+strings.Join(sources, ", "),
		"First contributed at: "+FormatDateTime(firstSeen),
		"Last contributed at: "+FormatDateTime(lastSeen),
	))

	return sections
}

func buildContributorTraceDetails(contributor TraceContributor) []TraceSection {
	return []TraceSection{
		{
			Title: "Contributor",
			Items: compactTraceValues(
				"Source: "+contributor.Source,
				"Enumeration ID: "+contributor.EnumerationID,
				"Originating asset ID: "+contributor.AssetID,
				"Discovered at: "+FormatDateTime(contributor.DiscoveryDate),
			),
		},
	}
}

func buildRelationGroupTraceDetails(assetID string, relations []models.AssetRelation) []TraceSection {
	if len(relations) == 0 {
		return nil
	}

	kinds := make([]string, 0, len(relations))
	seenKinds := make(map[string]struct{}, len(relations))
	peers := make([]string, 0, len(relations))
	seenPeers := make(map[string]struct{}, len(relations))
	sources := make([]string, 0, len(relations))
	seenSources := make(map[string]struct{}, len(relations))

	for _, relation := range relations {
		if relation.Kind != "" {
			if _, exists := seenKinds[relation.Kind]; !exists {
				seenKinds[relation.Kind] = struct{}{}
				kinds = append(kinds, relation.Kind)
			}
		}
		peer := relation.ToIdentifier
		if relation.ToAssetID == assetID {
			peer = relation.FromIdentifier
		}
		if peer != "" {
			if _, exists := seenPeers[peer]; !exists {
				seenPeers[peer] = struct{}{}
				peers = append(peers, peer)
			}
		}
		if relation.Source != "" {
			if _, exists := seenSources[relation.Source]; !exists {
				seenSources[relation.Source] = struct{}{}
				sources = append(sources, relation.Source)
			}
		}
	}

	sort.Strings(kinds)
	sort.Strings(peers)
	sort.Strings(sources)
	return []TraceSection{
		{
			Title: "Relation Summary",
			Items: compactTraceValues(
				fmt.Sprintf("Total relations: %d", len(relations)),
				fmt.Sprintf("Peer assets: %d", len(peers)),
				"Peers: "+strings.Join(peers, ", "),
				"Kinds: "+strings.Join(kinds, ", "),
				"Sources: "+strings.Join(sources, ", "),
			),
		},
	}
}

func buildEnrichmentGroupTraceDetails(asset models.Asset) []TraceSection {
	stateKeys := make([]string, 0, len(asset.EnrichmentStates))
	statusCounts := make(map[string]int, len(asset.EnrichmentStates))
	lastUpdated := time.Time{}

	for key, state := range asset.EnrichmentStates {
		stateKeys = append(stateKeys, key)
		status := strings.TrimSpace(strings.ToLower(state.Status))
		if status != "" {
			statusCounts[status]++
		}
		if !state.UpdatedAt.IsZero() && (lastUpdated.IsZero() || state.UpdatedAt.After(lastUpdated)) {
			lastUpdated = state.UpdatedAt
		}
	}

	sort.Strings(stateKeys)
	statuses := make([]string, 0, len(statusCounts))
	for status, count := range statusCounts {
		statuses = append(statuses, fmt.Sprintf("%s (%d)", status, count))
	}
	sort.Strings(statuses)

	dataKeys := make([]string, 0, len(asset.EnrichmentData))
	for key := range asset.EnrichmentData {
		dataKeys = append(dataKeys, key)
	}
	sort.Strings(dataKeys)

	return []TraceSection{
		{
			Title: "Enrichment Summary",
			Items: compactTraceValues(
				fmt.Sprintf("Stages: %d", len(stateKeys)),
				"Stage names: "+strings.Join(stateKeys, ", "),
				"Statuses: "+strings.Join(statuses, ", "),
				"Exported fields: "+strings.Join(dataKeys, ", "),
				"Latest update: "+FormatDateTime(lastUpdated),
			),
		},
	}
}

func traceContributorTimeBounds(contributors []TraceContributor) (time.Time, time.Time) {
	firstSeen := time.Time{}
	lastSeen := time.Time{}
	for _, contributor := range contributors {
		if contributor.DiscoveryDate.IsZero() {
			continue
		}
		if firstSeen.IsZero() || contributor.DiscoveryDate.Before(firstSeen) {
			firstSeen = contributor.DiscoveryDate
		}
		if lastSeen.IsZero() || contributor.DiscoveryDate.After(lastSeen) {
			lastSeen = contributor.DiscoveryDate
		}
	}
	return firstSeen, lastSeen
}

func buildObservationTraceDetails(observation models.AssetObservation, enumByID map[string]models.Enumeration, seedByID map[string]models.Seed) []TraceSection {
	sections := []TraceSection{
		{
			Title: "Observation",
			Items: compactTraceValues(
				"Observation ID: "+observation.ID,
				"Kind: "+string(observation.Kind),
				"Type: "+string(observation.Type),
				"Identifier: "+observation.Identifier,
				"Source: "+observation.Source,
				"Discovered at: "+FormatDateTime(observation.DiscoveryDate),
				"Ownership state: "+string(observation.OwnershipState),
				"Inclusion reason: "+observation.InclusionReason,
			),
		},
	}

	if enum := enumByID[observation.EnumerationID]; enum.ID != "" {
		seed := seedByID[enum.SeedID]
		sections = append(sections, TraceSection{
			Title: "Enumeration",
			Items: compactTraceValues(
				"Enumeration ID: "+enum.ID,
				"Status: "+enum.Status,
				"Created at: "+FormatDateTime(enum.CreatedAt),
				"Started at: "+FormatDateTime(enum.StartedAt),
				"Ended at: "+FormatDateTime(enum.EndedAt),
				"Seed ID: "+enum.SeedID,
			),
		})
		seedItems := compactTraceValues(
			"Seed ID: "+seed.ID,
			"Company: "+seed.CompanyName,
			"Seed domains: "+strings.Join(seed.Domains, ", "),
			"Seed tags: "+strings.Join(seed.Tags, ", "),
		)
		seedItems = append(seedItems, formatSeedEvidence(seed.Evidence)...)
		sections = appendTraceSection(sections, "Seed Context", seedItems)
	}

	if observation.DomainDetails != nil {
		domainAsset := models.Asset{
			Type:             models.AssetTypeDomain,
			Identifier:       observation.Identifier,
			DomainDetails:    observation.DomainDetails,
			EnrichmentStates: observation.EnrichmentStates,
		}
		sections = appendTraceSection(sections, "Domain Evidence", buildDomainTraceItems(domainAsset))
	}
	if observation.IPDetails != nil {
		ipAsset := models.Asset{
			Type:       models.AssetTypeIP,
			Identifier: observation.Identifier,
			IPDetails:  observation.IPDetails,
		}
		sections = appendTraceSection(sections, "Network Evidence", buildIPTraceItems(ipAsset))
	}
	if len(observation.EnrichmentData) > 0 {
		sections = appendTraceSection(sections, "Enrichment", buildEnrichmentTraceItems(observation.EnrichmentData))
	}
	if len(observation.EnrichmentStates) > 0 {
		stateKeys := make([]string, 0, len(observation.EnrichmentStates))
		for key := range observation.EnrichmentStates {
			stateKeys = append(stateKeys, key)
		}
		sort.Strings(stateKeys)
		stateItems := make([]string, 0, len(stateKeys))
		for _, key := range stateKeys {
			state := observation.EnrichmentStates[key]
			stateItems = append(stateItems, compactTraceValues(
				"Stage: "+key,
				"Status: "+state.Status,
				fmt.Sprintf("Cached: %t", state.Cached),
				"Updated at: "+FormatDateTime(state.UpdatedAt),
				"Error: "+state.Error,
			)...)
		}
		sections = appendTraceSection(sections, "Enrichment State", stateItems)
	}

	return sections
}

func uniqueTraceContributorValues(contributors []TraceContributor, value func(TraceContributor) string) []string {
	out := make([]string, 0, len(contributors))
	seen := make(map[string]struct{}, len(contributors))
	for _, contributor := range contributors {
		item := strings.TrimSpace(value(contributor))
		if item == "" {
			continue
		}
		if _, exists := seen[item]; exists {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func appendTraceSection(sections []TraceSection, title string, items []string) []TraceSection {
	if len(items) == 0 {
		return sections
	}

	clean := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		clean = append(clean, item)
	}
	if len(clean) == 0 {
		return sections
	}

	return append(sections, TraceSection{
		Title: title,
		Items: clean,
	})
}

func compactTraceValues(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || strings.HasSuffix(value, ":") {
			continue
		}
		out = append(out, value)
	}
	return out
}

func formatSeedEvidence(evidence []models.SeedEvidence) []string {
	items := make([]string, 0, len(evidence))
	for _, item := range evidence {
		parts := make([]string, 0, 4)
		if item.Source != "" {
			parts = append(parts, item.Source)
		}
		if item.Kind != "" {
			parts = append(parts, item.Kind)
		}
		if item.Value != "" {
			parts = append(parts, item.Value)
		}
		if item.Confidence > 0 {
			parts = append(parts, fmt.Sprintf("confidence %.2f", item.Confidence))
		}
		if item.Reasoned {
			parts = append(parts, "reasoned")
		}
		if len(parts) == 0 {
			continue
		}
		items = append(items, "Evidence: "+strings.Join(parts, " | "))
	}
	return items
}

func buildDomainTraceItems(asset models.Asset) []string {
	if asset.DomainDetails == nil {
		if resolution := models.DomainResolutionStatusForAsset(asset); resolution != "" {
			return []string{"DNS resolution: " + formatTraceLabel(string(resolution))}
		}
		return nil
	}

	items := make([]string, 0, 8)
	if resolution := models.DomainResolutionStatusForAsset(asset); resolution != "" {
		items = append(items, "DNS resolution: "+formatTraceLabel(string(resolution)))
	}
	if len(asset.DomainDetails.Records) > 0 {
		recordParts := make([]string, 0, len(asset.DomainDetails.Records))
		for _, record := range asset.DomainDetails.Records {
			recordParts = append(recordParts, record.Type+" "+record.Value)
		}
		items = append(items, "DNS records: "+strings.Join(recordParts, ", "))
	}
	if asset.DomainDetails.IsCatchAll {
		items = append(items, "Catch-all detection: true")
	}
	if asset.DomainDetails.RDAP != nil {
		rdap := asset.DomainDetails.RDAP
		if rdap.RegistrarName != "" {
			items = append(items, "Registrar: "+rdap.RegistrarName)
		}
		if rdap.RegistrarURL != "" {
			items = append(items, "Registrar URL: "+rdap.RegistrarURL)
		}
		if rdap.RegistrantName != "" {
			items = append(items, "Registrant name: "+rdap.RegistrantName)
		}
		if rdap.RegistrantOrg != "" {
			items = append(items, "Registrant org: "+rdap.RegistrantOrg)
		}
		if rdap.RegistrantEmail != "" {
			items = append(items, "Registrant email: "+rdap.RegistrantEmail)
		}
		if len(rdap.NameServers) > 0 {
			items = append(items, "Nameservers: "+strings.Join(rdap.NameServers, ", "))
		}
		if !rdap.CreationDate.IsZero() {
			items = append(items, "Registration created: "+rdap.CreationDate.Format("2006-01-02"))
		}
		if !rdap.UpdatedDate.IsZero() {
			items = append(items, "Registration updated: "+rdap.UpdatedDate.Format("2006-01-02"))
		}
		if !rdap.ExpirationDate.IsZero() {
			items = append(items, "Registration expires: "+rdap.ExpirationDate.Format("2006-01-02"))
		}
		if len(rdap.Statuses) > 0 {
			items = append(items, "Registration status: "+strings.Join(rdap.Statuses, ", "))
		}
	}

	return items
}

func buildIPTraceItems(asset models.Asset) []string {
	if asset.IPDetails == nil {
		return nil
	}

	items := make([]string, 0, 4)
	if asset.IPDetails.ASN != 0 {
		items = append(items, fmt.Sprintf("ASN: %d", asset.IPDetails.ASN))
	}
	if asset.IPDetails.Organization != "" {
		items = append(items, "Organization: "+asset.IPDetails.Organization)
	}
	if asset.IPDetails.PTR != "" {
		items = append(items, "PTR: "+asset.IPDetails.PTR)
	}
	return items
}

func buildEnrichmentTraceItems(enrichment map[string]interface{}) []string {
	if len(enrichment) == 0 {
		return nil
	}

	keys := make([]string, 0, len(enrichment))
	for key := range enrichment {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	items := make([]string, 0, len(keys))
	for _, key := range keys {
		items = append(items, fmt.Sprintf("%s: %s", key, formatEnrichmentValue(enrichment[key])))
	}
	return items
}

func formatEnrichmentValue(value interface{}) string {
	switch typed := value.(type) {
	case string:
		if typed == "" {
			return "-"
		}
		return typed
	case []string:
		return strings.Join(typed, ", ")
	default:
		data, err := json.Marshal(typed)
		if err != nil {
			return fmt.Sprint(typed)
		}
		return string(data)
	}
}

func formatTraceLabel(value string) string {
	return strings.ReplaceAll(value, "_", " ")
}

func summarizeObservationSourcesByKind(observations []models.AssetObservation, kind models.ObservationKind) string {
	values := make([]string, 0, len(observations))
	seen := make(map[string]struct{}, len(observations))
	for _, observation := range observations {
		if kind != "" && observation.Kind != kind {
			continue
		}
		source := strings.TrimSpace(strings.ToLower(observation.Source))
		if source == "" {
			continue
		}
		if _, exists := seen[source]; exists {
			continue
		}
		seen[source] = struct{}{}
		values = append(values, source)
	}
	sort.Strings(values)
	return strings.Join(values, ", ")
}
