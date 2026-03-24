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
		groupID := rootID + ":seeds"
		nodes = append(nodes, TraceNode{
			ID:            groupID,
			ParentID:      rootID,
			Kind:          "group",
			Label:         "Seed Context",
			Subtitle:      fmt.Sprintf("%d contributing seeds", len(uniqueTraceContributorValues(contributors, func(item TraceContributor) string { return item.SeedID }))),
			LinkedAssetID: asset.ID,
		})
		for _, contributor := range contributors {
			seed := seedByID[contributor.SeedID]
			nodes = append(nodes, TraceNode{
				ID:            "seed:" + contributor.SeedID + ":" + contributor.AssetID,
				ParentID:      groupID,
				Kind:          "seed",
				Label:         contributor.SeedLabel,
				Subtitle:      contributor.SeedID,
				Badges:        compactTraceValues(strings.Join(seed.Domains, ", "), FormatDateTime(contributor.DiscoveryDate)),
				LinkedAssetID: asset.ID,
				Details: []TraceSection{
					{Title: "Seed", Items: compactTraceValues("Seed ID: "+contributor.SeedID, "Domains: "+strings.Join(seed.Domains, ", "), "Source: "+contributor.Source)},
					{Title: "Evidence", Items: formatSeedEvidence(seed.Evidence)},
				},
			})
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
				"Seed ID: "+enum.SeedID,
				"Seed domains: "+strings.Join(seed.Domains, ", "),
			),
		})
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
