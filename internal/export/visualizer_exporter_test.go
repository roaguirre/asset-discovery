package export

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"asset-discovery/internal/filter"
	"asset-discovery/internal/models"
	"asset-discovery/internal/tracing/lineage"
)

func TestVisualizerExporter_ArchivesRunsAndRendersHTML(t *testing.T) {
	htmlPath := filepath.Join(t.TempDir(), "visualizer.html")

	firstRunTime := time.Date(2026, time.March, 17, 22, 50, 0, 0, time.FixedZone("-0300", -3*60*60))
	firstExporter := NewVisualizerExporter(htmlPath, "run-1", Downloads{
		JSON: "runs/run-1/results.json",
		CSV:  "runs/run-1/results.csv",
	})
	firstExporter.now = func() time.Time { return firstRunTime }

	if _, err := firstExporter.Process(context.Background(), sampleVisualizerContext("seed-1", "enum-1", "asset-1", "api.example.com", firstRunTime)); err != nil {
		t.Fatalf("expected first visualizer export to succeed, got %v", err)
	}

	secondRunTime := firstRunTime.Add(5 * time.Minute)
	secondExporter := NewVisualizerExporter(htmlPath, "run-2", Downloads{
		JSON: "runs/run-2/results.json",
		XLSX: "runs/run-2/results.xlsx",
	})
	secondExporter.now = func() time.Time { return secondRunTime }

	if _, err := secondExporter.Process(context.Background(), sampleVisualizerContext("seed-2", "enum-2", "asset-2", "app.example.com", secondRunTime)); err != nil {
		t.Fatalf("expected second visualizer export to succeed, got %v", err)
	}

	manifestPath := filepath.Join(strings.TrimSuffix(htmlPath, filepath.Ext(htmlPath)), "manifest.json")
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("expected manifest to exist, got %v", err)
	}

	var manifest Manifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatalf("expected manifest JSON to parse, got %v", err)
	}

	if len(manifest.Runs) != 2 {
		t.Fatalf("expected 2 archived runs, got %d", len(manifest.Runs))
	}

	if manifest.Runs[0].ID != "run-2" || manifest.Runs[1].ID != "run-1" {
		t.Fatalf("expected manifest runs ordered newest-first, got %+v", manifest.Runs)
	}

	firstSnapshotPath := filepath.Join(strings.TrimSuffix(htmlPath, filepath.Ext(htmlPath)), "runs", "run-1.json")
	if _, err := os.Stat(firstSnapshotPath); err != nil {
		t.Fatalf("expected first snapshot to exist, got %v", err)
	}

	firstSnapshotData, err := os.ReadFile(firstSnapshotPath)
	if err != nil {
		t.Fatalf("expected first snapshot to be readable, got %v", err)
	}

	var firstSnapshot Run
	if err := json.Unmarshal(firstSnapshotData, &firstSnapshot); err != nil {
		t.Fatalf("expected first snapshot JSON to parse, got %v", err)
	}

	if len(firstSnapshot.Rows) != 2 {
		t.Fatalf("expected first snapshot to contain 2 rows, got %d", len(firstSnapshot.Rows))
	}

	if firstSnapshot.Rows[0].DomainKind != string(DomainKindSubdomain) {
		t.Fatalf("expected visualizer row to classify api.example.com as subdomain, got %+v", firstSnapshot.Rows[0])
	}

	if firstSnapshot.Rows[0].RegistrableDomain != "example.com" {
		t.Fatalf("expected visualizer row registrable domain to be example.com, got %+v", firstSnapshot.Rows[0])
	}

	if firstSnapshot.Rows[0].TracePath != "#trace/run-1/asset-1" {
		t.Fatalf("expected visualizer row trace path to be populated, got %+v", firstSnapshot.Rows[0])
	}

	if len(firstSnapshot.Traces) != 2 {
		t.Fatalf("expected first snapshot to contain 2 traces, got %d", len(firstSnapshot.Traces))
	}

	if firstSnapshot.JudgeSummary == nil {
		t.Fatalf("expected first snapshot to include judge summary, got %+v", firstSnapshot)
	}
	if firstSnapshot.JudgeSummary.EvaluationCount != 2 || firstSnapshot.JudgeSummary.AcceptedCount != 1 || firstSnapshot.JudgeSummary.DiscardedCount != 2 {
		t.Fatalf("expected judge summary counts to be preserved, got %+v", firstSnapshot.JudgeSummary)
	}
	if !snapshotHasJudgeCandidate(firstSnapshot.JudgeSummary, "example-store.com", true) {
		t.Fatalf("expected accepted judge candidate to be present, got %+v", firstSnapshot.JudgeSummary)
	}
	if !snapshotHasJudgeCandidate(firstSnapshot.JudgeSummary, "facebook.com", false) {
		t.Fatalf("expected discarded judge candidate to be present, got %+v", firstSnapshot.JudgeSummary)
	}

	firstTrace := findTraceByAssetID(firstSnapshot.Traces, "asset-1")
	if firstTrace == nil {
		t.Fatalf("expected trace for asset-1 to be present, got %+v", firstSnapshot.Traces)
	}

	if len(firstTrace.Sections) == 0 {
		t.Fatalf("expected trace sections to be populated, got %+v", firstTrace)
	}
	if firstTrace.RootNodeID == "" || len(firstTrace.Nodes) == 0 {
		t.Fatalf("expected trace nodes to be populated, got %+v", firstTrace)
	}
	if !traceHasNode(firstTrace.Nodes, firstTrace.RootNodeID, "asset") {
		t.Fatalf("expected trace root asset node to be present, got %+v", firstTrace.Nodes)
	}

	if len(firstTrace.Related) == 0 || firstTrace.Related[0].AssetID != "asset-1-related" {
		t.Fatalf("expected trace to link to the related result, got %+v", firstTrace.Related)
	}

	htmlData, err := os.ReadFile(htmlPath)
	if err != nil {
		t.Fatalf("expected visualizer HTML to exist, got %v", err)
	}

	html := string(htmlData)
	assertInlineScriptBalanced(t, html)
	for _, needle := range []string{
		"run-1",
		"run-2",
		"api.example.com",
		"app.example.com",
		"source-filter-options",
		"splitSources",
		"source-pill",
		`sources: []`,
		`state.sources.every((source) => rowSources.includes(source))`,
		"sourceDescriptions = Object.freeze",
		"Certificate Transparency results from crt.sh",
		"PTR, ASN, organization, and CIDR enrichment backfill applied to canonical IP assets.",
		`id="app-tooltip"`,
		"data-tooltip=",
		`data-key="identifier" data-tooltip="The domain or hostname identifier for this asset."`,
		`data-key="asn" data-tooltip="Autonomous System Number associated with this IP address."`,
		`data-key="ptr" data-tooltip="Reverse DNS hostname returned for this IP address, when one exists."`,
		"showTooltip(",
		"trace-view-button",
		"Result Trace",
		"Open Trace",
		"Same Registrable Domain",
		"#trace/run-1/asset-1",
		"data-trace-link",
		"Judge Analysis",
		"Accepted And Discarded Candidates",
		"Discarded Candidates",
		"facebook.com",
		"example-store.com",
		"detail-toggle",
		"domain-group-row",
		"judge-view-button",
		"llm-summary",
		"detail-panel",
		"trace-tree",
		"trace-panel",
		"trace-node-button",
		"trace-panel-body",
		"trace-workspace",
		"ownership-pill",
		"Discovered By",
		"Enriched By",
		"expandedRows",
		"expandedDomainGroups",
		"domain-group-toggle",
		"domain-group-controls",
		"domain-summary-trigger",
		"domain-child-trigger",
		"domain-child-row",
		"domain-child-identifier",
		"group.summaryRow = registrable || group.rows[0] || null;",
		"const summaryExpanded = Boolean(summaryRow) && state.expandedRows.has(summaryRow.asset_id);",
		"const summaryRow = group.summaryRow || group.rows[0] || null;",
		"const childRows = group.rows.filter((row) => !summaryRow || row.asset_id !== summaryRow.asset_id);",
		"Showing \" + domainGroups.length + \" registrable domains",
		"function rowsForSourceFilter(runRows)",
		`if (state.view === "domains") { return runRows.filter((row) => row.asset_type === "domain"); }`,
		`if (state.view === "ips") { return runRows.filter((row) => row.asset_type === "ip"); }`,
	} {
		if !strings.Contains(html, needle) {
			t.Fatalf("expected rendered HTML to contain %q", needle)
		}
	}
}

func assertInlineScriptBalanced(t *testing.T, html string) {
	t.Helper()

	start := strings.Index(html, "<script>")
	if start < 0 {
		t.Fatalf("expected rendered HTML to include an inline script")
	}
	start += len("<script>")
	end := strings.Index(html[start:], "</script>")
	if end < 0 {
		t.Fatalf("expected rendered HTML to close the inline script")
	}

	script := html[start : start+end]
	type stackEntry struct {
		token rune
		line  int
	}

	var (
		stack          []stackEntry
		inSingleQuote  bool
		inDoubleQuote  bool
		inTemplate     bool
		inLineComment  bool
		inBlockComment bool
		escaped        bool
		line           = 1
	)

	matching := map[rune]rune{'(': ')', '{': '}', '[': ']'}

	for i, r := range script {
		if r == '\n' {
			line++
			if inLineComment {
				inLineComment = false
			}
		}

		if inLineComment {
			continue
		}
		if inBlockComment {
			if r == '*' && i+1 < len(script) && script[i+1] == '/' {
				inBlockComment = false
			}
			continue
		}
		if inSingleQuote {
			if escaped {
				escaped = false
				continue
			}
			if r == '\\' {
				escaped = true
				continue
			}
			if r == '\'' {
				inSingleQuote = false
			}
			continue
		}
		if inDoubleQuote {
			if escaped {
				escaped = false
				continue
			}
			if r == '\\' {
				escaped = true
				continue
			}
			if r == '"' {
				inDoubleQuote = false
			}
			continue
		}
		if inTemplate {
			if escaped {
				escaped = false
				continue
			}
			if r == '\\' {
				escaped = true
				continue
			}
			if r == '`' {
				inTemplate = false
			}
			continue
		}

		if r == '/' && i+1 < len(script) {
			switch script[i+1] {
			case '/':
				inLineComment = true
				continue
			case '*':
				inBlockComment = true
				continue
			}
		}

		switch r {
		case '\'':
			inSingleQuote = true
		case '"':
			inDoubleQuote = true
		case '`':
			inTemplate = true
		case '(', '{', '[':
			stack = append(stack, stackEntry{token: r, line: line})
		case ')', '}', ']':
			if len(stack) == 0 {
				t.Fatalf("expected balanced inline script, found unexpected %q at line %d", string(r), line)
			}
			top := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if matching[top.token] != r {
				t.Fatalf("expected balanced inline script, %q at line %d was closed by %q at line %d", string(top.token), top.line, string(r), line)
			}
		}
	}

	if len(stack) != 0 {
		top := stack[len(stack)-1]
		t.Fatalf("expected balanced inline script, %q opened at line %d was never closed", string(top.token), top.line)
	}
}

func TestRefreshVisualizerHTML_RebuildsFromArchivedSnapshots(t *testing.T) {
	htmlPath := filepath.Join(t.TempDir(), "visualizer.html")
	runTime := time.Date(2026, time.March, 24, 2, 10, 0, 0, time.FixedZone("-0300", -3*60*60))

	exporter := NewVisualizerExporter(htmlPath, "run-refresh", Downloads{
		JSON: "runs/run-refresh/results.json",
	})
	exporter.now = func() time.Time { return runTime }

	if _, err := exporter.Process(context.Background(), sampleVisualizerContext("seed-1", "enum-1", "asset-1", "api.example.com", runTime)); err != nil {
		t.Fatalf("expected visualizer export to succeed, got %v", err)
	}

	if err := os.Remove(htmlPath); err != nil {
		t.Fatalf("expected to remove rendered visualizer before refresh, got %v", err)
	}

	if err := RefreshVisualizerHTML(htmlPath); err != nil {
		t.Fatalf("expected visualizer refresh to rebuild archived HTML, got %v", err)
	}

	htmlData, err := os.ReadFile(htmlPath)
	if err != nil {
		t.Fatalf("expected refreshed visualizer HTML to exist, got %v", err)
	}

	html := string(htmlData)
	assertInlineScriptBalanced(t, html)
	for _, needle := range []string{"run-refresh", "api.example.com", "#trace/run-refresh/asset-1"} {
		if !strings.Contains(html, needle) {
			t.Fatalf("expected refreshed visualizer HTML to contain %q", needle)
		}
	}
}

func TestBuildVisualizerRun_SplitsDiscoveryAndEnrichmentSources(t *testing.T) {
	ts := time.Date(2026, time.March, 24, 9, 0, 0, 0, time.FixedZone("-0300", -3*60*60))
	pCtx := &models.PipelineContext{
		Seeds: []models.Seed{
			{ID: "seed-1", CompanyName: "Gesprobira", Domains: []string{"gesprobira.cl"}},
		},
		Enumerations: []models.Enumeration{
			{ID: "enum-1", SeedID: "seed-1", Status: "running", CreatedAt: ts},
		},
		Assets: []models.Asset{
			{
				ID:            "dom-ht-1",
				EnumerationID: "enum-1",
				Type:          models.AssetTypeDomain,
				Identifier:    "cpcontacts.gesprobira.cl",
				Source:        "hackertarget_collector",
				DiscoveryDate: ts,
			},
			{
				ID:            "dom-ct-1",
				EnumerationID: "enum-1",
				Type:          models.AssetTypeDomain,
				Identifier:    "cpcontacts.gesprobira.cl",
				Source:        "crt.sh",
				DiscoveryDate: ts.Add(10 * time.Second),
			},
		},
	}
	pCtx.EnsureAssetState()
	pCtx.AppendAssetObservations(models.AssetObservation{
		ID:            "obs-domain-enricher-1",
		Kind:          models.ObservationKindEnrichment,
		AssetID:       pCtx.Assets[0].ID,
		EnumerationID: "enum-1",
		Type:          models.AssetTypeDomain,
		Identifier:    "cpcontacts.gesprobira.cl",
		Source:        "domain_enricher",
		DiscoveryDate: ts.Add(20 * time.Second),
		DomainDetails: &models.DomainDetails{
			Records: []models.DNSRecord{{Type: "A", Value: "162.240.236.164"}},
		},
		EnrichmentStates: map[string]models.EnrichmentState{
			"domain_enricher": {Status: "completed", UpdatedAt: ts.Add(20 * time.Second)},
		},
	})

	run := buildVisualizerRun("run-test", ts.Add(30*time.Second), Downloads{}, pCtx)
	if len(run.Rows) != 1 {
		t.Fatalf("expected one canonical row, got %+v", run.Rows)
	}

	row := run.Rows[0]
	if row.Source != "crt.sh, hackertarget_collector" {
		t.Fatalf("expected row source to include discovery contributors only, got %+v", row)
	}
	if row.DiscoveredBy != "crt.sh, hackertarget_collector" {
		t.Fatalf("expected discovery sources to remain separate, got %+v", row)
	}
	if row.EnrichedBy != "domain_enricher" {
		t.Fatalf("expected enrichment source to be isolated, got %+v", row)
	}

	trace := findTraceByAssetID(run.Traces, row.AssetID)
	if trace == nil {
		t.Fatalf("expected trace for canonical row, got %+v", run.Traces)
	}
	if trace.DiscoveredBy != row.DiscoveredBy || trace.EnrichedBy != row.EnrichedBy {
		t.Fatalf("expected trace source split to match row, got %+v", trace)
	}
	if !traceHasNodeWithSource(trace.Nodes, "domain_enricher") {
		t.Fatalf("expected trace nodes to include the enrichment observation, got %+v", trace.Nodes)
	}
}

func TestBuildVisualizerRun_ExportsDomainResolutionAndWHOISDetails(t *testing.T) {
	ts := time.Date(2026, time.March, 24, 0, 35, 55, 0, time.FixedZone("-0300", -3*60*60))
	pCtx := &models.PipelineContext{
		Seeds: []models.Seed{{ID: "seed-1", Domains: []string{"gesprobira.cl"}}},
		Enumerations: []models.Enumeration{{
			ID:        "enum-1",
			SeedID:    "seed-1",
			Status:    "completed",
			CreatedAt: ts,
			UpdatedAt: ts,
		}},
		Assets: []models.Asset{{
			ID:            "dom-rdap-1",
			EnumerationID: "enum-1",
			Type:          models.AssetTypeDomain,
			Identifier:    "gesprobira.cl",
			Source:        "rdap_collector",
			DiscoveryDate: ts,
			DomainDetails: &models.DomainDetails{
				RDAP: &models.RDAPData{
					RegistrarName:   "NIC Chile",
					RegistrarURL:    "https://www.nic.cl",
					RegistrantName:  "Francisco Aguirre",
					RegistrantEmail: "contacto@example.cl",
					NameServers:     []string{"achiel.ns.cloudflare.com", "aida.ns.cloudflare.com"},
					CreationDate:    time.Date(2018, time.January, 25, 21, 37, 38, 0, time.UTC),
					UpdatedDate:     time.Date(2025, time.March, 23, 8, 11, 10, 0, time.UTC),
					ExpirationDate:  time.Date(2028, time.January, 25, 21, 37, 38, 0, time.UTC),
					Statuses:        []string{"ok"},
				},
			},
			EnrichmentStates: map[string]models.EnrichmentState{
				"domain_enricher": {Status: "completed", UpdatedAt: ts},
			},
		}},
	}

	run := buildVisualizerRun("run-test", ts, Downloads{}, pCtx)
	if len(run.Rows) != 1 {
		t.Fatalf("expected one row, got %+v", run.Rows)
	}

	row := run.Rows[0]
	if row.ResolutionStatus != string(models.DomainResolutionStatusUnresolved) {
		t.Fatalf("expected unresolved resolution status, got %+v", row)
	}
	if !hasEvidenceGroup(row.EvidenceGroups, "DNS", "Unresolved") {
		t.Fatalf("expected DNS evidence group to show unresolved state, got %+v", row.EvidenceGroups)
	}
	if !hasEvidenceGroup(row.EvidenceGroups, "Registration", "Registrar: NIC Chile") {
		t.Fatalf("expected grouped registration evidence, got %+v", row.EvidenceGroups)
	}
	for _, fragment := range []string{
		"Resolution unresolved",
		"Registrar NIC Chile",
		"RegistrarURL https://www.nic.cl",
		"RegistrantName Francisco Aguirre",
		"RegistrantEmail contacto@example.cl",
		"Created 2018-01-25",
		"Updated 2025-03-23",
		"Expires 2028-01-25",
		"RegistrationStatus ok",
	} {
		if !strings.Contains(row.Details, fragment) {
			t.Fatalf("expected details to contain %q, got %q", fragment, row.Details)
		}
	}

	trace := findTraceByAssetID(run.Traces, row.AssetID)
	if trace == nil {
		t.Fatalf("expected trace to be exported, got %+v", run.Traces)
	}
	if trace.ResolutionStatus != row.ResolutionStatus {
		t.Fatalf("expected trace resolution status to match row, got %+v", trace)
	}
	for _, fragment := range []string{
		"DNS resolution: unresolved",
		"Registrar URL: https://www.nic.cl",
		"Registrant email: contacto@example.cl",
		"Registration updated: 2025-03-23",
		"Registration status: ok",
	} {
		if !traceSectionContains(trace.Sections, "Domain Evidence", fragment) {
			t.Fatalf("expected domain trace section to contain %q, got %+v", fragment, trace.Sections)
		}
	}
}

func TestBuildVisualizerRun_MarksResolvedDomains(t *testing.T) {
	ts := time.Date(2026, time.March, 24, 0, 35, 55, 0, time.FixedZone("-0300", -3*60*60))
	run := buildVisualizerRun("run-test", ts, Downloads{}, sampleVisualizerContext("seed-1", "enum-1", "asset-1", "api.example.com", ts))
	if len(run.Rows) == 0 {
		t.Fatalf("expected rows to be exported, got %+v", run)
	}
	if run.Rows[0].ResolutionStatus != string(models.DomainResolutionStatusResolved) {
		t.Fatalf("expected first row to be marked resolved, got %+v", run.Rows[0])
	}
	if !hasEvidenceGroup(run.Rows[0].EvidenceGroups, "DNS", "A:203.0.113.10") {
		t.Fatalf("expected grouped DNS evidence, got %+v", run.Rows[0].EvidenceGroups)
	}
	if !strings.Contains(run.Rows[0].Details, "Resolution resolved") {
		t.Fatalf("expected details to include resolved marker, got %q", run.Rows[0].Details)
	}
}

func TestBuildVisualizerRun_ObservationTraceShowsUnresolvedDNSState(t *testing.T) {
	ts := time.Date(2026, time.March, 24, 1, 0, 0, 0, time.FixedZone("-0300", -3*60*60))
	pCtx := &models.PipelineContext{
		Seeds: []models.Seed{{ID: "seed-1", Domains: []string{"example.com"}}},
		Enumerations: []models.Enumeration{{
			ID:        "enum-1",
			SeedID:    "seed-1",
			Status:    "completed",
			CreatedAt: ts,
		}},
		Assets: []models.Asset{{
			ID:            "dom-1",
			EnumerationID: "enum-1",
			Type:          models.AssetTypeDomain,
			Identifier:    "missing.example.com",
			Source:        "crt.sh",
			DiscoveryDate: ts,
		}},
	}
	pCtx.EnsureAssetState()
	pCtx.AppendAssetObservations(models.AssetObservation{
		ID:            "obs-domain-enricher-1",
		Kind:          models.ObservationKindEnrichment,
		AssetID:       pCtx.Assets[0].ID,
		EnumerationID: "enum-1",
		Type:          models.AssetTypeDomain,
		Identifier:    "missing.example.com",
		Source:        "domain_enricher",
		DiscoveryDate: ts.Add(10 * time.Second),
		DomainDetails: &models.DomainDetails{},
		EnrichmentStates: map[string]models.EnrichmentState{
			"domain_enricher": {Status: "completed", UpdatedAt: ts.Add(10 * time.Second)},
		},
	})

	run := buildVisualizerRun("run-test", ts, Downloads{}, pCtx)
	trace := findTraceByAssetID(run.Traces, pCtx.Assets[0].ID)
	if trace == nil {
		t.Fatalf("expected trace to be exported, got %+v", run.Traces)
	}
	if !traceNodeSectionContains(trace.Nodes, "domain_enricher", "Domain Evidence", "DNS resolution: unresolved") {
		t.Fatalf("expected unresolved DNS state on enrichment observation node, got %+v", trace.Nodes)
	}
}

func TestVisualizerExporter_TracePreservesMergedContributorLineage(t *testing.T) {
	htmlPath := filepath.Join(t.TempDir(), "visualizer.html")
	ts := time.Date(2026, time.March, 18, 10, 0, 0, 0, time.FixedZone("-0300", -3*60*60))

	pCtx := sampleMergedVisualizerContext(ts)
	mergeFilter := filter.NewMergeFilter()
	if _, err := mergeFilter.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected merge filter to succeed, got %v", err)
	}

	if len(pCtx.Assets) != 1 {
		t.Fatalf("expected merged context to collapse to 1 asset, got %d", len(pCtx.Assets))
	}

	exporter := NewVisualizerExporter(htmlPath, "run-merged", Downloads{})
	exporter.now = func() time.Time { return ts.Add(5 * time.Minute) }

	if _, err := exporter.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected visualizer export to succeed, got %v", err)
	}

	snapshotPath := filepath.Join(strings.TrimSuffix(htmlPath, filepath.Ext(htmlPath)), "runs", "run-merged.json")
	snapshotData, err := os.ReadFile(snapshotPath)
	if err != nil {
		t.Fatalf("expected merged snapshot to be readable, got %v", err)
	}

	var snapshot Run
	if err := json.Unmarshal(snapshotData, &snapshot); err != nil {
		t.Fatalf("expected merged snapshot JSON to parse, got %v", err)
	}

	if len(snapshot.Rows) != 1 {
		t.Fatalf("expected merged snapshot to contain 1 row, got %d", len(snapshot.Rows))
	}

	if snapshot.Rows[0].EnumerationID != "enum-1, enum-2" {
		t.Fatalf("expected merged row to retain both enumerations, got %+v", snapshot.Rows[0])
	}

	if snapshot.Rows[0].SeedID != "seed-1, seed-2" {
		t.Fatalf("expected merged row to retain both seeds, got %+v", snapshot.Rows[0])
	}

	trace := findTraceByAssetID(snapshot.Traces, "merged-asset-1")
	if trace == nil {
		t.Fatalf("expected merged trace to exist, got %+v", snapshot.Traces)
	}

	if len(trace.Contributors) != 2 {
		t.Fatalf("expected merged trace to preserve 2 contributors, got %+v", trace.Contributors)
	}

	if trace.EnumerationID != "enum-1, enum-2" || trace.SeedID != "seed-1, seed-2" {
		t.Fatalf("expected merged trace summary to retain both enumerations and seeds, got %+v", trace)
	}
	if trace.RootNodeID == "" || len(trace.Nodes) == 0 {
		t.Fatalf("expected merged trace nodes to be present, got %+v", trace)
	}

	if !hasTraceContributor(trace.Contributors, "merged-asset-2", "enum-2", "seed-2", "wayback_collector") {
		t.Fatalf("expected merged trace to retain second contributor lineage, got %+v", trace.Contributors)
	}

	if !traceSectionContains(trace.Sections, "Contributor Provenance", "enumeration enum-2") {
		t.Fatalf("expected contributor provenance section to include enum-2, got %+v", trace.Sections)
	}

	if !traceSectionContains(trace.Sections, "Seed Context", "Evidence: ownership_judge | ownership_judged | example.com | confidence 0.93 | reasoned") {
		t.Fatalf("expected merged trace to include seed evidence for contributor seeds, got %+v", trace.Sections)
	}
}

func TestBuildVisualizerRun_MergedDiscoveryTraceUsesProvenanceSummaryAndRichGroupNodes(t *testing.T) {
	ts := time.Date(2026, time.March, 24, 2, 0, 0, 0, time.FixedZone("-0300", -3*60*60))
	run := buildVisualizerRun("run-gesprobira", ts, Downloads{}, sampleMergedDiscoveryTraceContext(ts))

	row := findRowByIdentifier(run.Rows, "gesprobira.cl")
	if row == nil {
		t.Fatalf("expected gesprobira.cl row to be present, got %+v", run.Rows)
	}

	expectedReason := "Supported by 4 discovery observations from crt.sh, dns_collector, sitemap_collector, wayback_collector"
	if row.InclusionReason != expectedReason {
		t.Fatalf("expected merged discovery reason, got %+v", row)
	}

	trace := findTraceByAssetID(run.Traces, row.AssetID)
	if trace == nil {
		t.Fatalf("expected trace for gesprobira.cl to be exported, got %+v", run.Traces)
	}

	if !traceNodeSectionContains(trace.Nodes, "gesprobira.cl", "Ownership", "Inclusion reason: "+expectedReason) {
		t.Fatalf("expected root ownership card to use merged discovery reason, got %+v", trace.Nodes)
	}
	if countTraceNodesByKind(trace.Nodes, "seed") != 1 {
		t.Fatalf("expected one seed node for the merged contributors, got %+v", trace.Nodes)
	}
	if countTraceNodesByKind(trace.Nodes, "contributor") != 4 {
		t.Fatalf("expected one contributor child per discovery provenance entry, got %+v", trace.Nodes)
	}
	if !traceNodeSectionContains(trace.Nodes, "Observations", "Observation Summary", "Total observations: 5") {
		t.Fatalf("expected observations group summary to be exported, got %+v", trace.Nodes)
	}
	if !traceNodeSectionContains(trace.Nodes, "Observations", "Observation Summary", "Discovery observations: 4") {
		t.Fatalf("expected observations group to count discovery observations, got %+v", trace.Nodes)
	}
	if !traceNodeSectionContains(trace.Nodes, "Seed Context", "Seed Summary", "Unique seeds: 1") {
		t.Fatalf("expected seed group summary to be exported, got %+v", trace.Nodes)
	}
	if !traceNodeSectionContainsWithKind(trace.Nodes, "contributor", "sitemap_collector", "Contributor", "Enumeration ID: enum-1") {
		t.Fatalf("expected contributor child node details to be preserved, got %+v", trace.Nodes)
	}
	if !traceNodeSectionContains(trace.Nodes, "Relations", "Relation Summary", "Total relations: 1") {
		t.Fatalf("expected relations group summary to be exported, got %+v", trace.Nodes)
	}
	if !traceNodeSectionContains(trace.Nodes, "Enrichment", "Enrichment Summary", "Stage names: domain_enricher") {
		t.Fatalf("expected enrichment group summary to be exported, got %+v", trace.Nodes)
	}
}

func TestVisualizerExporter_PreservesReconsiderationJudgeGroupAndFinalWaveAssets(t *testing.T) {
	htmlPath := filepath.Join(t.TempDir(), "visualizer.html")
	ts := time.Date(2026, time.March, 18, 11, 0, 0, 0, time.FixedZone("-0300", -3*60*60))

	pCtx := &models.PipelineContext{
		Seeds: []models.Seed{
			{
				ID:          "seed-1",
				CompanyName: "Example Corp",
				Domains:     []string{"example.com"},
			},
			{
				ID:          "seed-1:example-store.com",
				CompanyName: "Example Corp",
				Domains:     []string{"example-store.com"},
				Evidence: []models.SeedEvidence{
					{Source: "post_run_reconsideration", Kind: "ownership_judged", Value: "example-store.com", Confidence: 0.94, Reasoned: true},
				},
			},
		},
		Enumerations: []models.Enumeration{
			{
				ID:        "enum-1",
				SeedID:    "seed-1",
				Status:    "running",
				CreatedAt: ts.Add(-4 * time.Minute),
			},
			{
				ID:        "enum-2",
				SeedID:    "seed-1:example-store.com",
				Status:    "running",
				CreatedAt: ts.Add(-1 * time.Minute),
			},
		},
		Assets: []models.Asset{
			{
				ID:            "asset-1",
				EnumerationID: "enum-2",
				Type:          models.AssetTypeDomain,
				Identifier:    "portal.example-store.com",
				Source:        "crawler_collector",
				DiscoveryDate: ts,
				DomainDetails: &models.DomainDetails{},
			},
		},
		JudgeEvaluations: []models.JudgeEvaluation{
			{
				Collector:   "web_hint_collector",
				SeedID:      "seed-1",
				SeedLabel:   "Example Corp",
				SeedDomains: []string{"example.com"},
				Scenario:    "web ownership hints from example.com",
				Outcomes: []models.JudgeCandidateOutcome{
					{
						Root:       "example-store.com",
						Collect:    false,
						Explicit:   true,
						Confidence: 0.88,
						Reason:     "Homepage evidence alone was too weak.",
						Support:    []string{"https://example-store.com/ [store]"},
					},
				},
			},
			{
				Collector:   "run_reconsideration",
				SeedID:      "seed-1",
				SeedLabel:   "Example Corp",
				SeedDomains: []string{"example.com"},
				Scenario:    "post-run discarded candidate reconsideration",
				Outcomes: []models.JudgeCandidateOutcome{
					{
						Root:       "example-store.com",
						Collect:    true,
						Explicit:   true,
						Confidence: 0.94,
						Kind:       "ownership_judged",
						Reason:     "The completed run already contains first-party assets under this root.",
						Support:    []string{"Current run already discovered portal.example-store.com"},
					},
				},
			},
		},
	}

	exporter := NewVisualizerExporter(htmlPath, "run-reconsidered", Downloads{})
	exporter.now = func() time.Time { return ts.Add(3 * time.Minute) }

	if _, err := exporter.Process(context.Background(), pCtx); err != nil {
		t.Fatalf("expected visualizer export to succeed, got %v", err)
	}

	snapshotPath := filepath.Join(strings.TrimSuffix(htmlPath, filepath.Ext(htmlPath)), "runs", "run-reconsidered.json")
	snapshotData, err := os.ReadFile(snapshotPath)
	if err != nil {
		t.Fatalf("expected reconsidered snapshot to be readable, got %v", err)
	}

	var snapshot Run
	if err := json.Unmarshal(snapshotData, &snapshot); err != nil {
		t.Fatalf("expected reconsidered snapshot JSON to parse, got %v", err)
	}

	if !snapshotHasRow(snapshot.Rows, "portal.example-store.com") {
		t.Fatalf("expected final-wave asset to be present in the same run output, got %+v", snapshot.Rows)
	}

	group := findJudgeGroup(snapshot.JudgeSummary, "run_reconsideration", "post-run discarded candidate reconsideration")
	if group == nil {
		t.Fatalf("expected reconsideration judge group to be present, got %+v", snapshot.JudgeSummary)
	}
	if len(group.Accepted) != 1 || group.Accepted[0].Root != "example-store.com" {
		t.Fatalf("expected reconsidered candidate to appear as accepted, got %+v", group.Accepted)
	}
}

func sampleVisualizerContext(seedID, enumerationID, assetID, identifier string, ts time.Time) *models.PipelineContext {
	return &models.PipelineContext{
		Seeds: []models.Seed{
			{
				ID:          seedID,
				CompanyName: "Example Corp",
				Domains:     []string{"example.com"},
				Tags:        []string{"production"},
				Evidence: []models.SeedEvidence{
					{Source: "manual", Kind: "company_name", Value: "Example Corp"},
				},
			},
		},
		Enumerations: []models.Enumeration{
			{
				ID:        enumerationID,
				SeedID:    seedID,
				Status:    "running",
				CreatedAt: ts.Add(-2 * time.Minute),
				UpdatedAt: ts.Add(-1 * time.Minute),
			},
		},
		Assets: []models.Asset{
			{
				ID:            assetID,
				EnumerationID: enumerationID,
				Type:          models.AssetTypeDomain,
				Identifier:    identifier,
				Source:        "crt.sh",
				DiscoveryDate: ts,
				DomainDetails: &models.DomainDetails{
					Records: []models.DNSRecord{
						{Type: "A", Value: "203.0.113.10"},
					},
					RDAP: &models.RDAPData{
						RegistrarName: "Example Registrar",
						RegistrantOrg: "Example Corp",
						NameServers:   []string{"ns1.example.com"},
					},
				},
				EnrichmentData: map[string]interface{}{
					"cidr": "203.0.113.0/24",
				},
			},
			{
				ID:            assetID + "-related",
				EnumerationID: enumerationID,
				Type:          models.AssetTypeDomain,
				Identifier:    "www.example.com",
				Source:        "wayback_collector",
				DiscoveryDate: ts.Add(-1 * time.Minute),
				DomainDetails: &models.DomainDetails{
					Records: []models.DNSRecord{
						{Type: "CNAME", Value: "edge.example.net"},
					},
				},
			},
		},
		JudgeEvaluations: []models.JudgeEvaluation{
			{
				Collector:   "web_hint_collector",
				SeedID:      seedID,
				SeedLabel:   "Example Corp",
				SeedDomains: []string{"example.com"},
				Scenario:    "web ownership hints from example.com",
				Outcomes: []models.JudgeCandidateOutcome{
					{
						Root:       "example-store.com",
						Collect:    true,
						Confidence: 0.95,
						Kind:       "llm_link",
						Reason:     "Canonical storefront links point to a first-party property.",
						Explicit:   true,
						Support:    []string{"https://example-store.com/ [canonical]"},
					},
					{
						Root:       "facebook.com",
						Collect:    false,
						Confidence: 0.98,
						Reason:     "Social profile links are third-party platforms, not owned roots.",
						Explicit:   true,
						Support:    []string{"https://facebook.com/example [follow us]"},
					},
				},
			},
			{
				Collector:   "dns_collector",
				SeedID:      seedID,
				SeedLabel:   "Example Corp",
				SeedDomains: []string{"example.com"},
				Scenario:    "dns root variant pivot",
				Outcomes: []models.JudgeCandidateOutcome{
					{
						Root:     "cloudflare.com",
						Collect:  false,
						Explicit: false,
						Support:  []string{"Observed as a shared DNS target"},
					},
				},
			},
		},
	}
}

func sampleMergedVisualizerContext(ts time.Time) *models.PipelineContext {
	return &models.PipelineContext{
		Seeds: []models.Seed{
			{
				ID:          "seed-1",
				CompanyName: "Example Corp",
				Domains:     []string{"example.com"},
				Evidence: []models.SeedEvidence{
					{Source: "manual", Kind: "company_name", Value: "Example Corp"},
				},
			},
			{
				ID:          "seed-2",
				CompanyName: "Example Subsidiary",
				Domains:     []string{"example.com"},
				Tags:        []string{"subsidiary"},
				Evidence: []models.SeedEvidence{
					{Source: "ownership_judge", Kind: "ownership_judged", Value: "example.com", Confidence: 0.93, Reasoned: true},
				},
			},
		},
		Enumerations: []models.Enumeration{
			{
				ID:        "enum-1",
				SeedID:    "seed-1",
				Status:    "running",
				CreatedAt: ts.Add(-4 * time.Minute),
				UpdatedAt: ts.Add(-3 * time.Minute),
			},
			{
				ID:        "enum-2",
				SeedID:    "seed-2",
				Status:    "running",
				CreatedAt: ts.Add(-2 * time.Minute),
				UpdatedAt: ts.Add(-1 * time.Minute),
			},
		},
		Assets: []models.Asset{
			{
				ID:            "merged-asset-1",
				EnumerationID: "enum-1",
				Type:          models.AssetTypeDomain,
				Identifier:    "api.example.com",
				Source:        "crt.sh",
				DiscoveryDate: ts.Add(-2 * time.Minute),
				DomainDetails: &models.DomainDetails{
					Records: []models.DNSRecord{
						{Type: "A", Value: "203.0.113.10"},
					},
				},
			},
			{
				ID:            "merged-asset-2",
				EnumerationID: "enum-2",
				Type:          models.AssetTypeDomain,
				Identifier:    "api.example.com",
				Source:        "wayback_collector",
				DiscoveryDate: ts.Add(-1 * time.Minute),
				DomainDetails: &models.DomainDetails{
					Records: []models.DNSRecord{
						{Type: "CNAME", Value: "edge.example.net"},
					},
				},
			},
		},
	}
}

func sampleMergedDiscoveryTraceContext(ts time.Time) *models.PipelineContext {
	pCtx := &models.PipelineContext{
		Seeds: []models.Seed{
			{
				ID:          "seed-1",
				CompanyName: "Gesprobira",
				Domains:     []string{"gesprobira.cl"},
				Tags:        []string{"production"},
			},
		},
		Enumerations: []models.Enumeration{
			{
				ID:        "enum-1",
				SeedID:    "seed-1",
				Status:    "running",
				CreatedAt: ts.Add(-5 * time.Minute),
				UpdatedAt: ts.Add(-4 * time.Minute),
			},
		},
	}

	pCtx.AppendAssets(
		models.Asset{
			ID:            "dom-sitemap",
			EnumerationID: "enum-1",
			Type:          models.AssetTypeDomain,
			Identifier:    "gesprobira.cl",
			Source:        "sitemap_collector",
			DiscoveryDate: ts,
		},
		models.Asset{
			ID:            "dom-dns",
			EnumerationID: "enum-1",
			Type:          models.AssetTypeDomain,
			Identifier:    "gesprobira.cl",
			Source:        "dns_collector",
			DiscoveryDate: ts.Add(5 * time.Second),
		},
		models.Asset{
			ID:            "dom-wayback",
			EnumerationID: "enum-1",
			Type:          models.AssetTypeDomain,
			Identifier:    "gesprobira.cl",
			Source:        "wayback_collector",
			DiscoveryDate: ts.Add(10 * time.Second),
		},
		models.Asset{
			ID:            "dom-crtsh",
			EnumerationID: "enum-1",
			Type:          models.AssetTypeDomain,
			Identifier:    "gesprobira.cl",
			Source:        "crt.sh",
			DiscoveryDate: ts.Add(15 * time.Second),
		},
		models.Asset{
			ID:            "ip-1",
			EnumerationID: "enum-1",
			Type:          models.AssetTypeIP,
			Identifier:    "104.21.52.57",
			Source:        "domain_enricher",
			DiscoveryDate: ts.Add(25 * time.Second),
		},
	)

	domainAssetID := findCanonicalAssetID(pCtx.Assets, models.AssetTypeDomain, "gesprobira.cl")
	if domainAssetID == "" {
		return pCtx
	}

	pCtx.AppendAssetObservations(models.AssetObservation{
		ID:            "obs-domain-enricher-1",
		Kind:          models.ObservationKindEnrichment,
		AssetID:       domainAssetID,
		EnumerationID: "enum-1",
		Type:          models.AssetTypeDomain,
		Identifier:    "gesprobira.cl",
		Source:        "domain_enricher",
		DiscoveryDate: ts.Add(20 * time.Second),
		DomainDetails: &models.DomainDetails{},
		EnrichmentStates: map[string]models.EnrichmentState{
			"domain_enricher": {Status: "completed", UpdatedAt: ts.Add(20 * time.Second)},
		},
	})
	pCtx.AppendAssetRelations(models.AssetRelation{
		ID:             "rel-1",
		FromAssetType:  models.AssetTypeDomain,
		FromIdentifier: "gesprobira.cl",
		ToAssetType:    models.AssetTypeIP,
		ToIdentifier:   "104.21.52.57",
		EnumerationID:  "enum-1",
		Source:         "domain_enricher",
		Kind:           "dns_a",
		Label:          "Resolved IP",
		Reason:         "Resolved from gesprobira.cl via A",
		DiscoveryDate:  ts.Add(25 * time.Second),
	})

	return pCtx
}

func findTraceByAssetID(traces []lineage.Trace, assetID string) *lineage.Trace {
	for i := range traces {
		if traces[i].AssetID == assetID {
			return &traces[i]
		}
	}
	return nil
}

func hasTraceContributor(contributors []lineage.TraceContributor, assetID, enumerationID, seedID, source string) bool {
	for _, contributor := range contributors {
		if contributor.AssetID == assetID && contributor.EnumerationID == enumerationID && contributor.SeedID == seedID && contributor.Source == source {
			return true
		}
	}
	return false
}

func traceSectionContains(sections []lineage.TraceSection, title, fragment string) bool {
	for _, section := range sections {
		if section.Title != title {
			continue
		}
		for _, item := range section.Items {
			if strings.Contains(item, fragment) {
				return true
			}
		}
	}
	return false
}

func traceHasNode(nodes []lineage.TraceNode, id, kind string) bool {
	for _, node := range nodes {
		if node.ID == id && node.Kind == kind {
			return true
		}
	}
	return false
}

func traceHasNodeWithSource(nodes []lineage.TraceNode, source string) bool {
	for _, node := range nodes {
		if node.Kind == "observation" && node.Label == source {
			return true
		}
	}
	return false
}

func traceNodeSectionContains(nodes []lineage.TraceNode, label, title, fragment string) bool {
	for _, node := range nodes {
		if node.Label != label {
			continue
		}
		if traceSectionContains(node.Details, title, fragment) {
			return true
		}
	}
	return false
}

func traceNodeSectionContainsWithKind(nodes []lineage.TraceNode, kind, label, title, fragment string) bool {
	for _, node := range nodes {
		if node.Kind != kind || node.Label != label {
			continue
		}
		if traceSectionContains(node.Details, title, fragment) {
			return true
		}
	}
	return false
}

func countTraceNodesByKind(nodes []lineage.TraceNode, kind string) int {
	count := 0
	for _, node := range nodes {
		if node.Kind == kind {
			count++
		}
	}
	return count
}

func findRowByIdentifier(rows []Row, identifier string) *Row {
	for i := range rows {
		if rows[i].Identifier == identifier {
			return &rows[i]
		}
	}
	return nil
}

func findCanonicalAssetID(assets []models.Asset, assetType models.AssetType, identifier string) string {
	for _, asset := range assets {
		if asset.Type == assetType && asset.Identifier == identifier {
			return asset.ID
		}
	}
	return ""
}

func hasEvidenceGroup(groups []EvidenceGroup, title, itemFragment string) bool {
	for _, group := range groups {
		if group.Title != title {
			continue
		}
		if itemFragment == "" {
			return true
		}
		for _, item := range group.Items {
			if strings.Contains(item, itemFragment) {
				return true
			}
		}
	}
	return false
}

func snapshotHasJudgeCandidate(summary *lineage.JudgeSummary, root string, accepted bool) bool {
	if summary == nil {
		return false
	}
	for _, group := range summary.Groups {
		candidates := group.Discarded
		if accepted {
			candidates = group.Accepted
		}
		for _, candidate := range candidates {
			if candidate.Root == root {
				return true
			}
		}
	}
	return false
}

func snapshotHasRow(rows []Row, identifier string) bool {
	for _, row := range rows {
		if row.Identifier == identifier {
			return true
		}
	}
	return false
}

func findJudgeGroup(summary *lineage.JudgeSummary, collector, scenario string) *lineage.JudgeGroup {
	if summary == nil {
		return nil
	}
	for i := range summary.Groups {
		if summary.Groups[i].Collector == collector && summary.Groups[i].Scenario == scenario {
			return &summary.Groups[i]
		}
	}
	return nil
}
