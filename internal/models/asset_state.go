package models

func (p *PipelineContext) ensureAssetStateLocked() {
	if p.assetIndexByKey == nil {
		p.assetIndexByKey = make(map[string]int)
	}
	if p.observationIndexByID == nil {
		p.observationIndexByID = make(map[string]int)
	}
	if p.relationIndexByKey == nil {
		p.relationIndexByKey = make(map[string]int)
	}
	if p.assetStateInitialized {
		p.rebuildAssetIndexLocked()
		p.reconcilePendingRawAssetsLocked()
		return
	}

	rawAssets := append([]Asset(nil), p.Assets...)
	p.Assets = nil
	p.Observations = nil
	p.Relations = nil
	p.assetIndexByKey = make(map[string]int)
	p.observationIndexByID = make(map[string]int)
	p.relationIndexByKey = make(map[string]int)

	for _, asset := range rawAssets {
		p.ingestAssetLocked(asset)
	}

	p.assetStateInitialized = true
}

func (p *PipelineContext) rebuildAssetIndexLocked() {
	p.assetIndexByKey = make(map[string]int, len(p.Assets))
	for index, asset := range p.Assets {
		p.assetIndexByKey[canonicalAssetKey(asset.Type, asset.Identifier)] = index
	}
}

func (p *PipelineContext) reconcilePendingRawAssetsLocked() {
	if len(p.Assets) == 0 {
		return
	}

	pending := make([]Asset, 0)
	canonical := make([]Asset, 0, len(p.Assets))
	for _, asset := range p.Assets {
		if asset.ID != "" {
			if _, known := p.observationIndexByID[asset.ID]; !known {
				pending = append(pending, asset)
				continue
			}
		}
		canonical = append(canonical, asset)
	}

	if len(pending) == 0 {
		p.Assets = canonical
		p.rebuildAssetIndexLocked()
		return
	}

	p.Assets = canonical
	p.rebuildAssetIndexLocked()
	for _, asset := range pending {
		p.ingestAssetLocked(asset)
	}
}

func (p *PipelineContext) ingestAssetLocked(asset Asset) (Asset, AssetObservation, bool) {
	asset = normalizeAsset(asset)
	if asset.ID == "" {
		asset.ID = NewID("asset")
	}

	observation := assetObservationFromAsset(asset)
	key := canonicalAssetKey(asset.Type, asset.Identifier)
	index, exists := p.assetIndexByKey[key]
	if !exists {
		canonical := asset
		canonical.Provenance = mergeAssetProvenance(nil, assetProvenanceFromAsset(asset))
		canonical.DomainDetails = cloneDomainDetails(asset.DomainDetails)
		canonical.IPDetails = cloneIPDetails(asset.IPDetails)
		canonical.EnrichmentData = cloneEnrichmentData(asset.EnrichmentData)
		canonical.EnrichmentStates = cloneEnrichmentStates(asset.EnrichmentStates)
		if canonical.OwnershipState == "" {
			canonical.OwnershipState = defaultOwnershipState(canonical)
		}
		if canonical.InclusionReason == "" {
			canonical.InclusionReason = defaultInclusionReason(canonical)
		}

		p.Assets = append(p.Assets, canonical)
		index = len(p.Assets) - 1
		p.assetIndexByKey[key] = index
	} else {
		existing := &p.Assets[index]
		mergeAssetIntoCanonical(existing, asset, ObservationKindDiscovery)
		observation.AssetID = existing.ID
	}

	if observation.AssetID == "" {
		observation.AssetID = p.Assets[index].ID
	}

	if _, exists := p.observationIndexByID[observation.ID]; !exists {
		p.Observations = append(p.Observations, observation)
		p.observationIndexByID[observation.ID] = len(p.Observations) - 1
		return cloneAsset(p.Assets[index]), observation, true
	}

	return cloneAsset(p.Assets[index]), observation, false
}

func (p *PipelineContext) ingestObservationLocked(observation AssetObservation) (Asset, AssetObservation, bool) {
	observation = normalizeAssetObservation(observation)
	if observation.ID == "" {
		observation.ID = NewID("obs")
	}

	canonical := assetFromObservation(observation)
	key := canonicalAssetKey(observation.Type, observation.Identifier)
	index, exists := p.assetIndexByKey[key]
	if !exists {
		if canonical.ID == "" {
			canonical.ID = NewID("asset")
		}
		if observation.Kind == ObservationKindDiscovery {
			canonical.Provenance = mergeAssetProvenance(nil, assetProvenanceFromObservation(observation))
		}
		if canonical.OwnershipState == "" {
			canonical.OwnershipState = defaultOwnershipState(canonical)
		}
		if canonical.InclusionReason == "" {
			canonical.InclusionReason = defaultInclusionReason(canonical)
		}
		p.Assets = append(p.Assets, canonical)
		index = len(p.Assets) - 1
		p.assetIndexByKey[key] = index
	} else {
		existing := &p.Assets[index]
		mergeAssetIntoCanonical(existing, canonical, observation.Kind)
		observation.AssetID = existing.ID
	}

	if observation.AssetID == "" {
		observation.AssetID = p.Assets[index].ID
	}

	if _, exists := p.observationIndexByID[observation.ID]; !exists {
		p.Observations = append(p.Observations, observation)
		p.observationIndexByID[observation.ID] = len(p.Observations) - 1
		return cloneAsset(p.Assets[index]), observation, true
	}

	return cloneAsset(p.Assets[index]), observation, false
}

func (p *PipelineContext) addAssetRelationLocked(relation AssetRelation) (AssetRelation, bool) {
	relation = normalizeAssetRelation(relation)
	if relation.ID == "" {
		relation.ID = NewID("rel")
	}
	if relation.FromIdentifier != "" {
		if resolved := p.resolveCanonicalAssetIDLocked(relation.FromAssetType, relation.FromIdentifier); resolved != "" {
			relation.FromAssetID = resolved
		}
	}
	if relation.ToIdentifier != "" {
		if resolved := p.resolveCanonicalAssetIDLocked(relation.ToAssetType, relation.ToIdentifier); resolved != "" {
			relation.ToAssetID = resolved
		}
	}

	key := assetRelationKey(relation)
	if _, exists := p.relationIndexByKey[key]; exists {
		return AssetRelation{}, false
	}

	p.Relations = append(p.Relations, relation)
	p.relationIndexByKey[key] = len(p.Relations) - 1
	return relation, true
}

func emitMutationEvents(listener MutationListener, assets []Asset, observations []AssetObservation, relations []AssetRelation) {
	if listener == nil {
		return
	}

	for _, asset := range assets {
		listener.OnAssetUpsert(asset)
	}
	for _, observation := range observations {
		listener.OnObservationAdded(observation)
	}
	for _, relation := range relations {
		listener.OnRelationAdded(relation)
	}
}

func (p *PipelineContext) resolveCanonicalAssetIDLocked(assetType AssetType, identifier string) string {
	key := canonicalAssetKey(assetType, identifier)
	index, exists := p.assetIndexByKey[key]
	if !exists || index < 0 || index >= len(p.Assets) {
		return ""
	}
	return p.Assets[index].ID
}
