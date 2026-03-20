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

func BuildTrace(
	asset models.Asset,
	domainKind string,
	registrableDomain string,
	contributors []TraceContributor,
	enumByID map[string]models.Enumeration,
	seedByID map[string]models.Seed,
) Trace {
	trace := Trace{
		AssetID:           asset.ID,
		Identifier:        asset.Identifier,
		AssetType:         string(asset.Type),
		Source:            asset.Source,
		EnumerationID:     SummarizeContributorValues(contributors, func(item TraceContributor) string { return item.EnumerationID }),
		SeedID:            SummarizeContributorValues(contributors, func(item TraceContributor) string { return item.SeedID }),
		DomainKind:        domainKind,
		RegistrableDomain: registrableDomain,
		Contributors:      contributors,
	}

	identityItems := []string{
		"Asset ID: " + asset.ID,
		"Asset type: " + string(asset.Type),
		"Collected from: " + asset.Source,
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
		return nil
	}

	items := make([]string, 0, 8)
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
		if rdap.RegistrantOrg != "" {
			items = append(items, "Registrant org: "+rdap.RegistrantOrg)
		}
		if len(rdap.NameServers) > 0 {
			items = append(items, "Nameservers: "+strings.Join(rdap.NameServers, ", "))
		}
		if !rdap.CreationDate.IsZero() {
			items = append(items, "Registration created: "+rdap.CreationDate.Format("2006-01-02"))
		}
		if !rdap.ExpirationDate.IsZero() {
			items = append(items, "Registration expires: "+rdap.ExpirationDate.Format("2006-01-02"))
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
