package models

import (
	"fmt"
	"sort"
	"strings"
)

func mergeAssetIntoCanonical(existing *Asset, incoming Asset, observationKind ObservationKind) {
	if existing == nil {
		return
	}

	if existing.ID == "" {
		existing.ID = incoming.ID
	}
	if existing.EnumerationID == "" {
		existing.EnumerationID = incoming.EnumerationID
	}
	if existing.Identifier == "" {
		existing.Identifier = incoming.Identifier
	}
	if existing.Type == "" {
		existing.Type = incoming.Type
	}
	if existing.DiscoveryDate.IsZero() || (!incoming.DiscoveryDate.IsZero() && incoming.DiscoveryDate.Before(existing.DiscoveryDate)) {
		existing.DiscoveryDate = incoming.DiscoveryDate
	}
	if observationKind == ObservationKindDiscovery {
		existing.Source = mergeSources(existing.Source, incoming.Source)
		existing.Provenance = mergeAssetProvenance(existing.Provenance, assetProvenanceFromAsset(incoming))
	}

	mergedOwnership, incomingWins := mergeOwnershipState(existing.OwnershipState, incoming.OwnershipState)
	existing.InclusionReason = mergeInclusionReason(
		existing.InclusionReason,
		incoming.InclusionReason,
		incomingWins,
		observationKind,
		existing.Provenance,
		existing.Source,
	)
	if existing.InclusionReason == "" {
		existing.InclusionReason = defaultInclusionReason(incoming)
	}
	existing.OwnershipState = mergedOwnership
	if existing.OwnershipState == "" {
		existing.OwnershipState = defaultOwnershipState(*existing)
	}

	if incoming.DomainDetails != nil {
		if existing.DomainDetails == nil {
			existing.DomainDetails = &DomainDetails{}
		}
		existing.DomainDetails.Records = mergeDomainRecords(existing.DomainDetails.Records, incoming.DomainDetails.Records)
		existing.DomainDetails.IsCatchAll = existing.DomainDetails.IsCatchAll || incoming.DomainDetails.IsCatchAll
		if incoming.DomainDetails.RDAP != nil {
			if existing.DomainDetails.RDAP == nil {
				existing.DomainDetails.RDAP = cloneRDAPData(incoming.DomainDetails.RDAP)
			} else {
				mergeRDAPData(existing.DomainDetails.RDAP, incoming.DomainDetails.RDAP)
			}
		}
	}

	if incoming.IPDetails != nil {
		if existing.IPDetails == nil {
			existing.IPDetails = &IPDetails{}
		}
		if existing.IPDetails.ASN == 0 && incoming.IPDetails.ASN != 0 {
			existing.IPDetails.ASN = incoming.IPDetails.ASN
		}
		if existing.IPDetails.Organization == "" && incoming.IPDetails.Organization != "" {
			existing.IPDetails.Organization = incoming.IPDetails.Organization
		}
		if existing.IPDetails.PTR == "" && incoming.IPDetails.PTR != "" {
			existing.IPDetails.PTR = incoming.IPDetails.PTR
		}
	}

	if existing.EnrichmentData == nil {
		existing.EnrichmentData = make(map[string]interface{})
	}
	for key, value := range incoming.EnrichmentData {
		existing.EnrichmentData[key] = value
	}

	if existing.EnrichmentStates == nil {
		existing.EnrichmentStates = make(map[string]EnrichmentState)
	}
	for key, state := range incoming.EnrichmentStates {
		if existingState, exists := existing.EnrichmentStates[key]; !exists || enrichmentStatePriority(state) >= enrichmentStatePriority(existingState) {
			existing.EnrichmentStates[key] = state
		}
	}
}

func mergeOwnershipState(existing, incoming OwnershipState) (OwnershipState, bool) {
	switch {
	case existing == OwnershipStateOwned || incoming == OwnershipStateOwned:
		return OwnershipStateOwned, incoming == OwnershipStateOwned && existing != OwnershipStateOwned
	case existing == OwnershipStateUncertain || incoming == OwnershipStateUncertain:
		return OwnershipStateUncertain, incoming == OwnershipStateUncertain && existing != OwnershipStateUncertain
	case existing == OwnershipStateAssociatedInfrastructure:
		return OwnershipStateAssociatedInfrastructure, false
	case incoming == OwnershipStateAssociatedInfrastructure:
		return OwnershipStateAssociatedInfrastructure, existing == ""
	default:
		return existing, false
	}
}

func mergeInclusionReason(
	existing string,
	incoming string,
	incomingWins bool,
	observationKind ObservationKind,
	discoveryProvenance []AssetProvenance,
	fallbackSource string,
) string {
	existing = strings.TrimSpace(existing)
	incoming = strings.TrimSpace(incoming)

	existingKind := classifyInclusionReason(existing)
	incomingKind := classifyInclusionReason(incoming)

	if observationKind == ObservationKindEnrichment {
		if existing != "" {
			return existing
		}
		if derived := summarizeDiscoveryInclusionReason(discoveryProvenance, fallbackSource); derived != "" {
			return derived
		}
		return incoming
	}

	switch {
	case incomingKind == inclusionReasonExplicit && (existingKind != inclusionReasonExplicit || incomingWins):
		return incoming
	case existingKind == inclusionReasonExplicit:
		return existing
	default:
		if derived := summarizeDiscoveryInclusionReason(discoveryProvenance, fallbackSource); derived != "" {
			return derived
		}
		if existing != "" {
			return existing
		}
		return incoming
	}
}

type inclusionReasonKind int

const (
	inclusionReasonEmpty inclusionReasonKind = iota
	inclusionReasonGeneric
	inclusionReasonExplicit
)

func classifyInclusionReason(reason string) inclusionReasonKind {
	reason = strings.TrimSpace(reason)
	switch {
	case reason == "":
		return inclusionReasonEmpty
	case strings.HasPrefix(reason, "Discovered via "):
		return inclusionReasonGeneric
	case strings.HasPrefix(reason, "Supported by ") && strings.Contains(reason, " discovery observations"):
		return inclusionReasonGeneric
	default:
		return inclusionReasonExplicit
	}
}

func summarizeDiscoveryInclusionReason(provenance []AssetProvenance, fallbackSource string) string {
	observationCount := 0
	sourceValues := make([]string, 0, len(provenance))
	seenSources := make(map[string]struct{}, len(provenance))

	for _, item := range provenance {
		if item.AssetID == "" && item.EnumerationID == "" && item.Source == "" && item.DiscoveryDate.IsZero() {
			continue
		}
		observationCount++

		source := strings.TrimSpace(strings.ToLower(item.Source))
		if source == "" {
			continue
		}
		if _, exists := seenSources[source]; exists {
			continue
		}
		seenSources[source] = struct{}{}
		sourceValues = append(sourceValues, source)
	}

	sort.Strings(sourceValues)
	if len(sourceValues) == 0 {
		sourceValues = splitSourceValues(fallbackSource)
	}
	if observationCount == 0 {
		observationCount = len(sourceValues)
	}
	if observationCount == 0 {
		return ""
	}
	if observationCount == 1 && len(sourceValues) > 0 {
		return "Discovered via " + sourceValues[0]
	}
	if len(sourceValues) == 0 {
		return fmt.Sprintf("Supported by %d discovery observations", observationCount)
	}
	return fmt.Sprintf(
		"Supported by %d discovery observations from %s",
		observationCount,
		strings.Join(sourceValues, ", "),
	)
}

func mergeSources(existing string, incoming string) string {
	values := splitSourceValues(existing)
	values = append(values, splitSourceValues(incoming)...)
	return strings.Join(uniqueNormalizedStrings(values), ", ")
}

func splitSourceValues(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(strings.ToLower(part))
		if part == "" {
			continue
		}
		out = append(out, part)
	}
	return out
}

func defaultOwnershipState(asset Asset) OwnershipState {
	if asset.Type == AssetTypeIP {
		return OwnershipStateAssociatedInfrastructure
	}
	return OwnershipStateOwned
}

func defaultInclusionReason(asset Asset) string {
	source := strings.TrimSpace(asset.Source)
	if source == "" {
		return ""
	}
	return "Discovered via " + source
}

func DomainResolutionStatusForAsset(asset Asset) DomainResolutionStatus {
	if asset.Type != AssetTypeDomain {
		return ""
	}
	if asset.DomainDetails != nil && len(asset.DomainDetails.Records) > 0 {
		return DomainResolutionStatusResolved
	}

	state, exists := asset.EnrichmentStates["domain_enricher"]
	if !exists {
		return DomainResolutionStatusNotChecked
	}

	switch strings.TrimSpace(strings.ToLower(state.Status)) {
	case "completed", "cached":
		return DomainResolutionStatusUnresolved
	case "failed", "retryable":
		return DomainResolutionStatusLookupFailed
	default:
		return DomainResolutionStatusNotChecked
	}
}

func enrichmentStatePriority(state EnrichmentState) int {
	switch strings.TrimSpace(strings.ToLower(state.Status)) {
	case "completed":
		return 4
	case "cached":
		return 3
	case "retryable":
		return 2
	case "failed":
		return 1
	default:
		return 0
	}
}
