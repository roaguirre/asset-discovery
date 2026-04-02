package models

import (
	"sort"
	"strings"
	"time"
)

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

func cloneAsset(asset Asset) Asset {
	clone := asset
	clone.Provenance = append([]AssetProvenance(nil), asset.Provenance...)
	clone.DomainDetails = cloneDomainDetails(asset.DomainDetails)
	clone.IPDetails = cloneIPDetails(asset.IPDetails)
	clone.EnrichmentData = cloneEnrichmentData(asset.EnrichmentData)
	clone.EnrichmentStates = cloneEnrichmentStates(asset.EnrichmentStates)
	return clone
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
