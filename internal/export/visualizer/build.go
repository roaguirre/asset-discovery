package visualizer

import (
	"fmt"
	"sort"
	"strings"
	"time"

	exportshared "asset-discovery/internal/export/shared"
	"asset-discovery/internal/models"
	"asset-discovery/internal/tracing/lineage"
)

func BuildRun(runID string, createdAt time.Time, downloads Downloads, pCtx *models.PipelineContext) Run {
	enumByID := make(map[string]models.Enumeration, len(pCtx.Enumerations))
	for _, enum := range pCtx.Enumerations {
		enumByID[enum.ID] = enum
	}

	seedByID := make(map[string]models.Seed, len(pCtx.Seeds))
	for _, seed := range pCtx.Seeds {
		seedByID[seed.ID] = seed
	}

	observationsByAssetID := make(map[string][]models.AssetObservation, len(pCtx.Observations))
	for _, observation := range pCtx.Observations {
		if observation.AssetID == "" {
			continue
		}
		observationsByAssetID[observation.AssetID] = append(observationsByAssetID[observation.AssetID], observation)
	}

	relationsByAssetID := make(map[string][]models.AssetRelation, len(pCtx.Relations))
	for _, relation := range pCtx.Relations {
		if relation.FromAssetID != "" {
			relationsByAssetID[relation.FromAssetID] = append(relationsByAssetID[relation.FromAssetID], relation)
		}
		if relation.ToAssetID != "" && relation.ToAssetID != relation.FromAssetID {
			relationsByAssetID[relation.ToAssetID] = append(relationsByAssetID[relation.ToAssetID], relation)
		}
	}

	rows := make([]Row, 0, len(pCtx.Assets))
	tracesByAssetID := make(map[string]lineage.Trace, len(pCtx.Assets))
	for _, asset := range exportshared.SortedAssetsForExport(pCtx.Assets) {
		classified := exportshared.ClassifyAsset(asset)
		contributors := lineage.BuildTraceContributors(asset, enumByID, seedByID)
		allSources, discoverySources, enrichmentSources := lineage.SummarizeObservationSources(observationsByAssetID[asset.ID], asset.Source)
		rowSource := discoverySources
		if strings.TrimSpace(rowSource) == "" {
			rowSource = allSources
		}
		tracePath := lineage.BuildTracePath(runID, asset.ID)
		row := Row{
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
			EnumerationID:     lineage.SummarizeContributorValues(contributors, func(item lineage.TraceContributor) string { return item.EnumerationID }),
			SeedID:            lineage.SummarizeContributorValues(contributors, func(item lineage.TraceContributor) string { return item.SeedID }),
			Status:            lineage.SummarizeTraceStatus(asset, contributors, enumByID),
			DiscoveryDate:     asset.DiscoveryDate,
			Details:           buildDetails(asset),
			EvidenceGroups:    buildEvidenceGroups(asset),
			TracePath:         tracePath,
		}
		if asset.IPDetails != nil {
			row.ASN = asset.IPDetails.ASN
			row.Organization = asset.IPDetails.Organization
			row.PTR = asset.IPDetails.PTR
		}
		rows = append(rows, row)
		tracesByAssetID[asset.ID] = lineage.BuildTrace(
			asset,
			string(classified.DomainKind),
			classified.RegistrableDomain,
			contributors,
			observationsByAssetID[asset.ID],
			relationsByAssetID[asset.ID],
			enumByID,
			seedByID,
		)
	}

	sort.SliceStable(rows, func(i, j int) bool {
		if rowGroup(rows[i]) != rowGroup(rows[j]) {
			return rowGroup(rows[i]) < rowGroup(rows[j])
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

	return Run{
		RunSummary: RunSummary{
			ID:               runID,
			Label:            createdAt.Format("2006-01-02 15:04:05 -0700"),
			CreatedAt:        createdAt,
			AssetCount:       len(pCtx.Assets),
			EnumerationCount: len(pCtx.Enumerations),
			SeedCount:        len(pCtx.Seeds),
			Downloads:        downloads,
		},
		Rows:         rows,
		Traces:       traces,
		JudgeSummary: lineage.BuildJudgeSummary(pCtx.JudgeEvaluations),
	}
}

func rowGroup(row Row) int {
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

func buildDetails(asset models.Asset) string {
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
			appendRDAPDetails(&parts, asset.DomainDetails.RDAP)
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

func appendRDAPDetails(parts *[]string, rdap *models.RDAPData) {
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

func buildEvidenceGroups(asset models.Asset) []EvidenceGroup {
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
			if registrationItems := buildRegistrationItems(asset.DomainDetails.RDAP); len(registrationItems) > 0 {
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

func buildRegistrationItems(rdap *models.RDAPData) []string {
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

func buildTraceLinks(runID string, row Row, rows []Row) []lineage.TraceLink {
	links := make([]lineage.TraceLink, 0, 8)
	seen := map[string]struct{}{row.AssetID: {}}

	appendMatch := func(candidate Row, label, description string) {
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
