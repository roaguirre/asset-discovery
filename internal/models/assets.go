package models

func (p *PipelineContext) EnsureAssetState() {
	p.Lock()
	defer p.Unlock()
	p.ensureAssetStateLocked()
}

func (p *PipelineContext) AppendAssets(assets ...Asset) {
	if len(assets) == 0 {
		return
	}

	var assetEvents []Asset
	var observationEvents []AssetObservation
	var listener MutationListener

	p.Lock()
	p.ensureAssetStateLocked()
	for _, asset := range assets {
		assetEvent, observationEvent, observationAdded := p.ingestAssetLocked(asset)
		assetEvents = append(assetEvents, assetEvent)
		if observationAdded {
			observationEvents = append(observationEvents, observationEvent)
		}
	}
	listener = p.mutationListener
	p.Unlock()

	emitMutationEvents(listener, assetEvents, observationEvents, nil)
}

func (p *PipelineContext) AppendAssetRelations(relations ...AssetRelation) {
	if len(relations) == 0 {
		return
	}

	var relationEvents []AssetRelation
	var listener MutationListener

	p.Lock()
	p.ensureAssetStateLocked()
	for _, relation := range relations {
		relationEvent, added := p.addAssetRelationLocked(relation)
		if added {
			relationEvents = append(relationEvents, relationEvent)
		}
	}
	listener = p.mutationListener
	p.Unlock()

	emitMutationEvents(listener, nil, nil, relationEvents)
}

func (p *PipelineContext) AppendAssetObservations(observations ...AssetObservation) {
	if len(observations) == 0 {
		return
	}

	var assetEvents []Asset
	var observationEvents []AssetObservation
	var listener MutationListener

	p.Lock()
	p.ensureAssetStateLocked()
	for _, observation := range observations {
		assetEvent, observationEvent, observationAdded := p.ingestObservationLocked(observation)
		assetEvents = append(assetEvents, assetEvent)
		if observationAdded {
			observationEvents = append(observationEvents, observationEvent)
		}
	}
	listener = p.mutationListener
	p.Unlock()

	emitMutationEvents(listener, assetEvents, observationEvents, nil)
}
