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

	rows := make([]models.VisualizerRow, 0, len(pCtx.Assets))
	for _, asset := range sortedAssetsForExport(pCtx.Assets) {
		enum := enumByID[asset.EnumerationID]
		classified := classifyAsset(asset)
		rows = append(rows, models.VisualizerRow{
			AssetID:       asset.ID,
			Identifier:    asset.Identifier,
			AssetType:     string(asset.Type),
			DomainKind:    string(classified.domainKind),
			ApexDomain:    classified.apexDomain,
			Source:        asset.Source,
			EnumerationID: asset.EnumerationID,
			SeedID:        enum.SeedID,
			Status:        enum.Status,
			DiscoveryDate: asset.DiscoveryDate,
			Details:       buildVisualizerDetails(asset),
		})
	}

	sort.SliceStable(rows, func(i, j int) bool {
		if visualizerRowGroup(rows[i]) != visualizerRowGroup(rows[j]) {
			return visualizerRowGroup(rows[i]) < visualizerRowGroup(rows[j])
		}
		if rows[i].ApexDomain != rows[j].ApexDomain {
			return rows[i].ApexDomain < rows[j].ApexDomain
		}
		if rows[i].DiscoveryDate.Equal(rows[j].DiscoveryDate) {
			return rows[i].Identifier < rows[j].Identifier
		}
		return rows[i].DiscoveryDate.After(rows[j].DiscoveryDate)
	})

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
		Rows: rows,
	}
}

func visualizerRowGroup(row models.VisualizerRow) int {
	if row.AssetType == string(models.AssetTypeDomain) {
		switch row.DomainKind {
		case string(models.DomainKindApex):
			return exportGroupApexDomain
		case string(models.DomainKindSubdomain):
			return exportGroupSubdomain
		}
	}

	return exportGroupIP
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
    .field select {
      width: 100%;
      border: 1px solid rgba(126, 59, 0, 0.14);
      border-radius: 14px;
      padding: 0.85rem 0.95rem;
      font: inherit;
      color: var(--ink);
      background: var(--panel-strong);
    }

    .field input:focus,
    .field select:focus {
      outline: 2px solid rgba(190, 106, 21, 0.22);
      border-color: rgba(190, 106, 21, 0.35);
    }

    .summary {
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(170px, 1fr));
      gap: 0.85rem;
      padding: 1rem;
      margin-bottom: 1.25rem;
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
    }

    .table-meta {
      display: flex;
      flex-wrap: wrap;
      align-items: center;
      justify-content: space-between;
      gap: 0.75rem;
      margin-bottom: 0.85rem;
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
        <input id="search-input" type="search" placeholder="Filter identifier, apex domain, details, source, seed, or enumeration">
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
        <label for="source-filter">Source</label>
        <select id="source-filter">
          <option value="">All sources</option>
        </select>
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
        <div class="muted" id="table-caption">No archived runs loaded.</div>
        <div id="download-links"></div>
      </div>
      <div class="table-wrap">
        <table>
          <thead>
            <tr>
              <th><button type="button" data-key="identifier">Identifier</button></th>
              <th><button type="button" data-key="domain_kind">Domain Kind</button></th>
              <th><button type="button" data-key="apex_domain">Apex Domain</button></th>
              <th><button type="button" data-key="asset_type">Type</button></th>
              <th><button type="button" data-key="source">Source</button></th>
              <th><button type="button" data-key="enumeration_id">Enumeration</button></th>
              <th><button type="button" data-key="seed_id">Seed</button></th>
              <th><button type="button" data-key="status">Status</button></th>
              <th><button type="button" data-key="discovery_date">Discovered</button></th>
              <th><button type="button" data-key="details">Details</button></th>
            </tr>
          </thead>
          <tbody id="results-body"></tbody>
        </table>
      </div>
      <p id="empty-state">No rows match the active filters.</p>
    </section>
  </main>

  <script>
    const runs = {{.RunsJSON}};
    const state = {
      runId: runs[0] ? runs[0].id : "",
      search: "",
      type: "",
      domainKind: "",
      source: "",
      sortKey: "discovery_date",
      sortDirection: "desc"
    };

    const runSelect = document.getElementById("run-select");
    const searchInput = document.getElementById("search-input");
    const typeFilter = document.getElementById("type-filter");
    const domainKindFilter = document.getElementById("domain-kind-filter");
    const sourceFilter = document.getElementById("source-filter");
    const body = document.getElementById("results-body");
    const emptyState = document.getElementById("empty-state");
    const downloadLinks = document.getElementById("download-links");
    const tableCaption = document.getElementById("table-caption");

    document.getElementById("run-count").textContent = String(runs.length);

    const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: "base" });

    function currentRun() {
      return runs.find((run) => run.id === state.runId) || runs[0] || null;
    }

    function fillRunSelect() {
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

    function normalize(value) {
      return String(value || "").toLowerCase();
    }

    function formatDomainKind(value) {
      return String(value || "")
        .replaceAll("_", " ")
        .replace(/\b\w/g, (char) => char.toUpperCase());
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
        .filter((row) => !state.source || row.source === state.source)
        .filter((row) => {
          if (!state.search) {
            return true;
          }
          return normalize([
            row.identifier,
            row.domain_kind,
            row.apex_domain,
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
      body.innerHTML = "";

      document.getElementById("selected-run").textContent = run ? run.label : "No runs";
      document.getElementById("asset-count").textContent = String(run ? run.asset_count : 0);
      document.getElementById("enumeration-count").textContent = String(run ? run.enumeration_count : 0);
      document.getElementById("seed-count").textContent = String(run ? run.seed_count : 0);
      document.getElementById("visible-count").textContent = String(rows.length);
      tableCaption.textContent = run ? "Showing " + rows.length + " of " + run.rows.length + " rows from " + run.label + "." : "No archived runs loaded.";

      renderDownloads(run);

      rows.forEach((row) => {
        const tr = document.createElement("tr");
        const discovered = row.discovery_date ? new Date(row.discovery_date).toLocaleString() : "";
        const domainKind = row.domain_kind
          ? "<span class=\"pill\">" + escapeHTML(formatDomainKind(row.domain_kind)) + "</span>"
          : "<span class=\"muted\">-</span>";
        const apexDomain = row.apex_domain
          ? escapeHTML(row.apex_domain)
          : "<span class=\"muted\">-</span>";
        tr.innerHTML = [
          "<td><strong>", escapeHTML(row.identifier), "</strong><br><span class=\"muted\">", escapeHTML(row.asset_id), "</span></td>",
          "<td>", domainKind, "</td>",
          "<td>", apexDomain, "</td>",
          "<td><span class=\"pill\">", escapeHTML(row.asset_type || "unknown"), "</span></td>",
          "<td>", escapeHTML(row.source), "</td>",
          "<td>", escapeHTML(row.enumeration_id), "</td>",
          "<td>", escapeHTML(row.seed_id), "</td>",
          "<td>", escapeHTML(row.status), "</td>",
          "<td>", escapeHTML(discovered), "</td>",
          "<td>", escapeHTML(row.details), "</td>"
        ].join("");
        body.appendChild(tr);
      });

      emptyState.style.display = rows.length === 0 ? "block" : "none";
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
      refillFilter(sourceFilter, uniqueValues(rows, "source"), "All sources", state.source);
      state.source = sourceFilter.value;
    }

    function updateSortIndicators() {
      document.querySelectorAll("thead button").forEach((button) => {
        const key = button.dataset.key;
        const suffix = key === state.sortKey ? (state.sortDirection === "asc" ? " ▲" : " ▼") : "";
        const labels = {
          identifier: "Identifier",
          domain_kind: "Domain Kind",
          apex_domain: "Apex Domain",
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
      renderTable();
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

    sourceFilter.addEventListener("change", (event) => {
      state.source = event.target.value;
      renderTable();
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

    fillRunSelect();
    updateFiltersForRun();
    renderTable();
  </script>
</body>
</html>
`))
