package models

import (
	"sort"
	"strings"
	"time"
)

func (p *PipelineContext) EnsureAssetState() {
	p.Lock()
	defer p.Unlock()
	p.ensureAssetStateLocked()
}

func (p *PipelineContext) AppendAssets(assets ...Asset) {
	if len(assets) == 0 {
		return
	}

	p.Lock()
	defer p.Unlock()

	p.ensureAssetStateLocked()
	for _, asset := range assets {
		p.ingestAssetLocked(asset)
	}
}

func (p *PipelineContext) AppendAssetRelations(relations ...AssetRelation) {
	if len(relations) == 0 {
		return
	}

	p.Lock()
	defer p.Unlock()

	p.ensureAssetStateLocked()
	for _, relation := range relations {
		p.addAssetRelationLocked(relation)
	}
}

func (p *PipelineContext) AppendAssetObservations(observations ...AssetObservation) {
	if len(observations) == 0 {
		return
	}

	p.Lock()
	defer p.Unlock()

	p.ensureAssetStateLocked()
	for _, observation := range observations {
		p.ingestObservationLocked(observation)
	}
}

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

func (p *PipelineContext) ingestAssetLocked(asset Asset) {
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
	}
}

func (p *PipelineContext) ingestObservationLocked(observation AssetObservation) {
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
	}
}

func (p *PipelineContext) addAssetRelationLocked(relation AssetRelation) {
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
		return
	}

	p.Relations = append(p.Relations, relation)
	p.relationIndexByKey[key] = len(p.Relations) - 1
}

func (p *PipelineContext) resolveCanonicalAssetIDLocked(assetType AssetType, identifier string) string {
	key := canonicalAssetKey(assetType, identifier)
	index, exists := p.assetIndexByKey[key]
	if !exists || index < 0 || index >= len(p.Assets) {
		return ""
	}
	return p.Assets[index].ID
}

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
	existing.InclusionReason = mergeInclusionReason(existing.InclusionReason, incoming.InclusionReason, incomingWins)
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

func mergeInclusionReason(existing, incoming string, incomingWins bool) string {
	existing = strings.TrimSpace(existing)
	incoming = strings.TrimSpace(incoming)
	switch {
	case incomingWins && incoming != "":
		return incoming
	case existing != "":
		return existing
	default:
		return incoming
	}
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

func assetRelationKey(relation AssetRelation) string {
	return strings.Join([]string{
		relation.FromAssetID,
		string(relation.FromAssetType),
		relation.FromIdentifier,
		relation.ToAssetID,
		string(relation.ToAssetType),
		relation.ToIdentifier,
		relation.ObservationID,
		relation.EnumerationID,
		relation.Source,
		relation.Kind,
		relation.Label,
		relation.Reason,
		relation.DiscoveryDate.UTC().Format(time.RFC3339Nano),
	}, "|")
}

func cloneDomainDetails(details *DomainDetails) *DomainDetails {
	if details == nil {
		return nil
	}
	clone := *details
	clone.Records = append([]DNSRecord(nil), details.Records...)
	clone.RDAP = cloneRDAPData(details.RDAP)
	return &clone
}

func cloneIPDetails(details *IPDetails) *IPDetails {
	if details == nil {
		return nil
	}
	clone := *details
	return &clone
}

func cloneEnrichmentData(values map[string]interface{}) map[string]interface{} {
	if len(values) == 0 {
		return nil
	}
	clone := make(map[string]interface{}, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
}

func cloneEnrichmentStates(values map[string]EnrichmentState) map[string]EnrichmentState {
	if len(values) == 0 {
		return nil
	}
	clone := make(map[string]EnrichmentState, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
}

func mergeDomainRecords(existing []DNSRecord, incoming []DNSRecord) []DNSRecord {
	if len(existing) == 0 && len(incoming) == 0 {
		return nil
	}
	merged := append([]DNSRecord(nil), existing...)
	seen := make(map[string]struct{}, len(existing)+len(incoming))
	for _, record := range merged {
		seen[domainRecordKey(record)] = struct{}{}
	}
	for _, record := range incoming {
		key := domainRecordKey(record)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		merged = append(merged, record)
	}
	return uniqueDomainRecords(merged)
}

func uniqueDomainRecords(records []DNSRecord) []DNSRecord {
	if len(records) == 0 {
		return nil
	}
	unique := make([]DNSRecord, 0, len(records))
	seen := make(map[string]struct{}, len(records))
	for _, record := range records {
		record.Type = strings.TrimSpace(strings.ToUpper(record.Type))
		record.Value = strings.TrimSpace(record.Value)
		if record.Type == "" || record.Value == "" {
			continue
		}
		key := domainRecordKey(record)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, record)
	}
	sort.SliceStable(unique, func(i, j int) bool {
		if unique[i].Type == unique[j].Type {
			return strings.ToLower(unique[i].Value) < strings.ToLower(unique[j].Value)
		}
		return unique[i].Type < unique[j].Type
	})
	return unique
}

func domainRecordKey(record DNSRecord) string {
	return strings.TrimSpace(strings.ToUpper(record.Type)) + "|" + strings.TrimSpace(strings.ToLower(record.Value))
}

func cloneRDAPData(data *RDAPData) *RDAPData {
	if data == nil {
		return nil
	}
	clone := *data
	clone.Statuses = append([]string(nil), data.Statuses...)
	clone.NameServers = append([]string(nil), data.NameServers...)
	return &clone
}

func mergeRDAPData(existing, incoming *RDAPData) {
	if existing == nil || incoming == nil {
		return
	}

	if existing.RegistrarName == "" {
		existing.RegistrarName = incoming.RegistrarName
	}
	if existing.RegistrarIANAID == "" {
		existing.RegistrarIANAID = incoming.RegistrarIANAID
	}
	if existing.RegistrarURL == "" {
		existing.RegistrarURL = incoming.RegistrarURL
	}
	if existing.CreationDate.IsZero() {
		existing.CreationDate = incoming.CreationDate
	}
	if existing.ExpirationDate.IsZero() {
		existing.ExpirationDate = incoming.ExpirationDate
	}
	if existing.UpdatedDate.IsZero() {
		existing.UpdatedDate = incoming.UpdatedDate
	}
	if existing.RegistrantName == "" {
		existing.RegistrantName = incoming.RegistrantName
	}
	if existing.RegistrantEmail == "" {
		existing.RegistrantEmail = incoming.RegistrantEmail
	}
	if existing.RegistrantOrg == "" {
		existing.RegistrantOrg = incoming.RegistrantOrg
	}

	existing.Statuses = uniqueNormalizedStrings(append(existing.Statuses, incoming.Statuses...))
	existing.NameServers = uniqueNormalizedStrings(append(existing.NameServers, incoming.NameServers...))
}

func assetProvenanceFromAsset(asset Asset) AssetProvenance {
	return AssetProvenance{
		AssetID:       strings.TrimSpace(asset.ID),
		EnumerationID: strings.TrimSpace(asset.EnumerationID),
		Source:        strings.TrimSpace(strings.ToLower(asset.Source)),
		DiscoveryDate: asset.DiscoveryDate,
	}
}

func assetProvenanceFromObservation(observation AssetObservation) AssetProvenance {
	return AssetProvenance{
		AssetID:       strings.TrimSpace(observation.ID),
		EnumerationID: strings.TrimSpace(observation.EnumerationID),
		Source:        strings.TrimSpace(strings.ToLower(observation.Source)),
		DiscoveryDate: observation.DiscoveryDate,
	}
}

func mergeAssetProvenance(existing []AssetProvenance, incoming ...AssetProvenance) []AssetProvenance {
	merged := append([]AssetProvenance(nil), existing...)
	index := make(map[string]int, len(merged))
	for i, item := range merged {
		index[assetProvenanceKey(item)] = i
	}

	for _, item := range incoming {
		if item.AssetID == "" && item.EnumerationID == "" && item.Source == "" && item.DiscoveryDate.IsZero() {
			continue
		}

		key := assetProvenanceKey(item)
		if _, exists := index[key]; exists {
			continue
		}

		index[key] = len(merged)
		merged = append(merged, item)
	}

	sort.SliceStable(merged, func(i, j int) bool {
		if merged[i].DiscoveryDate.Equal(merged[j].DiscoveryDate) {
			return assetProvenanceKey(merged[i]) < assetProvenanceKey(merged[j])
		}
		if merged[i].DiscoveryDate.IsZero() {
			return false
		}
		if merged[j].DiscoveryDate.IsZero() {
			return true
		}
		return merged[i].DiscoveryDate.Before(merged[j].DiscoveryDate)
	})

	return merged
}

func assetProvenanceKey(item AssetProvenance) string {
	return item.AssetID + "|" + item.EnumerationID + "|" + item.Source + "|" + item.DiscoveryDate.UTC().Format(time.RFC3339Nano)
}
