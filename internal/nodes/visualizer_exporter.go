package nodes

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"asset-discovery/internal/exportutil"
	"asset-discovery/internal/models"
)

// VisualizerExporter archives run snapshots and writes a self-contained HTML viewer.
type VisualizerExporter struct {
	filepath  string
	runID     string
	downloads models.VisualizerDownloads
	now       func() time.Time
}

func NewVisualizerExporter(filepath, runID string, downloads models.VisualizerDownloads) *VisualizerExporter {
	return &VisualizerExporter{
		filepath:  filepath,
		runID:     runID,
		downloads: downloads,
		now:       time.Now,
	}
}

func (e *VisualizerExporter) Process(ctx context.Context, pCtx *models.PipelineContext) (*models.PipelineContext, error) {
	log.Printf("[Visualizer Exporter] Writing run history to %s...", e.filepath)

	completedAt := e.now()
	markEnumerationsCompleted(pCtx, completedAt)

	storageDir := strings.TrimSuffix(e.filepath, filepath.Ext(e.filepath))
	runsDir := filepath.Join(storageDir, "runs")
	if err := os.MkdirAll(runsDir, 0755); err != nil {
		return pCtx, fmt.Errorf("failed to create visualizer run directory: %w", err)
	}

	snapshotPath := filepath.Join(runsDir, e.runID+".json")
	snapshot := buildVisualizerRun(e.runID, completedAt, e.downloads, pCtx)
	snapshot.DataPath = filepath.ToSlash(mustRelativePath(filepath.Dir(e.filepath), snapshotPath))
	if err := writeJSONFile(snapshotPath, snapshot); err != nil {
		return pCtx, fmt.Errorf("failed to write visualizer snapshot: %w", err)
	}

	manifestPath := filepath.Join(storageDir, "manifest.json")
	manifest, err := readVisualizerManifest(manifestPath)
	if err != nil {
		return pCtx, fmt.Errorf("failed to read visualizer manifest: %w", err)
	}

	manifest = upsertVisualizerRun(manifest, snapshot.VisualizerRunSummary)
	if err := writeJSONFile(manifestPath, manifest); err != nil {
		return pCtx, fmt.Errorf("failed to write visualizer manifest: %w", err)
	}

	runs, err := loadVisualizerRuns(storageDir, manifest)
	if err != nil {
		return pCtx, fmt.Errorf("failed to load visualizer runs: %w", err)
	}

	if err := renderVisualizerHTML(e.filepath, runs); err != nil {
		return pCtx, fmt.Errorf("failed to render visualizer HTML: %w", err)
	}

	return pCtx, nil
}

func markEnumerationsCompleted(pCtx *models.PipelineContext, completedAt time.Time) {
	for i := range pCtx.Enumerations {
		if pCtx.Enumerations[i].StartedAt.IsZero() {
			pCtx.Enumerations[i].StartedAt = pCtx.Enumerations[i].CreatedAt
		}
		pCtx.Enumerations[i].Status = "completed"
		pCtx.Enumerations[i].UpdatedAt = completedAt
		if pCtx.Enumerations[i].EndedAt.IsZero() {
			pCtx.Enumerations[i].EndedAt = completedAt
		}
	}
}

func buildVisualizerRun(runID string, createdAt time.Time, downloads models.VisualizerDownloads, pCtx *models.PipelineContext) models.VisualizerRun {
	enumByID := make(map[string]models.Enumeration, len(pCtx.Enumerations))
	for _, enum := range pCtx.Enumerations {
		enumByID[enum.ID] = enum
	}

	seedByID := make(map[string]models.Seed, len(pCtx.Seeds))
	for _, seed := range pCtx.Seeds {
		seedByID[seed.ID] = seed
	}

	rows := make([]models.VisualizerRow, 0, len(pCtx.Assets))
	tracesByAssetID := make(map[string]models.VisualizerTrace, len(pCtx.Assets))
	for _, asset := range exportutil.SortedAssetsForExport(pCtx.Assets) {
		classified := exportutil.ClassifyAsset(asset)
		contributors := buildVisualizerTraceContributors(asset, enumByID, seedByID)
		tracePath := buildVisualizerTracePath(runID, asset.ID)
		rows = append(rows, models.VisualizerRow{
			AssetID:           asset.ID,
			Identifier:        asset.Identifier,
			AssetType:         string(asset.Type),
			DomainKind:        string(classified.DomainKind),
			RegistrableDomain: classified.RegistrableDomain,
			Source:            asset.Source,
			EnumerationID:     summarizeTraceContributorValues(contributors, func(item models.VisualizerTraceContributor) string { return item.EnumerationID }),
			SeedID:            summarizeTraceContributorValues(contributors, func(item models.VisualizerTraceContributor) string { return item.SeedID }),
			Status:            summarizeTraceStatus(asset, contributors, enumByID),
			DiscoveryDate:     asset.DiscoveryDate,
			Details:           buildVisualizerDetails(asset),
			TracePath:         tracePath,
		})
		tracesByAssetID[asset.ID] = buildVisualizerTrace(asset, classified, contributors, enumByID, seedByID)
	}

	sort.SliceStable(rows, func(i, j int) bool {
		if visualizerRowGroup(rows[i]) != visualizerRowGroup(rows[j]) {
			return visualizerRowGroup(rows[i]) < visualizerRowGroup(rows[j])
		}
		if rows[i].RegistrableDomain != rows[j].RegistrableDomain {
			return rows[i].RegistrableDomain < rows[j].RegistrableDomain
		}
		if rows[i].DiscoveryDate.Equal(rows[j].DiscoveryDate) {
			return rows[i].Identifier < rows[j].Identifier
		}
		return rows[i].DiscoveryDate.After(rows[j].DiscoveryDate)
	})

	traces := make([]models.VisualizerTrace, 0, len(rows))
	for _, row := range rows {
		trace, ok := tracesByAssetID[row.AssetID]
		if !ok {
			continue
		}
		trace.Related = buildVisualizerTraceLinks(runID, row, rows)
		traces = append(traces, trace)
	}

	return models.VisualizerRun{
		VisualizerRunSummary: models.VisualizerRunSummary{
			ID:               runID,
			Label:            createdAt.Format("2006-01-02 15:04:05 -0700"),
			CreatedAt:        createdAt,
			AssetCount:       len(pCtx.Assets),
			EnumerationCount: len(pCtx.Enumerations),
			SeedCount:        len(pCtx.Seeds),
			Downloads:        downloads,
		},
		Rows:   rows,
		Traces: traces,
	}
}

func visualizerRowGroup(row models.VisualizerRow) int {
	const (
		groupRegistrableDomain = iota
		groupSubdomain
		groupIP
	)

	if row.AssetType == string(models.AssetTypeDomain) {
		switch row.DomainKind {
		case string(models.DomainKindRegistrable):
			return groupRegistrableDomain
		case string(models.DomainKindSubdomain):
			return groupSubdomain
		}
	}

	return groupIP
}

func buildVisualizerDetails(asset models.Asset) string {
	parts := make([]string, 0, 8)

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
			if asset.DomainDetails.RDAP.RegistrarName != "" {
				parts = append(parts, "Registrar "+asset.DomainDetails.RDAP.RegistrarName)
			}
			if asset.DomainDetails.RDAP.RegistrantOrg != "" {
				parts = append(parts, "Registrant "+asset.DomainDetails.RDAP.RegistrantOrg)
			}
			if len(asset.DomainDetails.RDAP.NameServers) > 0 {
				parts = append(parts, "NS "+strings.Join(asset.DomainDetails.RDAP.NameServers, ", "))
			}
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

func buildVisualizerTracePath(runID, assetID string) string {
	return "#trace/" + runID + "/" + assetID
}

func buildVisualizerTrace(asset models.Asset, classified exportutil.ClassifiedAsset, contributors []models.VisualizerTraceContributor, enumByID map[string]models.Enumeration, seedByID map[string]models.Seed) models.VisualizerTrace {
	trace := models.VisualizerTrace{
		AssetID:           asset.ID,
		Identifier:        asset.Identifier,
		AssetType:         string(asset.Type),
		Source:            asset.Source,
		EnumerationID:     summarizeTraceContributorValues(contributors, func(item models.VisualizerTraceContributor) string { return item.EnumerationID }),
		SeedID:            summarizeTraceContributorValues(contributors, func(item models.VisualizerTraceContributor) string { return item.SeedID }),
		DomainKind:        string(classified.DomainKind),
		RegistrableDomain: classified.RegistrableDomain,
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
		identityItems = append(identityItems, "Discovered at: "+exportutil.FormatDateTime(asset.DiscoveryDate))
	}
	if classified.DomainKind != "" {
		identityItems = append(identityItems, "Domain kind: "+formatTraceLabel(string(classified.DomainKind)))
	}
	if classified.RegistrableDomain != "" {
		identityItems = append(identityItems, "Registrable domain: "+classified.RegistrableDomain)
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
			enumItems = append(enumItems, "Created at: "+exportutil.FormatDateTime(enum.CreatedAt))
		}
		if !enum.StartedAt.IsZero() {
			enumItems = append(enumItems, "Started at: "+exportutil.FormatDateTime(enum.StartedAt))
		}
		if !enum.EndedAt.IsZero() {
			enumItems = append(enumItems, "Ended at: "+exportutil.FormatDateTime(enum.EndedAt))
		}
		trace.Sections = appendTraceSection(trace.Sections, "Enumeration", enumItems)
	}

	trace.Sections = appendTraceSection(trace.Sections, "Domain Evidence", buildDomainTraceItems(asset))
	trace.Sections = appendTraceSection(trace.Sections, "Network Evidence", buildIPTraceItems(asset))
	trace.Sections = appendTraceSection(trace.Sections, "Enrichment", buildEnrichmentTraceItems(asset.EnrichmentData))

	return trace
}

func buildVisualizerTraceContributors(asset models.Asset, enumByID map[string]models.Enumeration, seedByID map[string]models.Seed) []models.VisualizerTraceContributor {
	provenance := append([]models.AssetProvenance(nil), asset.Provenance...)
	if len(provenance) == 0 {
		provenance = append(provenance, models.AssetProvenance{
			AssetID:       asset.ID,
			EnumerationID: asset.EnumerationID,
			Source:        asset.Source,
			DiscoveryDate: asset.DiscoveryDate,
		})
	}

	contributors := make([]models.VisualizerTraceContributor, 0, len(provenance))
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

		contributors = append(contributors, models.VisualizerTraceContributor{
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

func buildContributorTraceItems(contributors []models.VisualizerTraceContributor) []string {
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
			parts = append(parts, "discovered "+exportutil.FormatDateTime(contributor.DiscoveryDate))
		}
		if len(parts) == 0 {
			continue
		}
		items = append(items, strings.Join(parts, " | "))
	}
	return items
}

func summarizeTraceContributorValues(contributors []models.VisualizerTraceContributor, value func(models.VisualizerTraceContributor) string) string {
	values := uniqueTraceContributorValues(contributors, value)
	return strings.Join(values, ", ")
}

func uniqueTraceContributorValues(contributors []models.VisualizerTraceContributor, value func(models.VisualizerTraceContributor) string) []string {
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

func summarizeTraceStatus(asset models.Asset, contributors []models.VisualizerTraceContributor, enumByID map[string]models.Enumeration) string {
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

func buildVisualizerTraceLinks(runID string, row models.VisualizerRow, rows []models.VisualizerRow) []models.VisualizerTraceLink {
	links := make([]models.VisualizerTraceLink, 0, 8)
	seen := map[string]struct{}{row.AssetID: {}}

	appendMatch := func(candidate models.VisualizerRow, label, description string) {
		if _, exists := seen[candidate.AssetID]; exists {
			return
		}
		seen[candidate.AssetID] = struct{}{}
		links = append(links, models.VisualizerTraceLink{
			AssetID:     candidate.AssetID,
			Identifier:  candidate.Identifier,
			Label:       label,
			Description: description,
			TracePath:   buildVisualizerTracePath(runID, candidate.AssetID),
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
		if visualizerHasSharedValue(row.EnumerationID, candidate.EnumerationID) {
			appendMatch(candidate, "Same Enumeration", "Collected in "+candidate.EnumerationID)
		}
	}

	return links
}

func visualizerHasSharedValue(left, right string) bool {
	leftValues := splitVisualizerValues(left)
	rightValues := splitVisualizerValues(right)
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

func splitVisualizerValues(value string) []string {
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

func appendTraceSection(sections []models.VisualizerTraceSection, title string, items []string) []models.VisualizerTraceSection {
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

	return append(sections, models.VisualizerTraceSection{
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

func readVisualizerManifest(path string) (models.VisualizerManifest, error) {
	var manifest models.VisualizerManifest

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return manifest, nil
		}
		return manifest, err
	}

	if err := json.Unmarshal(data, &manifest); err != nil {
		return manifest, err
	}

	return manifest, nil
}

func upsertVisualizerRun(manifest models.VisualizerManifest, summary models.VisualizerRunSummary) models.VisualizerManifest {
	replaced := false
	for i := range manifest.Runs {
		if manifest.Runs[i].ID == summary.ID {
			manifest.Runs[i] = summary
			replaced = true
			break
		}
	}

	if !replaced {
		manifest.Runs = append(manifest.Runs, summary)
	}

	sort.SliceStable(manifest.Runs, func(i, j int) bool {
		return manifest.Runs[i].CreatedAt.After(manifest.Runs[j].CreatedAt)
	})

	return manifest
}

func loadVisualizerRuns(storageDir string, manifest models.VisualizerManifest) ([]models.VisualizerRun, error) {
	runs := make([]models.VisualizerRun, 0, len(manifest.Runs))

	for _, summary := range manifest.Runs {
		path := filepath.Join(filepath.Dir(storageDir), filepath.FromSlash(summary.DataPath))
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}

		var run models.VisualizerRun
		if err := json.Unmarshal(data, &run); err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}

	sort.SliceStable(runs, func(i, j int) bool {
		return runs[i].CreatedAt.After(runs[j].CreatedAt)
	})

	return runs, nil
}

func renderVisualizerHTML(path string, runs []models.VisualizerRun) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}

	runsJSON, err := json.Marshal(runs)
	if err != nil {
		return err
	}

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	data := struct {
		GeneratedAt string
		RunsJSON    template.JS
	}{
		GeneratedAt: time.Now().Format("2006-01-02 15:04:05 -0700"),
		RunsJSON:    template.JS(string(runsJSON)),
	}

	return visualizerTemplate.Execute(f, data)
}

func writeJSONFile(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	encoder := json.NewEncoder(f)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func mustRelativePath(fromDir, to string) string {
	rel, err := filepath.Rel(fromDir, to)
	if err != nil {
		return filepath.ToSlash(to)
	}

	return rel
}

var visualizerTemplate = template.Must(template.New("visualizer").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Asset Discovery Visualizer</title>
  <style>
    :root {
      color-scheme: light;
      --bg: #f6f3ec;
      --panel: rgba(255, 252, 245, 0.94);
      --panel-strong: #fffdf7;
      --ink: #1b1713;
      --muted: #6e6258;
      --line: rgba(80, 61, 44, 0.16);
      --accent: #be6a15;
      --accent-strong: #7e3b00;
      --accent-soft: rgba(190, 106, 21, 0.12);
      --shadow: 0 20px 45px rgba(68, 44, 18, 0.08);
      --font-body: "IBM Plex Sans", "Segoe UI", sans-serif;
      --font-heading: "Space Grotesk", "Avenir Next", sans-serif;
    }

    * { box-sizing: border-box; }

    body {
      margin: 0;
      min-height: 100vh;
      font-family: var(--font-body);
      color: var(--ink);
      background:
        radial-gradient(circle at top left, rgba(216, 170, 112, 0.3), transparent 28rem),
        radial-gradient(circle at top right, rgba(117, 156, 130, 0.18), transparent 26rem),
        linear-gradient(180deg, #faf6ee 0%, #f2ebe1 55%, #ebe1d2 100%);
    }

    main {
      width: min(1380px, calc(100vw - 2rem));
      margin: 0 auto;
      padding: 2.5rem 0 3rem;
    }

    .hero, .controls, .summary, .table-shell {
      background: var(--panel);
      border: 1px solid var(--line);
      border-radius: 22px;
      box-shadow: var(--shadow);
      backdrop-filter: blur(10px);
    }

    .hero {
      padding: 2rem;
      margin-bottom: 1.25rem;
      overflow: hidden;
      position: relative;
    }

    .hero::after {
      content: "";
      position: absolute;
      inset: auto -6rem -7rem auto;
      width: 18rem;
      height: 18rem;
      border-radius: 999px;
      background: radial-gradient(circle, rgba(190, 106, 21, 0.26), transparent 70%);
      pointer-events: none;
    }

    .eyebrow {
      display: inline-flex;
      align-items: center;
      gap: 0.5rem;
      padding: 0.35rem 0.75rem;
      border-radius: 999px;
      background: var(--accent-soft);
      color: var(--accent-strong);
      font-size: 0.78rem;
      font-weight: 700;
      letter-spacing: 0.08em;
      text-transform: uppercase;
    }

    h1 {
      margin: 1rem 0 0.35rem;
      font-family: var(--font-heading);
      font-size: clamp(2rem, 3.6vw, 3.25rem);
      line-height: 1;
    }

    .hero p {
      max-width: 58rem;
      margin: 0;
      color: var(--muted);
      font-size: 1rem;
      line-height: 1.6;
    }

    .controls {
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
      gap: 1rem;
      padding: 1rem;
      margin-bottom: 1.25rem;
      position: relative;
      z-index: 10;
    }

    .field {
      display: flex;
      flex-direction: column;
      gap: 0.45rem;
    }

    .field label {
      font-size: 0.82rem;
      font-weight: 700;
      letter-spacing: 0.05em;
      text-transform: uppercase;
      color: var(--muted);
    }

    .field input,
    .field select,
    .multi-select-trigger {
      width: 100%;
      border: 1px solid rgba(126, 59, 0, 0.14);
      border-radius: 14px;
      padding: 0.85rem 0.95rem;
      font: inherit;
      color: var(--ink);
      background: var(--panel-strong);
    }

    .field input:focus,
    .field select:focus,
    .multi-select-trigger:focus,
    .multi-select.is-open .multi-select-trigger {
      outline: 2px solid rgba(190, 106, 21, 0.22);
      border-color: rgba(190, 106, 21, 0.35);
    }

    .multi-select {
      position: relative;
    }

    .multi-select.is-open {
      z-index: 30;
    }

    .multi-select-trigger {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 0.75rem;
      cursor: pointer;
      text-align: left;
    }

    .multi-select-trigger::after {
      content: "▾";
      flex: none;
      color: var(--muted);
      font-size: 0.92rem;
    }

    .multi-select-menu {
      position: absolute;
      top: calc(100% + 0.45rem);
      left: 0;
      right: 0;
      z-index: 40;
      max-height: min(20rem, calc(100vh - 14rem));
      overflow: auto;
      padding: 0.45rem;
      border: 1px solid rgba(126, 59, 0, 0.18);
      border-radius: 16px;
      background: rgba(255, 252, 245, 0.98);
      box-shadow: 0 18px 36px rgba(68, 44, 18, 0.14);
    }

    .multi-select-options {
      display: grid;
      gap: 0.2rem;
      max-height: 15rem;
      overflow: auto;
      padding-top: 0.2rem;
      border-top: 1px solid rgba(80, 61, 44, 0.08);
    }

    .multi-select-option {
      display: flex;
      align-items: center;
      gap: 0.65rem;
      padding: 0.55rem 0.6rem;
      border-radius: 12px;
      cursor: pointer;
      user-select: none;
    }

    .multi-select-option:hover {
      background: rgba(190, 106, 21, 0.08);
    }

    .multi-select-option input {
      width: 1rem;
      height: 1rem;
      margin: 0;
      accent-color: var(--accent);
    }

    .multi-select-option-all {
      font-weight: 700;
    }

    .summary {
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(170px, 1fr));
      gap: 0.85rem;
      padding: 1rem;
      margin-bottom: 1.25rem;
      position: relative;
      z-index: 1;
    }

    .metric {
      padding: 1rem;
      border-radius: 18px;
      background: linear-gradient(180deg, rgba(255, 248, 235, 0.92), rgba(255, 255, 255, 0.72));
      border: 1px solid rgba(126, 59, 0, 0.08);
    }

    .metric span {
      display: block;
      color: var(--muted);
      font-size: 0.78rem;
      font-weight: 700;
      letter-spacing: 0.06em;
      text-transform: uppercase;
      margin-bottom: 0.45rem;
    }

    .metric strong {
      display: block;
      font-family: var(--font-heading);
      font-size: 1.8rem;
      line-height: 1;
    }

    .table-shell {
      padding: 1rem;
      position: relative;
      z-index: 1;
    }

    .table-meta {
      display: grid;
      gap: 0.75rem;
      margin-bottom: 0.85rem;
    }

    .table-toolbar {
      display: flex;
      flex-wrap: wrap;
      align-items: center;
      justify-content: space-between;
      gap: 0.75rem;
    }

    #download-links {
      display: flex;
      flex-wrap: wrap;
      gap: 0.55rem;
    }

    #download-links a {
      color: var(--accent-strong);
      text-decoration: none;
      padding: 0.45rem 0.8rem;
      border-radius: 999px;
      background: var(--accent-soft);
      font-size: 0.9rem;
      font-weight: 700;
    }

    .view-toggle {
      display: inline-flex;
      align-items: center;
      gap: 0.35rem;
      padding: 0.25rem;
      border-radius: 999px;
      background: rgba(126, 59, 0, 0.08);
    }

    .view-toggle button {
      border: 0;
      border-radius: 999px;
      padding: 0.5rem 0.9rem;
      background: transparent;
      color: var(--muted);
      font: inherit;
      font-weight: 700;
      cursor: pointer;
    }

    .view-toggle button.is-active {
      background: var(--panel-strong);
      color: var(--accent-strong);
      box-shadow: inset 0 0 0 1px rgba(126, 59, 0, 0.12);
    }

    .table-wrap {
      overflow: auto;
      border-radius: 18px;
      border: 1px solid var(--line);
      background: rgba(255, 255, 255, 0.82);
    }

    table {
      width: 100%;
      border-collapse: collapse;
      min-width: 980px;
    }

    thead th {
      position: sticky;
      top: 0;
      z-index: 1;
      padding: 0;
      background: rgba(255, 248, 235, 0.96);
      border-bottom: 1px solid var(--line);
      text-align: left;
    }

    thead button {
      width: 100%;
      border: 0;
      background: transparent;
      padding: 0.9rem 1rem;
      text-align: left;
      font: inherit;
      font-weight: 700;
      color: var(--ink);
      cursor: pointer;
    }

    tbody td {
      padding: 0.9rem 1rem;
      border-bottom: 1px solid rgba(80, 61, 44, 0.08);
      vertical-align: top;
      line-height: 1.45;
    }

    tbody tr:nth-child(even) {
      background: rgba(249, 244, 235, 0.6);
    }

    .pill {
      display: inline-flex;
      align-items: center;
      padding: 0.22rem 0.55rem;
      border-radius: 999px;
      background: rgba(117, 156, 130, 0.14);
      color: #365644;
      font-size: 0.78rem;
      font-weight: 700;
      text-transform: capitalize;
    }

    .source-list {
      display: flex;
      flex-wrap: wrap;
      gap: 0.35rem;
    }

    .source-pill {
      background: rgba(190, 106, 21, 0.12);
      color: var(--accent-strong);
      text-transform: none;
      cursor: help;
    }

    [data-tooltip] {
      position: relative;
    }

    .app-tooltip {
      position: fixed;
      left: 0;
      top: 0;
      z-index: 9999;
      max-width: min(26rem, calc(100vw - 1.5rem));
      padding: 0.65rem 0.8rem;
      border-radius: 14px;
      border: 1px solid rgba(126, 59, 0, 0.14);
      background: rgba(27, 23, 19, 0.96);
      color: #fff8ef;
      box-shadow: 0 18px 40px rgba(27, 23, 19, 0.28);
      font-size: 0.82rem;
      line-height: 1.45;
      pointer-events: none;
      opacity: 0;
      transform: translate3d(0, -0.2rem, 0);
      transition: opacity 120ms ease, transform 120ms ease;
    }

    .app-tooltip[data-visible="true"] {
      opacity: 1;
      transform: translate3d(0, 0, 0);
    }

    .result-trace-link,
    .trace-related-link,
    .trace-back-button {
      display: inline-flex;
      align-items: center;
      justify-content: center;
      gap: 0.35rem;
      border-radius: 999px;
      padding: 0.45rem 0.8rem;
      text-decoration: none;
      font-size: 0.86rem;
      font-weight: 700;
    }

    .result-trace-link,
    .trace-related-link {
      color: var(--accent-strong);
      background: var(--accent-soft);
    }

    .trace-back-button {
      border: 0;
      background: rgba(117, 156, 130, 0.14);
      color: #365644;
      cursor: pointer;
    }

    .trace-view {
      display: grid;
      gap: 1rem;
    }

    .trace-header {
      display: flex;
      flex-wrap: wrap;
      align-items: flex-start;
      justify-content: space-between;
      gap: 1rem;
      padding: 1rem;
      border-radius: 18px;
      border: 1px solid rgba(126, 59, 0, 0.08);
      background: linear-gradient(180deg, rgba(255, 248, 235, 0.92), rgba(255, 255, 255, 0.78));
    }

    .trace-header h2 {
      margin: 0.35rem 0 0.25rem;
      font-family: var(--font-heading);
      font-size: clamp(1.4rem, 2vw, 2rem);
      line-height: 1.1;
    }

    .trace-header p {
      margin: 0;
      color: var(--muted);
      line-height: 1.55;
    }

    .trace-summary {
      display: flex;
      flex-wrap: wrap;
      gap: 0.45rem;
    }

    .trace-summary .pill {
      background: rgba(126, 59, 0, 0.08);
      color: var(--accent-strong);
      text-transform: none;
    }

    .trace-grid {
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(250px, 1fr));
      gap: 0.85rem;
    }

    .trace-card {
      padding: 1rem;
      border-radius: 18px;
      border: 1px solid rgba(126, 59, 0, 0.08);
      background: rgba(255, 255, 255, 0.82);
    }

    .trace-card h3,
    .trace-related-shell h3 {
      margin: 0 0 0.75rem;
      font-family: var(--font-heading);
      font-size: 1rem;
    }

    .trace-items {
      margin: 0;
      padding-left: 1rem;
      color: var(--ink);
    }

    .trace-items li + li {
      margin-top: 0.45rem;
    }

    .trace-related-shell {
      padding: 1rem;
      border-radius: 18px;
      border: 1px solid rgba(126, 59, 0, 0.08);
      background: rgba(255, 255, 255, 0.82);
    }

    .trace-related-list {
      display: grid;
      gap: 0.65rem;
    }

    .trace-related-item {
      display: flex;
      flex-wrap: wrap;
      align-items: center;
      justify-content: space-between;
      gap: 0.75rem;
      padding: 0.8rem 0.9rem;
      border-radius: 14px;
      background: rgba(249, 244, 235, 0.72);
      border: 1px solid rgba(80, 61, 44, 0.08);
    }

    .trace-related-copy strong {
      display: block;
      margin-bottom: 0.2rem;
    }

    .muted {
      color: var(--muted);
    }

    #empty-state {
      display: none;
      padding: 1.25rem 0 0.25rem;
      color: var(--muted);
    }

    @media (max-width: 720px) {
      main {
        width: min(100vw - 1rem, 1380px);
        padding-top: 1rem;
      }

      .hero, .controls, .summary, .table-shell {
        border-radius: 18px;
      }

      .hero {
        padding: 1.35rem;
      }
    }
  </style>
</head>
<body>
  <div class="app-tooltip" id="app-tooltip" role="tooltip" aria-hidden="true"></div>
  <main>
    <section class="hero">
      <div class="eyebrow">Enumeration Results</div>
      <h1>Asset Discovery Visualizer</h1>
      <p>All archived discovery runs in one place. Choose a run, filter the table, and sort any column. The selector defaults to the latest exported run.</p>
      <p class="muted">Generated at {{.GeneratedAt}}</p>
    </section>

    <section class="controls">
      <div class="field">
        <label for="run-select">Run</label>
        <select id="run-select"></select>
      </div>
      <div class="field">
        <label for="search-input">Search</label>
        <input id="search-input" type="search" placeholder="Filter identifier, registrable domain, details, source, seed, or enumeration">
      </div>
      <div class="field">
        <label for="type-filter">Asset Type</label>
        <select id="type-filter">
          <option value="">All asset types</option>
        </select>
      </div>
      <div class="field">
        <label for="domain-kind-filter">Domain Kind</label>
        <select id="domain-kind-filter">
          <option value="">All domain kinds</option>
        </select>
      </div>
      <div class="field">
        <label for="source-filter-trigger">Source</label>
        <div class="multi-select" id="source-filter">
          <button type="button" class="multi-select-trigger" id="source-filter-trigger" aria-haspopup="true" aria-expanded="false">
            All sources
          </button>
          <div class="multi-select-menu" id="source-filter-menu" hidden>
            <label class="multi-select-option multi-select-option-all" data-tooltip="Show assets from every source.">
              <input type="checkbox" data-role="all" checked>
              <span>All sources</span>
            </label>
            <div class="multi-select-options" id="source-filter-options"></div>
          </div>
        </div>
      </div>
    </section>

    <section class="summary">
      <article class="metric">
        <span>Archived Runs</span>
        <strong id="run-count">0</strong>
      </article>
      <article class="metric">
        <span>Selected Run</span>
        <strong id="selected-run">Latest</strong>
      </article>
      <article class="metric">
        <span>Assets</span>
        <strong id="asset-count">0</strong>
      </article>
      <article class="metric">
        <span>Enumerations</span>
        <strong id="enumeration-count">0</strong>
      </article>
      <article class="metric">
        <span>Seeds</span>
        <strong id="seed-count">0</strong>
      </article>
      <article class="metric">
        <span>Visible Rows</span>
        <strong id="visible-count">0</strong>
      </article>
    </section>

    <section class="table-shell">
      <div class="table-meta">
        <div class="table-toolbar">
          <div class="muted" id="table-caption">No archived runs loaded.</div>
          <div class="view-toggle" role="tablist" aria-label="Visualizer views">
            <button type="button" id="results-view-button" class="is-active">Results</button>
            <button type="button" id="trace-view-button">Trace</button>
          </div>
        </div>
        <div id="download-links"></div>
      </div>
      <div class="table-wrap" id="results-view">
        <table>
          <thead>
            <tr>
              <th><button type="button" data-key="identifier">Identifier</button></th>
              <th><button type="button" data-key="domain_kind">Domain Kind</button></th>
              <th><button type="button" data-key="registrable_domain">Registrable Domain</button></th>
              <th><button type="button" data-key="asset_type">Type</button></th>
              <th><button type="button" data-key="source">Source</button></th>
              <th><button type="button" data-key="enumeration_id">Enumeration</button></th>
              <th><button type="button" data-key="seed_id">Seed</button></th>
              <th><button type="button" data-key="status">Status</button></th>
              <th><button type="button" data-key="discovery_date">Discovered</button></th>
              <th><button type="button" data-key="details">Details</button></th>
              <th>Trace</th>
            </tr>
          </thead>
          <tbody id="results-body"></tbody>
        </table>
      </div>
      <p id="empty-state">No rows match the active filters.</p>
      <section class="trace-view" id="trace-view" hidden>
        <div class="trace-header">
          <div>
            <div class="eyebrow">Result Trace</div>
            <h2 id="trace-title">Select a result</h2>
            <p id="trace-subtitle">Choose any result row to inspect its exported provenance, context, and related results.</p>
          </div>
          <button type="button" class="trace-back-button" id="trace-back-button">Back To Results</button>
        </div>
        <div class="trace-summary" id="trace-summary"></div>
        <div class="trace-grid" id="trace-sections"></div>
        <div class="trace-related-shell">
          <h3>Related Results</h3>
          <div id="trace-related"></div>
        </div>
      </section>
    </section>
  </main>

  <script>
    const runs = {{.RunsJSON}};
    const initialHash = parseHash();
    const state = {
      runId: initialHash.runId || (runs[0] ? runs[0].id : ""),
      search: "",
      type: "",
      domainKind: "",
      sources: [],
      view: initialHash.view,
      traceAssetId: initialHash.assetId,
      sortKey: "discovery_date",
      sortDirection: "desc"
    };

    const runSelect = document.getElementById("run-select");
    const searchInput = document.getElementById("search-input");
    const typeFilter = document.getElementById("type-filter");
    const domainKindFilter = document.getElementById("domain-kind-filter");
    const sourceFilter = document.getElementById("source-filter");
    const sourceFilterTrigger = document.getElementById("source-filter-trigger");
    const sourceFilterMenu = document.getElementById("source-filter-menu");
    const sourceFilterOptions = document.getElementById("source-filter-options");
    const body = document.getElementById("results-body");
    const resultsView = document.getElementById("results-view");
    const traceView = document.getElementById("trace-view");
    const resultsViewButton = document.getElementById("results-view-button");
    const traceViewButton = document.getElementById("trace-view-button");
    const traceBackButton = document.getElementById("trace-back-button");
    const traceTitle = document.getElementById("trace-title");
    const traceSubtitle = document.getElementById("trace-subtitle");
    const traceSummary = document.getElementById("trace-summary");
    const traceSections = document.getElementById("trace-sections");
    const traceRelated = document.getElementById("trace-related");
    const emptyState = document.getElementById("empty-state");
    const downloadLinks = document.getElementById("download-links");
    const tableCaption = document.getElementById("table-caption");
    const appTooltip = document.getElementById("app-tooltip");

    document.getElementById("run-count").textContent = String(runs.length);

    const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: "base" });
    const sourceDescriptions = Object.freeze({
      "crt.sh": "Certificate Transparency results from crt.sh, used to surface domains seen in public TLS certificates.",
      "dns_collector": "Direct DNS lookups for the target domain, including A, AAAA, MX, TXT, and NS records.",
      "hackertarget_collector": "Passive subdomain and host search results returned by HackerTarget.",
      "rdap_collector": "Registration data from RDAP, including registrar, registrant, and nameserver metadata.",
      "wayback_collector": "Historical hostnames recovered from the Internet Archive Wayback Machine CDX index.",
      "alienvault_collector": "Passive DNS observations from AlienVault OTX.",
      "web_hint_collector": "Ownership hints mined from the target website and security.txt references.",
      "reverse_registration_collector": "Candidate sibling domains discovered through certificate transparency and RDAP overlap.",
      "asn_cidr_collector": "PTR-derived domains and roots discovered by pivoting through known ASN and CIDR network ranges."
    });

    function parseHash() {
      const value = String(window.location.hash || "").replace(/^#/, "");
      if (!value.startsWith("trace/")) {
        return { view: "results", runId: "", assetId: "" };
      }

      const parts = value.split("/");
      if (parts.length < 3) {
        return { view: "results", runId: "", assetId: "" };
      }

      return {
        view: "trace",
        runId: decodeURIComponent(parts[1] || ""),
        assetId: decodeURIComponent(parts.slice(2).join("/"))
      };
    }

    function syncHash() {
      const url = new URL(window.location.href);
      if (state.view === "trace" && state.runId && state.traceAssetId) {
        url.hash = "trace/" + encodeURIComponent(state.runId) + "/" + encodeURIComponent(state.traceAssetId);
      } else {
        url.hash = "";
      }
      window.history.replaceState(null, "", url);
    }

    function currentRun() {
      return runs.find((run) => run.id === state.runId) || runs[0] || null;
    }

    function currentTrace(run) {
      if (!run || !Array.isArray(run.traces)) {
        return null;
      }
      return run.traces.find((trace) => trace.asset_id === state.traceAssetId) || null;
    }

    function fillRunSelect() {
      const selectedRun = currentRun();
      state.runId = selectedRun ? selectedRun.id : "";
      runSelect.innerHTML = "";
      runs.forEach((run) => {
        const option = document.createElement("option");
        option.value = run.id;
        option.textContent = run.label + " (" + run.asset_count + " assets)";
        runSelect.appendChild(option);
      });

      runSelect.value = state.runId;
    }

    function uniqueValues(rows, key) {
      return [...new Set(rows.map((row) => row[key]).filter(Boolean))].sort((a, b) => collator.compare(a, b));
    }

    function splitSources(value) {
      const seen = new Set();
      return String(value || "")
        .split(",")
        .map((part) => part.trim())
        .filter((part) => {
          if (!part || seen.has(part)) {
            return false;
          }
          seen.add(part);
          return true;
        });
    }

    function uniqueSourceValues(rows) {
      const values = new Set();
      rows.forEach((row) => {
        splitSources(row.source).forEach((source) => values.add(source));
      });
      return [...values].sort((a, b) => collator.compare(a, b));
    }

    function refillFilter(select, values, placeholder, activeValue) {
      select.innerHTML = "";
      const all = document.createElement("option");
      all.value = "";
      all.textContent = placeholder;
      select.appendChild(all);

      values.forEach((value) => {
        const option = document.createElement("option");
        option.value = value;
        option.textContent = value;
        select.appendChild(option);
      });

      if (values.includes(activeValue)) {
        select.value = activeValue;
      } else {
        select.value = "";
      }
    }

    function sourceFilterLabel() {
      if (state.sources.length === 0) {
        return "All sources";
      }
      if (state.sources.length === 1) {
        return state.sources[0];
      }
      return String(state.sources.length) + " sources selected";
    }

    function syncSourceFilterUI() {
      const selected = new Set(state.sources);
      sourceFilterOptions.querySelectorAll("input[type=\"checkbox\"]").forEach((input) => {
        input.checked = selected.has(input.value);
      });

      const allToggle = sourceFilterMenu.querySelector("input[data-role=\"all\"]");
      if (allToggle) {
        allToggle.checked = state.sources.length === 0;
      }

      sourceFilterTrigger.textContent = sourceFilterLabel();
      sourceFilterTrigger.setAttribute("aria-expanded", sourceFilterMenu.hidden ? "false" : "true");
    }

    function hideTooltip() {
      appTooltip.dataset.visible = "false";
      appTooltip.setAttribute("aria-hidden", "true");
      appTooltip.textContent = "";
    }

    function showTooltip(target, text) {
      if (!target || !text) {
        hideTooltip();
        return;
      }

      appTooltip.textContent = text;
      appTooltip.dataset.visible = "true";
      appTooltip.setAttribute("aria-hidden", "false");

      const rect = target.getBoundingClientRect();
      const tooltipRect = appTooltip.getBoundingClientRect();
      const gap = 12;
      const maxLeft = Math.max(gap, window.innerWidth - tooltipRect.width - gap);
      const desiredLeft = rect.left + (rect.width / 2) - (tooltipRect.width / 2);
      const left = Math.min(maxLeft, Math.max(gap, desiredLeft));

      let top = rect.bottom + gap;
      if (top + tooltipRect.height > window.innerHeight - gap) {
        top = rect.top - tooltipRect.height - gap;
      }
      if (top < gap) {
        top = gap;
      }

      appTooltip.style.left = Math.round(left) + "px";
      appTooltip.style.top = Math.round(top) + "px";
    }

    function tooltipTarget(event) {
      const target = event.target.closest("[data-tooltip]");
      if (!target || !document.body.contains(target)) {
        return null;
      }
      return target;
    }

    function refillSourceFilter(rows) {
      const values = uniqueSourceValues(rows);
      const active = new Set(state.sources.filter((source) => values.includes(source)));
      state.sources = values.filter((source) => active.has(source));

      sourceFilterOptions.innerHTML = "";
      values.forEach((value) => {
        const label = document.createElement("label");
        label.className = "multi-select-option";
        label.dataset.tooltip = describeSource(value);

        const input = document.createElement("input");
        input.type = "checkbox";
        input.value = value;

        const text = document.createElement("span");
        text.textContent = value;

        label.appendChild(input);
        label.appendChild(text);
        sourceFilterOptions.appendChild(label);
      });

      syncSourceFilterUI();
    }

    function normalize(value) {
      return String(value || "").toLowerCase();
    }

    function describeSource(value) {
      return sourceDescriptions[value] || ("Collected from " + String(value || "an unknown source") + ".");
    }

    function formatDomainKind(value) {
      return String(value || "")
        .replaceAll("_", " ")
        .replace(/\b\w/g, (char) => char.toUpperCase());
    }

    function renderSourceCell(value) {
      const sources = splitSources(value);
      if (sources.length === 0) {
        return "<span class=\"muted\">-</span>";
      }
      return "<div class=\"source-list\">" + sources.map((source) => {
        return "<span class=\"pill source-pill\" data-tooltip=\"" + escapeHTML(describeSource(source)) + "\">" + escapeHTML(source) + "</span>";
      }).join("") + "</div>";
    }

    function renderTraceSummary(trace) {
      const pills = [];
      const contributors = Array.isArray(trace.contributors) ? trace.contributors : [];
      const uniqueContributorValues = (key) => {
        const seen = new Set();
        return contributors
          .map((item) => String(item && item[key] || "").trim())
          .filter((value) => {
            if (!value || seen.has(value)) {
              return false;
            }
            seen.add(value);
            return true;
          });
      };
      if (trace.asset_type) {
        pills.push("<span class=\"pill\">" + escapeHTML(trace.asset_type) + "</span>");
      }
      if (trace.domain_kind) {
        pills.push("<span class=\"pill\">" + escapeHTML(formatDomainKind(trace.domain_kind)) + "</span>");
      }
      if (trace.registrable_domain) {
        pills.push("<span class=\"pill\">" + escapeHTML(trace.registrable_domain) + "</span>");
      }
      if (trace.source) {
        splitSources(trace.source).forEach((source) => {
          pills.push("<span class=\"pill\">" + escapeHTML(source) + "</span>");
        });
      }
      if (contributors.length > 0) {
        pills.push("<span class=\"pill\">" + escapeHTML(String(contributors.length)) + " contributor" + (contributors.length === 1 ? "" : "s") + "</span>");

        const enumerations = uniqueContributorValues("enumeration_id");
        if (enumerations.length === 1) {
          pills.push("<span class=\"pill\">Enum " + escapeHTML(enumerations[0]) + "</span>");
        } else if (enumerations.length > 1) {
          pills.push("<span class=\"pill\">" + escapeHTML(String(enumerations.length)) + " enumerations</span>");
        }

        const seeds = uniqueContributorValues("seed_id");
        if (seeds.length === 1) {
          pills.push("<span class=\"pill\">Seed " + escapeHTML(seeds[0]) + "</span>");
        } else if (seeds.length > 1) {
          pills.push("<span class=\"pill\">" + escapeHTML(String(seeds.length)) + " seeds</span>");
        }
      } else {
        if (trace.enumeration_id) {
          pills.push("<span class=\"pill\">Enum " + escapeHTML(trace.enumeration_id) + "</span>");
        }
        if (trace.seed_id) {
          pills.push("<span class=\"pill\">Seed " + escapeHTML(trace.seed_id) + "</span>");
        }
      }
      return pills.join("");
    }

    function renderTraceSections(trace) {
      const sections = Array.isArray(trace.sections) ? trace.sections : [];
      if (sections.length === 0) {
        return "<article class=\"trace-card\"><h3>No Trace Sections</h3><p class=\"muted\">This result does not include exported trace details.</p></article>";
      }

      return sections.map((section) => {
        const items = Array.isArray(section.items) ? section.items : [];
        return [
          "<article class=\"trace-card\">",
          "<h3>", escapeHTML(section.title || "Trace"), "</h3>",
          "<ul class=\"trace-items\">",
          items.map((item) => "<li>" + escapeHTML(item) + "</li>").join(""),
          "</ul>",
          "</article>"
        ].join("");
      }).join("");
    }

    function renderTraceRelated(trace) {
      const related = Array.isArray(trace.related) ? trace.related : [];
      if (related.length === 0) {
        return "<p class=\"muted\">No related results were linked for this exported trace.</p>";
      }

      return "<div class=\"trace-related-list\">" + related.map((link) => {
        return [
          "<div class=\"trace-related-item\">",
          "<div class=\"trace-related-copy\">",
          "<strong>", escapeHTML(link.identifier || link.asset_id), "</strong>",
          "<span class=\"muted\">", escapeHTML(link.label || "Related Result"), "</span>",
          link.description ? "<div class=\"muted\">" + escapeHTML(link.description) + "</div>" : "",
          "</div>",
          "<a href=\"", escapeHTML(link.trace_path || "#"), "\" class=\"trace-related-link\" data-trace-link data-run-id=\"", escapeHTML(state.runId), "\" data-asset-id=\"", escapeHTML(link.asset_id), "\">Open Trace</a>",
          "</div>"
        ].join("");
      }).join("") + "</div>";
    }

    function ensureTraceSelection(run, rows) {
      if (state.view !== "trace") {
        return;
      }

      const trace = currentTrace(run);
      if (trace) {
        return;
      }

      const firstTrace = run && Array.isArray(run.traces) ? run.traces[0] : null;
      const firstRow = rows[0] || null;
      if (firstTrace) {
        state.traceAssetId = firstTrace.asset_id;
        return;
      }
      if (firstRow) {
        state.traceAssetId = firstRow.asset_id;
        return;
      }

      state.view = "results";
      state.traceAssetId = "";
    }

    function openTrace(runId, assetId) {
      if (!runId || !assetId) {
        return;
      }

      state.runId = runId;
      state.view = "trace";
      state.traceAssetId = assetId;
      fillRunSelect();
      updateFiltersForRun();
      renderTable();
      syncHash();
    }

    function openTraceFromCurrentSelection() {
      const run = currentRun();
      const rows = visibleRows(run);
      if (!run) {
        return;
      }

      if (state.traceAssetId && currentTrace(run)) {
        state.view = "trace";
      } else if (rows[0]) {
        state.view = "trace";
        state.traceAssetId = rows[0].asset_id;
      } else if (Array.isArray(run.traces) && run.traces[0]) {
        state.view = "trace";
        state.traceAssetId = run.traces[0].asset_id;
      } else {
        state.view = "results";
        state.traceAssetId = "";
      }

      renderTable();
      syncHash();
    }

    function compareRows(left, right) {
      const key = state.sortKey;
      const direction = state.sortDirection === "asc" ? 1 : -1;
      let leftValue = left[key] || "";
      let rightValue = right[key] || "";

      if (key === "discovery_date") {
        leftValue = leftValue ? Date.parse(leftValue) : 0;
        rightValue = rightValue ? Date.parse(rightValue) : 0;
        if (leftValue < rightValue) {
          return -1 * direction;
        }
        if (leftValue > rightValue) {
          return 1 * direction;
        }
        return collator.compare(left.identifier, right.identifier);
      }

      return collator.compare(String(leftValue), String(rightValue)) * direction;
    }

    function visibleRows(run) {
      if (!run) {
        return [];
      }

      return run.rows
        .filter((row) => !state.type || row.asset_type === state.type)
        .filter((row) => !state.domainKind || row.domain_kind === state.domainKind)
        .filter((row) => {
          if (state.sources.length === 0) {
            return true;
          }
          const rowSources = splitSources(row.source);
          return state.sources.every((source) => rowSources.includes(source));
        })
        .filter((row) => {
          if (!state.search) {
            return true;
          }
          return normalize([
            row.identifier,
            row.domain_kind,
            row.registrable_domain,
            row.asset_type,
            row.source,
            row.enumeration_id,
            row.seed_id,
            row.status,
            row.details
          ].join(" ")).includes(state.search);
        })
        .slice()
        .sort(compareRows);
    }

    function renderDownloads(run) {
      downloadLinks.innerHTML = "";
      if (!run || !run.downloads) {
        return;
      }

      [["JSON", run.downloads.json], ["CSV", run.downloads.csv], ["XLSX", run.downloads.xlsx]].forEach(([label, href]) => {
        if (!href) {
          return;
        }
        const link = document.createElement("a");
        link.href = href;
        link.textContent = label;
        downloadLinks.appendChild(link);
      });
    }

    function renderTable() {
      const run = currentRun();
      const rows = visibleRows(run);
      ensureTraceSelection(run, rows);
      const trace = currentTrace(run);
      hideTooltip();
      body.innerHTML = "";

      document.getElementById("selected-run").textContent = run ? run.label : "No runs";
      document.getElementById("asset-count").textContent = String(run ? run.asset_count : 0);
      document.getElementById("enumeration-count").textContent = String(run ? run.enumeration_count : 0);
      document.getElementById("seed-count").textContent = String(run ? run.seed_count : 0);
      document.getElementById("visible-count").textContent = String(rows.length);
      tableCaption.textContent = run ? "Showing " + rows.length + " of " + run.rows.length + " rows from " + run.label + "." : "No archived runs loaded.";

      renderDownloads(run);

      const showTrace = state.view === "trace" && trace;
      resultsView.hidden = showTrace;
      traceView.hidden = !showTrace;
      resultsViewButton.classList.toggle("is-active", !showTrace);
      traceViewButton.classList.toggle("is-active", showTrace);

      if (showTrace) {
        traceTitle.textContent = trace.identifier || trace.asset_id || "Result Trace";
        traceSubtitle.textContent = "Trace for asset " + String(trace.asset_id || "unknown") + ". Follow related results to pivot across the exported dataset.";
        traceSummary.innerHTML = renderTraceSummary(trace);
        traceSections.innerHTML = renderTraceSections(trace);
        traceRelated.innerHTML = renderTraceRelated(trace);
      } else {
        traceTitle.textContent = "Select a result";
        traceSubtitle.textContent = "Choose any result row to inspect its exported provenance, context, and related results.";
        traceSummary.innerHTML = "";
        traceSections.innerHTML = "";
        traceRelated.innerHTML = "<p class=\"muted\">No trace selected.</p>";
      }

      rows.forEach((row) => {
        const tr = document.createElement("tr");
        const discovered = row.discovery_date ? new Date(row.discovery_date).toLocaleString() : "";
        const domainKind = row.domain_kind
          ? "<span class=\"pill\">" + escapeHTML(formatDomainKind(row.domain_kind)) + "</span>"
          : "<span class=\"muted\">-</span>";
        const registrableDomain = row.registrable_domain
          ? escapeHTML(row.registrable_domain)
          : "<span class=\"muted\">-</span>";
        tr.innerHTML = [
          "<td><strong>", escapeHTML(row.identifier), "</strong><br><span class=\"muted\">", escapeHTML(row.asset_id), "</span></td>",
          "<td>", domainKind, "</td>",
          "<td>", registrableDomain, "</td>",
          "<td><span class=\"pill\">", escapeHTML(row.asset_type || "unknown"), "</span></td>",
          "<td>", renderSourceCell(row.source), "</td>",
          "<td>", escapeHTML(row.enumeration_id), "</td>",
          "<td>", escapeHTML(row.seed_id), "</td>",
          "<td>", escapeHTML(row.status), "</td>",
          "<td>", escapeHTML(discovered), "</td>",
          "<td>", escapeHTML(row.details), "</td>",
          "<td><a href=\"", escapeHTML(row.trace_path || "#"), "\" class=\"result-trace-link\" data-trace-link data-run-id=\"", escapeHTML(run ? run.id : ""), "\" data-asset-id=\"", escapeHTML(row.asset_id), "\">Open Trace</a></td>"
        ].join("");
        body.appendChild(tr);
      });

      emptyState.style.display = !showTrace && rows.length === 0 ? "block" : "none";
      updateSortIndicators();
    }

    function updateFiltersForRun() {
      const run = currentRun();
      const rows = run ? run.rows : [];
      refillFilter(typeFilter, uniqueValues(rows, "asset_type"), "All asset types", state.type);
      state.type = typeFilter.value;
      refillFilter(domainKindFilter, uniqueValues(rows, "domain_kind"), "All domain kinds", state.domainKind);
      Array.from(domainKindFilter.options).forEach((option) => {
        if (option.value) {
          option.textContent = formatDomainKind(option.value);
        }
      });
      state.domainKind = domainKindFilter.value;
      refillSourceFilter(rows);
    }

    function updateSortIndicators() {
      document.querySelectorAll("thead button").forEach((button) => {
        const key = button.dataset.key;
        const suffix = key === state.sortKey ? (state.sortDirection === "asc" ? " ▲" : " ▼") : "";
        const labels = {
          identifier: "Identifier",
          domain_kind: "Domain Kind",
          registrable_domain: "Registrable Domain",
          asset_type: "Type",
          source: "Source",
          enumeration_id: "Enumeration",
          seed_id: "Seed",
          status: "Status",
          discovery_date: "Discovered",
          details: "Details"
        };
        button.textContent = (labels[key] || key) + suffix;
      });
    }

    function escapeHTML(value) {
      return String(value || "")
        .replaceAll("&", "&amp;")
        .replaceAll("<", "&lt;")
        .replaceAll(">", "&gt;")
        .replaceAll('"', "&quot;");
    }

    runSelect.addEventListener("change", (event) => {
      state.runId = event.target.value;
      updateFiltersForRun();
      if (state.view === "trace") {
        const run = currentRun();
        const trace = currentTrace(run);
        if (!trace) {
          state.traceAssetId = run && Array.isArray(run.traces) && run.traces[0] ? run.traces[0].asset_id : "";
          if (!state.traceAssetId) {
            state.view = "results";
          }
        }
      }
      renderTable();
      syncHash();
    });

    searchInput.addEventListener("input", (event) => {
      state.search = normalize(event.target.value);
      renderTable();
    });

    typeFilter.addEventListener("change", (event) => {
      state.type = event.target.value;
      renderTable();
    });

    domainKindFilter.addEventListener("change", (event) => {
      state.domainKind = event.target.value;
      renderTable();
    });

    resultsViewButton.addEventListener("click", () => {
      state.view = "results";
      renderTable();
      syncHash();
    });

    traceViewButton.addEventListener("click", () => {
      openTraceFromCurrentSelection();
    });

    traceBackButton.addEventListener("click", () => {
      state.view = "results";
      renderTable();
      syncHash();
    });

    sourceFilter.addEventListener("click", (event) => {
      event.stopPropagation();
    });

    sourceFilterTrigger.addEventListener("click", () => {
      sourceFilterMenu.hidden = !sourceFilterMenu.hidden;
      sourceFilter.classList.toggle("is-open", !sourceFilterMenu.hidden);
      syncSourceFilterUI();
    });

    sourceFilterMenu.addEventListener("change", (event) => {
      const target = event.target;
      if (!(target instanceof HTMLInputElement) || target.type !== "checkbox") {
        return;
      }

      if (target.dataset.role === "all") {
        state.sources = [];
      } else {
        state.sources = Array.from(sourceFilterOptions.querySelectorAll("input[type=\"checkbox\"]:checked")).map((input) => input.value);
      }

      syncSourceFilterUI();
      renderTable();
    });

    document.addEventListener("click", () => {
      sourceFilterMenu.hidden = true;
      sourceFilter.classList.remove("is-open");
      syncSourceFilterUI();
    });

    document.addEventListener("keydown", (event) => {
      if (event.key === "Escape") {
        sourceFilterMenu.hidden = true;
        sourceFilter.classList.remove("is-open");
        syncSourceFilterUI();
      }
    });

    document.querySelectorAll("thead button").forEach((button) => {
      button.addEventListener("click", () => {
        const { key } = button.dataset;
        if (state.sortKey === key) {
          state.sortDirection = state.sortDirection === "asc" ? "desc" : "asc";
        } else {
          state.sortKey = key;
          state.sortDirection = key === "discovery_date" ? "desc" : "asc";
        }
        renderTable();
      });
    });

    document.addEventListener("click", (event) => {
      const link = event.target.closest("[data-trace-link]");
      if (!link) {
        return;
      }

      event.preventDefault();
      openTrace(link.dataset.runId || state.runId, link.dataset.assetId);
    });

    document.addEventListener("pointerover", (event) => {
      const target = tooltipTarget(event);
      if (!target) {
        return;
      }
      showTooltip(target, target.dataset.tooltip);
    });

    document.addEventListener("pointermove", (event) => {
      const target = tooltipTarget(event);
      if (!target) {
        return;
      }
      showTooltip(target, target.dataset.tooltip);
    });

    document.addEventListener("pointerout", (event) => {
      if (!tooltipTarget(event)) {
        return;
      }
      hideTooltip();
    });

    document.addEventListener("focusin", (event) => {
      const target = tooltipTarget(event);
      if (!target) {
        return;
      }
      showTooltip(target, target.dataset.tooltip);
    });

    document.addEventListener("focusout", (event) => {
      if (!tooltipTarget(event)) {
        return;
      }
      hideTooltip();
    });

    window.addEventListener("scroll", hideTooltip, true);
    window.addEventListener("resize", hideTooltip);

    window.addEventListener("hashchange", () => {
      const next = parseHash();
      state.view = next.view;
      if (next.runId) {
        state.runId = next.runId;
      }
      state.traceAssetId = next.assetId;
      fillRunSelect();
      updateFiltersForRun();
      renderTable();
    });

    fillRunSelect();
    updateFiltersForRun();
    renderTable();
  </script>
</body>
</html>
`))
