package models

import "strings"

func canonicalAssetKey(assetType AssetType, identifier string) string {
	return string(assetType) + "|" + strings.TrimSpace(strings.ToLower(identifier))
}

func normalizeAsset(asset Asset) Asset {
	asset.ID = strings.TrimSpace(asset.ID)
	asset.EnumerationID = strings.TrimSpace(asset.EnumerationID)
	asset.Identifier = strings.TrimSpace(asset.Identifier)
	asset.Source = strings.TrimSpace(asset.Source)
	asset.InclusionReason = strings.TrimSpace(asset.InclusionReason)
	asset.OwnershipState = OwnershipState(strings.TrimSpace(strings.ToLower(string(asset.OwnershipState))))
	asset.DomainDetails = cloneDomainDetails(asset.DomainDetails)
	asset.IPDetails = cloneIPDetails(asset.IPDetails)
	asset.EnrichmentData = cloneEnrichmentData(asset.EnrichmentData)
	asset.EnrichmentStates = cloneEnrichmentStates(asset.EnrichmentStates)
	if asset.Type == AssetTypeDomain {
		asset.Identifier = strings.ToLower(asset.Identifier)
		if asset.DomainDetails != nil {
			asset.DomainDetails.Records = uniqueDomainRecords(asset.DomainDetails.Records)
		}
	}
	if asset.Type == AssetTypeIP {
		asset.Identifier = strings.ToLower(asset.Identifier)
	}
	return asset
}

func normalizeAssetRelation(relation AssetRelation) AssetRelation {
	relation.ID = strings.TrimSpace(relation.ID)
	relation.FromAssetID = strings.TrimSpace(relation.FromAssetID)
	relation.FromIdentifier = strings.TrimSpace(strings.ToLower(relation.FromIdentifier))
	relation.ToAssetID = strings.TrimSpace(relation.ToAssetID)
	relation.ToIdentifier = strings.TrimSpace(strings.ToLower(relation.ToIdentifier))
	relation.ObservationID = strings.TrimSpace(relation.ObservationID)
	relation.EnumerationID = strings.TrimSpace(relation.EnumerationID)
	relation.Source = strings.TrimSpace(strings.ToLower(relation.Source))
	relation.Kind = strings.TrimSpace(strings.ToLower(relation.Kind))
	relation.Label = strings.TrimSpace(relation.Label)
	relation.Reason = strings.TrimSpace(relation.Reason)
	return relation
}

func assetObservationFromAsset(asset Asset) AssetObservation {
	return AssetObservation{
		Kind:             ObservationKindDiscovery,
		ID:               asset.ID,
		EnumerationID:    asset.EnumerationID,
		Type:             asset.Type,
		Identifier:       asset.Identifier,
		Source:           asset.Source,
		DiscoveryDate:    asset.DiscoveryDate,
		OwnershipState:   asset.OwnershipState,
		InclusionReason:  asset.InclusionReason,
		DomainDetails:    cloneDomainDetails(asset.DomainDetails),
		IPDetails:        cloneIPDetails(asset.IPDetails),
		EnrichmentData:   cloneEnrichmentData(asset.EnrichmentData),
		EnrichmentStates: cloneEnrichmentStates(asset.EnrichmentStates),
	}
}

func normalizeAssetObservation(observation AssetObservation) AssetObservation {
	observation.Kind = normalizeObservationKind(observation.Kind)
	observation.ID = strings.TrimSpace(observation.ID)
	observation.AssetID = strings.TrimSpace(observation.AssetID)
	observation.EnumerationID = strings.TrimSpace(observation.EnumerationID)
	observation.Identifier = strings.TrimSpace(observation.Identifier)
	observation.Source = strings.TrimSpace(strings.ToLower(observation.Source))
	observation.InclusionReason = strings.TrimSpace(observation.InclusionReason)
	observation.OwnershipState = OwnershipState(strings.TrimSpace(strings.ToLower(string(observation.OwnershipState))))
	observation.DomainDetails = cloneDomainDetails(observation.DomainDetails)
	observation.IPDetails = cloneIPDetails(observation.IPDetails)
	observation.EnrichmentData = cloneEnrichmentData(observation.EnrichmentData)
	observation.EnrichmentStates = cloneEnrichmentStates(observation.EnrichmentStates)
	if observation.Type == AssetTypeDomain {
		observation.Identifier = strings.ToLower(observation.Identifier)
		if observation.DomainDetails != nil {
			observation.DomainDetails.Records = uniqueDomainRecords(observation.DomainDetails.Records)
		}
	}
	if observation.Type == AssetTypeIP {
		observation.Identifier = strings.ToLower(observation.Identifier)
	}
	return observation
}

func normalizeObservationKind(kind ObservationKind) ObservationKind {
	switch ObservationKind(strings.TrimSpace(strings.ToLower(string(kind)))) {
	case ObservationKindEnrichment:
		return ObservationKindEnrichment
	default:
		return ObservationKindDiscovery
	}
}

func assetFromObservation(observation AssetObservation) Asset {
	return Asset{
		ID:               strings.TrimSpace(observation.AssetID),
		EnumerationID:    observation.EnumerationID,
		Type:             observation.Type,
		Identifier:       observation.Identifier,
		Source:           observation.Source,
		DiscoveryDate:    observation.DiscoveryDate,
		OwnershipState:   observation.OwnershipState,
		InclusionReason:  observation.InclusionReason,
		DomainDetails:    cloneDomainDetails(observation.DomainDetails),
		IPDetails:        cloneIPDetails(observation.IPDetails),
		EnrichmentData:   cloneEnrichmentData(observation.EnrichmentData),
		EnrichmentStates: cloneEnrichmentStates(observation.EnrichmentStates),
	}
}
