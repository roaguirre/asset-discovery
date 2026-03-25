package runservice

import (
	"fmt"
	"sort"
	"strings"
	"time"

	exportshared "asset-discovery/internal/export/shared"
	"asset-discovery/internal/models"
	"asset-discovery/internal/tracing/lineage"
)

// EvidenceGroup stores grouped supporting details for a projected asset row.
type EvidenceGroup struct {
	Title string   `json:"title"`
	Items []string `json:"items,omitempty"`
}

// AssetRow is the live asset document persisted under runs/{runID}/assets.
type AssetRow struct {
	AssetID           string          `json:"asset_id"`
	Identifier        string          `json:"identifier"`
	AssetType         string          `json:"asset_type"`
	DomainKind        string          `json:"domain_kind,omitempty"`
	RegistrableDomain string          `json:"registrable_domain,omitempty"`
	ResolutionStatus  string          `json:"resolution_status,omitempty"`
	OwnershipState    string          `json:"ownership_state,omitempty"`
	InclusionReason   string          `json:"inclusion_reason,omitempty"`
	ASN               int             `json:"asn,omitempty"`
	Organization      string          `json:"organization,omitempty"`
	PTR               string          `json:"ptr,omitempty"`
	Source            string          `json:"source"`
	DiscoveredBy      string          `json:"discovered_by,omitempty"`
	EnrichedBy        string          `json:"enriched_by,omitempty"`
	EnumerationID     string          `json:"enumeration_id"`
	SeedID            string          `json:"seed_id"`
	Status            string          `json:"status"`
	DiscoveryDate     time.Time       `json:"discovery_date,omitempty"`
	Details           string          `json:"details,omitempty"`
	EvidenceGroups    []EvidenceGroup `json:"evidence_groups,omitempty"`
	TracePath         string          `json:"trace_path,omitempty"`
}

type assetProjectionInputs struct {
	enumByID            map[string]models.Enumeration
	seedByID            map[string]models.Seed
	observationsByAsset map[string][]models.AssetObservation
	relationsByAsset    map[string][]models.AssetRelation
}

func buildProjectedAssetReadModel(runID string, pCtx *models.PipelineContext) ([]AssetRow, []lineage.Trace) {
	inputs := buildAssetProjectionInputs(pCtx)
	rows := make([]AssetRow, 0, len(pCtx.Assets))
	tracesByAssetID := make(map[string]lineage.Trace, len(pCtx.Assets))

	for _, asset := range exportshared.SortedAssetsForExport(pCtx.Assets) {
		row, trace := buildProjectedAssetData(runID, asset, inputs)
		rows = append(rows, row)
		tracesByAssetID[asset.ID] = trace
	}

	sort.SliceStable(rows, func(i, j int) bool {
		if assetRowGroup(rows[i]) != assetRowGroup(rows[j]) {
			return assetRowGroup(rows[i]) < assetRowGroup(rows[j])
		}
		if rows[i].RegistrableDomain != rows[j].RegistrableDomain {
			return rows[i].RegistrableDomain < rows[j].RegistrableDomain
		}
		if rows[i].DiscoveryDate.Equal(rows[j].DiscoveryDate) {
			return rows[i].Identifier < rows[j].Identifier
		}
		return rows[i].DiscoveryDate.After(rows[j].DiscoveryDate)
	})

	traces := make([]lineage.Trace, 0, len(rows))
	for _, row := range rows {
		trace, ok := tracesByAssetID[row.AssetID]
		if !ok {
			continue
		}
		trace.Related = buildTraceLinks(runID, row, rows)
		traces = append(traces, trace)
	}

	return rows, traces
}

func buildProjectedAssetRow(runID string, pCtx *models.PipelineContext, assetID string) (AssetRow, bool) {
	rows, _ := buildProjectedAssetReadModel(runID, pCtx)
	for _, row := range rows {
		if row.AssetID == assetID {
			return row, true
		}
	}
	return AssetRow{}, false
}

func buildAssetProjectionInputs(pCtx *models.PipelineContext) assetProjectionInputs {
	enumByID := make(map[string]models.Enumeration, len(pCtx.Enumerations))
	for _, enumeration := range pCtx.Enumerations {
		enumByID[enumeration.ID] = enumeration
	}

	seedByID := make(map[string]models.Seed, len(pCtx.Seeds))
	for _, seed := range pCtx.Seeds {
		seedByID[seed.ID] = seed
	}

	observationsByAsset := make(map[string][]models.AssetObservation, len(pCtx.Observations))
	for _, observation := range pCtx.Observations {
		if observation.AssetID == "" {
			continue
		}
		observationsByAsset[observation.AssetID] = append(observationsByAsset[observation.AssetID], observation)
	}

	relationsByAsset := make(map[string][]models.AssetRelation, len(pCtx.Relations))
	for _, relation := range pCtx.Relations {
		if relation.FromAssetID != "" {
			relationsByAsset[relation.FromAssetID] = append(relationsByAsset[relation.FromAssetID], relation)
		}
		if relation.ToAssetID != "" && relation.ToAssetID != relation.FromAssetID {
			relationsByAsset[relation.ToAssetID] = append(relationsByAsset[relation.ToAssetID], relation)
		}
	}

	return assetProjectionInputs{
		enumByID:            enumByID,
		seedByID:            seedByID,
		observationsByAsset: observationsByAsset,
		relationsByAsset:    relationsByAsset,
	}
}

func buildProjectedAssetData(
	runID string,
	asset models.Asset,
	inputs assetProjectionInputs,
) (AssetRow, lineage.Trace) {
	classified := exportshared.ClassifyAsset(asset)
	contributors := lineage.BuildTraceContributors(asset, inputs.enumByID, inputs.seedByID)
	allSources, discoverySources, enrichmentSources := lineage.SummarizeObservationSources(
		inputs.observationsByAsset[asset.ID],
		asset.Source,
	)

	rowSource := discoverySources
	if strings.TrimSpace(rowSource) == "" {
		rowSource = allSources
	}

	row := AssetRow{
		AssetID:           asset.ID,
		Identifier:        asset.Identifier,
		AssetType:         string(asset.Type),
		DomainKind:        string(classified.DomainKind),
		RegistrableDomain: classified.RegistrableDomain,
		ResolutionStatus:  string(models.DomainResolutionStatusForAsset(asset)),
		OwnershipState:    string(asset.OwnershipState),
		InclusionReason:   asset.InclusionReason,
		Source:            rowSource,
		DiscoveredBy:      discoverySources,
		EnrichedBy:        enrichmentSources,
		EnumerationID: lineage.SummarizeContributorValues(
			contributors,
			func(item lineage.TraceContributor) string { return item.EnumerationID },
		),
		SeedID: lineage.SummarizeContributorValues(
			contributors,
			func(item lineage.TraceContributor) string { return item.SeedID },
		),
		Status:         lineage.SummarizeTraceStatus(asset, contributors, inputs.enumByID),
		DiscoveryDate:  asset.DiscoveryDate,
		Details:        buildProjectedAssetDetails(asset),
		EvidenceGroups: buildProjectedEvidenceGroups(asset),
		TracePath:      lineage.BuildTracePath(runID, asset.ID),
	}
	if asset.IPDetails != nil {
		row.ASN = asset.IPDetails.ASN
		row.Organization = asset.IPDetails.Organization
		row.PTR = asset.IPDetails.PTR
	}

	trace := lineage.BuildTrace(
		asset,
		string(classified.DomainKind),
		classified.RegistrableDomain,
		contributors,
		inputs.observationsByAsset[asset.ID],
		inputs.relationsByAsset[asset.ID],
		inputs.enumByID,
		inputs.seedByID,
	)

	return row, trace
}

func assetRowGroup(row AssetRow) int {
	const (
		groupRegistrableDomain = iota
		groupSubdomain
		groupIP
	)

	if row.AssetType == string(models.AssetTypeDomain) {
		switch row.DomainKind {
		case string(exportshared.DomainKindRegistrable):
			return groupRegistrableDomain
		case string(exportshared.DomainKindSubdomain):
			return groupSubdomain
		}
	}

	return groupIP
}

func buildProjectedAssetDetails(asset models.Asset) string {
	parts := make([]string, 0, 16)

	if asset.OwnershipState != "" {
		parts = append(parts, "Ownership "+string(asset.OwnershipState))
	}
	if asset.InclusionReason != "" {
		parts = append(parts, "Reason "+asset.InclusionReason)
	}
	if resolution := models.DomainResolutionStatusForAsset(asset); resolution != "" {
		parts = append(parts, "Resolution "+string(resolution))
	}

	if asset.DomainDetails != nil {
		if len(asset.DomainDetails.Records) > 0 {
			recordParts := make([]string, 0, len(asset.DomainDetails.Records))
			for _, record := range asset.DomainDetails.Records {
				recordParts = append(recordParts, fmt.Sprintf("%s:%s", record.Type, record.Value))
			}
			parts = append(parts, "DNS "+strings.Join(recordParts, "; "))
		}

		if asset.DomainDetails.IsCatchAll {
			parts = append(parts, "Catch-all")
		}

		if asset.DomainDetails.RDAP != nil {
			appendProjectedRDAPDetails(&parts, asset.DomainDetails.RDAP)
		}
	}

	if asset.IPDetails != nil {
		if asset.IPDetails.ASN != 0 {
			parts = append(parts, fmt.Sprintf("ASN %d", asset.IPDetails.ASN))
		}
		if asset.IPDetails.Organization != "" {
			parts = append(parts, "Org "+asset.IPDetails.Organization)
		}
		if asset.IPDetails.PTR != "" {
			parts = append(parts, "PTR "+asset.IPDetails.PTR)
		}
	}

	if asset.EnrichmentData != nil {
		if cidr, ok := asset.EnrichmentData["cidr"].(string); ok && cidr != "" {
			parts = append(parts, "CIDR "+cidr)
		}
	}

	return strings.Join(parts, " | ")
}

func appendProjectedRDAPDetails(parts *[]string, rdap *models.RDAPData) {
	if rdap == nil {
		return
	}
	if rdap.RegistrarName != "" {
		*parts = append(*parts, "Registrar "+rdap.RegistrarName)
	}
	if rdap.RegistrarURL != "" {
		*parts = append(*parts, "RegistrarURL "+rdap.RegistrarURL)
	}
	if rdap.RegistrantName != "" {
		*parts = append(*parts, "RegistrantName "+rdap.RegistrantName)
	}
	if rdap.RegistrantOrg != "" {
		*parts = append(*parts, "RegistrantOrg "+rdap.RegistrantOrg)
	}
	if rdap.RegistrantEmail != "" {
		*parts = append(*parts, "RegistrantEmail "+rdap.RegistrantEmail)
	}
	if len(rdap.NameServers) > 0 {
		*parts = append(*parts, "Nameservers "+strings.Join(rdap.NameServers, ", "))
	}
	if !rdap.CreationDate.IsZero() {
		*parts = append(*parts, "Created "+rdap.CreationDate.Format("2006-01-02"))
	}
	if !rdap.UpdatedDate.IsZero() {
		*parts = append(*parts, "Updated "+rdap.UpdatedDate.Format("2006-01-02"))
	}
	if !rdap.ExpirationDate.IsZero() {
		*parts = append(*parts, "Expires "+rdap.ExpirationDate.Format("2006-01-02"))
	}
	if len(rdap.Statuses) > 0 {
		*parts = append(*parts, "RegistrationStatus "+strings.Join(rdap.Statuses, ", "))
	}
}

func buildProjectedEvidenceGroups(asset models.Asset) []EvidenceGroup {
	groups := make([]EvidenceGroup, 0, 4)

	if asset.Type == models.AssetTypeDomain {
		resolution := models.DomainResolutionStatusForAsset(asset)
		dnsItems := make([]string, 0, 8)
		if asset.DomainDetails != nil {
			for _, record := range asset.DomainDetails.Records {
				dnsItems = append(dnsItems, fmt.Sprintf("%s:%s", record.Type, record.Value))
			}
			if asset.DomainDetails.IsCatchAll {
				dnsItems = append(dnsItems, "Catch-all detected")
			}
		}
		if len(dnsItems) == 0 {
			switch resolution {
			case models.DomainResolutionStatusUnresolved:
				dnsItems = append(dnsItems, "Unresolved")
			case models.DomainResolutionStatusLookupFailed:
				dnsItems = append(dnsItems, "Lookup failed")
			}
		}
		if len(dnsItems) > 0 {
			groups = append(groups, EvidenceGroup{
				Title: "DNS",
				Items: dnsItems,
			})
		}

		if asset.DomainDetails != nil {
			if registrationItems := buildProjectedRegistrationItems(asset.DomainDetails.RDAP); len(registrationItems) > 0 {
				groups = append(groups, EvidenceGroup{
					Title: "Registration",
					Items: registrationItems,
				})
			}
		}
	}

	if asset.Type == models.AssetTypeIP {
		networkItems := make([]string, 0, 4)
		if asset.IPDetails != nil {
			if asset.IPDetails.ASN != 0 {
				networkItems = append(networkItems, fmt.Sprintf("ASN: %d", asset.IPDetails.ASN))
			}
			if asset.IPDetails.Organization != "" {
				networkItems = append(networkItems, "Organization: "+asset.IPDetails.Organization)
			}
			if asset.IPDetails.PTR != "" {
				networkItems = append(networkItems, "PTR: "+asset.IPDetails.PTR)
			}
		}
		if asset.EnrichmentData != nil {
			if cidr, ok := asset.EnrichmentData["cidr"].(string); ok && cidr != "" {
				networkItems = append(networkItems, "CIDR: "+cidr)
			}
		}
		if len(networkItems) > 0 {
			groups = append(groups, EvidenceGroup{
				Title: "Network",
				Items: networkItems,
			})
		}
	}

	return groups
}

func buildProjectedRegistrationItems(rdap *models.RDAPData) []string {
	if rdap == nil {
		return nil
	}

	items := make([]string, 0, 9)
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
		items = append(items, "Registrant organization: "+rdap.RegistrantOrg)
	}
	if rdap.RegistrantEmail != "" {
		items = append(items, "Registrant email: "+rdap.RegistrantEmail)
	}
	if !rdap.CreationDate.IsZero() {
		items = append(items, "Created: "+rdap.CreationDate.Format("2006-01-02"))
	}
	if !rdap.UpdatedDate.IsZero() {
		items = append(items, "Updated: "+rdap.UpdatedDate.Format("2006-01-02"))
	}
	if !rdap.ExpirationDate.IsZero() {
		items = append(items, "Expires: "+rdap.ExpirationDate.Format("2006-01-02"))
	}
	if len(rdap.Statuses) > 0 {
		items = append(items, "Status: "+strings.Join(rdap.Statuses, ", "))
	}
	return items
}

func buildTraceLinks(runID string, row AssetRow, rows []AssetRow) []lineage.TraceLink {
	links := make([]lineage.TraceLink, 0, 8)
	seen := map[string]struct{}{row.AssetID: {}}

	appendMatch := func(candidate AssetRow, label, description string) {
		if _, exists := seen[candidate.AssetID]; exists {
			return
		}
		seen[candidate.AssetID] = struct{}{}
		links = append(links, lineage.TraceLink{
			AssetID:     candidate.AssetID,
			Identifier:  candidate.Identifier,
			Label:       label,
			Description: description,
			TracePath:   lineage.BuildTracePath(runID, candidate.AssetID),
		})
	}

	for _, candidate := range rows {
		if len(links) >= 8 {
			break
		}
		if candidate.AssetID == row.AssetID {
			continue
		}
		if row.RegistrableDomain != "" && candidate.RegistrableDomain == row.RegistrableDomain {
			appendMatch(candidate, "Same Registrable Domain", "Shares "+candidate.RegistrableDomain)
		}
	}

	for _, candidate := range rows {
		if len(links) >= 8 {
			break
		}
		if candidate.AssetID == row.AssetID {
			continue
		}
		if hasSharedValue(row.EnumerationID, candidate.EnumerationID) {
			appendMatch(candidate, "Same Enumeration", "Collected in "+candidate.EnumerationID)
		}
	}

	return links
}

func hasSharedValue(left, right string) bool {
	leftValues := splitValues(left)
	rightValues := splitValues(right)
	if len(leftValues) == 0 || len(rightValues) == 0 {
		return false
	}

	seen := make(map[string]struct{}, len(leftValues))
	for _, item := range leftValues {
		seen[item] = struct{}{}
	}
	for _, item := range rightValues {
		if _, exists := seen[item]; exists {
			return true
		}
	}
	return false
}

func splitValues(value string) []string {
	parts := strings.Split(value, ",")
	values := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if _, exists := seen[part]; exists {
			continue
		}
		seen[part] = struct{}{}
		values = append(values, part)
	}
	return values
}
