    const initialHash = parseHash();
    const state = {
      runId: initialHash.runId || (runs[0] ? runs[0].id : ""),
      search: "",
      type: "",
      domainKind: "",
      resolutionStatus: "",
      sources: [],
      view: initialHash.view,
      traceAssetId: initialHash.assetId,
      traceNodeId: "",
      sortKey: "discovery_date",
      sortDirection: "desc",
      expandedRows: new Set(),
      expandedDomainGroups: new Set()
    };

    const runSelect = document.getElementById("run-select");
    const searchInput = document.getElementById("search-input");
    const domainKindFilter = document.getElementById("domain-kind-filter");
    const resolutionStatusField = document.getElementById("resolution-status-field");
    const resolutionStatusFilter = document.getElementById("resolution-status-filter");
    const sourceFilter = document.getElementById("source-filter");
    const sourceFilterTrigger = document.getElementById("source-filter-trigger");
    const sourceFilterMenu = document.getElementById("source-filter-menu");
    const sourceFilterOptions = document.getElementById("source-filter-options");
    const body = document.getElementById("results-body");
    const resultsView = document.getElementById("results-view");
    const traceView = document.getElementById("trace-view");
    const domainsViewButton = document.getElementById("domains-view-button");
    const ipsViewButton = document.getElementById("ips-view-button");
    const resultsHead = document.getElementById("results-head");
    const judgeViewButton = document.getElementById("judge-view-button");
    const traceViewButton = document.getElementById("trace-view-button");
    const traceBackButton = document.getElementById("trace-back-button");
    const traceTitle = document.getElementById("trace-title");
    const traceSubtitle = document.getElementById("trace-subtitle");
    const traceSummary = document.getElementById("trace-summary");
    const traceTree = document.getElementById("trace-tree");
    const tracePanel = document.getElementById("trace-panel");
    const traceRelated = document.getElementById("trace-related");
    const emptyState = document.getElementById("empty-state");
    const downloadLinks = document.getElementById("download-links");
    const tableCaption = document.getElementById("table-caption");
    const judgeShell = document.getElementById("judge-shell");
    const judgeCaption = document.getElementById("judge-caption");
    const judgeGroups = document.getElementById("judge-groups");
    const appTooltip = document.getElementById("app-tooltip");
    const llmSummaryText = document.getElementById("llm-summary-text");

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
      "sitemap_collector": "Hosts and judged cross-root candidates discovered from robots.txt and sitemap documents.",
      "reverse_registration_collector": "Candidate sibling domains discovered through certificate transparency and RDAP overlap.",
      "asn_cidr_collector": "PTR-derived domains and roots discovered by pivoting through known ASN and CIDR network ranges.",
      "domain_enricher": "DNS and RDAP enrichment backfill that also materializes IP assets from resolved A and AAAA records.",
      "ip_enricher": "PTR, ASN, organization, and CIDR enrichment backfill applied to canonical IP assets.",
      "crawler_collector": "Assets discovered by crawling links from already-discovered web pages."
    });

    function parseHash() {
      const value = String(window.location.hash || "").replace(/^#/, "");
      if (value === "ips") { return { view: "ips", runId: "", assetId: "" }; }
      if (value === "judge") { return { view: "judge", runId: "", assetId: "" }; }
      if (!value.startsWith("trace/")) {
        return { view: "domains", runId: "", assetId: "" };
      }
      const parts = value.split("/");
      if (parts.length < 3) {
        return { view: "domains", runId: "", assetId: "" };
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
      } else if (state.view === "ips") {
        url.hash = "ips";
      } else if (state.view === "judge") {
        url.hash = "judge";
      } else {
        url.hash = "";
      }
      window.history.replaceState(null, "", url);
    }

    function currentRun() {
      return runs.find((run) => run.id === state.runId) || runs[0] || null;
    }

    function currentTrace(run) {
      if (!run || !Array.isArray(run.traces)) { return null; }
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
          if (!part || seen.has(part)) { return false; }
          seen.add(part);
          return true;
        });
    }

    function uniqueSourceValues(rows) {
      const values = new Set();
      rows.forEach((row) => { splitSources(row.source).forEach((source) => values.add(source)); });
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
      select.value = values.includes(activeValue) ? activeValue : "";
    }

    function sourceFilterLabel() {
      if (state.sources.length === 0) { return "All sources"; }
      if (state.sources.length === 1) { return state.sources[0]; }
      return String(state.sources.length) + " sources selected";
    }

    function syncSourceFilterUI() {
      const selected = new Set(state.sources);
      sourceFilterOptions.querySelectorAll("input[type=\"checkbox\"]").forEach((input) => {
        input.checked = selected.has(input.value);
      });
      const allToggle = sourceFilterMenu.querySelector("input[data-role=\"all\"]");
      if (allToggle) { allToggle.checked = state.sources.length === 0; }
      sourceFilterTrigger.textContent = sourceFilterLabel();
      sourceFilterTrigger.setAttribute("aria-expanded", sourceFilterMenu.hidden ? "false" : "true");
    }

    function hideTooltip() {
      appTooltip.dataset.visible = "false";
      appTooltip.setAttribute("aria-hidden", "true");
      appTooltip.textContent = "";
    }

    function showTooltip(target, text) {
      if (!target || !text) { hideTooltip(); return; }
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
      if (top + tooltipRect.height > window.innerHeight - gap) { top = rect.top - tooltipRect.height - gap; }
      if (top < gap) { top = gap; }
      appTooltip.style.left = Math.round(left) + "px";
      appTooltip.style.top = Math.round(top) + "px";
    }

    function tooltipTarget(event) {
      const target = event.target.closest("[data-tooltip]");
      if (!target || !document.body.contains(target)) { return null; }
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

    function rowsForSourceFilter(runRows) {
      if (!Array.isArray(runRows)) { return []; }
      if (state.view === "domains") { return runRows.filter((row) => row.asset_type === "domain"); }
      if (state.view === "ips") { return runRows.filter((row) => row.asset_type === "ip"); }
      return runRows;
    }

    function hasActiveDomainFilters() {
      return Boolean(state.search) || Boolean(state.domainKind) || Boolean(state.resolutionStatus) || state.sources.length > 0;
    }

    function buildDomainGroups(rows, allRows) {
      const groups = [];
      const byKey = new Map();
      const summaryByKey = new Map();

      (Array.isArray(allRows) ? allRows : rows).forEach((row) => {
        const key = row.registrable_domain || row.identifier;
        if (!key || row.domain_kind !== "registrable" || summaryByKey.has(key)) { return; }
        summaryByKey.set(key, row);
      });

      rows.forEach((row) => {
        const key = row.registrable_domain || row.identifier;
        if (!key) { return; }

        let group = byKey.get(key);
        if (!group) {
          group = { key, rows: [] };
          byKey.set(key, group);
          groups.push(group);
        }
        group.rows.push(row);
      });

      groups.forEach((group) => {
        const summaryRow = summaryByKey.get(group.key) || null;
        const visibleSummaryRow = summaryRow
          ? group.rows.find((row) => row.asset_id === summaryRow.asset_id) || null
          : null;
        const others = group.rows.filter((row) => !visibleSummaryRow || row.asset_id !== visibleSummaryRow.asset_id);
        group.summaryRow = summaryRow;
        group.rows = visibleSummaryRow ? [visibleSummaryRow].concat(others) : group.rows.slice();
      });

      return groups;
    }

    function isDomainGroupExpanded(groupKey) {
      return hasActiveDomainFilters() || state.expandedDomainGroups.has(groupKey);
    }

    function displayedDomainRowCount(groups) {
      return groups.reduce((count, group) => {
        return count + 1 + (isDomainGroupExpanded(group.key) ? group.rows.length : 0);
      }, 0);
    }

    function normalize(value) { return String(value || "").toLowerCase(); }

    function describeSource(value) {
      return sourceDescriptions[value] || ("Collected from " + String(value || "an unknown source") + ".");
    }

    function formatDomainKind(value) {
      return String(value || "").replaceAll("_", " ").replace(/\b\w/g, (char) => char.toUpperCase());
    }

    function formatResolutionStatus(value) {
      return String(value || "").replaceAll("_", " ").replace(/\b\w/g, (char) => char.toUpperCase());
    }

    function renderSourceCell(value) {
      const sources = splitSources(value);
      if (sources.length === 0) { return "<span class=\"muted\">-</span>"; }
      return "<div class=\"source-list\">" + sources.map((source) => {
        return "<span class=\"pill source-pill\" data-tooltip=\"" + escapeHTML(describeSource(source)) + "\">" + escapeHTML(source) + "</span>";
      }).join("") + "</div>";
    }

    /* --- Detail Parsing --- */
    function parseDetails(details) {
      if (!details) { return []; }
      return details.split(" | ").map((part) => {
        const idx = part.indexOf(" ");
        if (idx < 0) { return { label: "Info", value: part }; }
        return { label: part.substring(0, idx), value: part.substring(idx + 1) };
      });
    }

    function renderOwnershipBadge(value) {
      const normalized = String(value || "").trim().toLowerCase();
      const label = normalized ? humanizeKey(normalized) : "Unclassified";
      const tone = normalized ? " ownership-" + normalized.replaceAll("_", "-") : "";
      return "<span class=\"pill ownership-pill" + tone + "\">" + escapeHTML(label) + "</span>";
    }

    function renderEvidenceGroups(groups) {
      if (!Array.isArray(groups) || groups.length === 0) {
        return "";
      }
      return "<div class=\"detail-evidence-groups\">" + groups.map((group) => {
        const items = Array.isArray(group.items) ? group.items : [];
        return [
          "<section class=\"detail-evidence-group\">",
          "<div class=\"detail-preview-label\">" + escapeHTML(group.title || "Evidence") + "</div>",
          items.length > 0
            ? "<div class=\"detail-evidence-items\">" + items.map((item) => "<div class=\"detail-evidence-item\">" + escapeHTML(item) + "</div>").join("") + "</div>"
            : "<div class=\"detail-evidence-item muted\">No exported values.</div>",
          "</section>",
        ].join("");
      }).join("") + "</div>";
    }

    function renderDetailPanel(row, run) {
      const trace = run && Array.isArray(run.traces)
        ? run.traces.find((t) => t.asset_id === row.asset_id)
        : null;
      const detailPairs = parseDetails(row.details).filter((pair) => pair.label !== "Ownership" && pair.label !== "Reason");
      const previewPairs = detailPairs.slice(0, 4);
      const groupedEvidence = renderEvidenceGroups(row.evidence_groups);
      const contributors = trace && Array.isArray(trace.contributors) ? trace.contributors : [];
      const related = trace && Array.isArray(trace.related) ? trace.related : [];
      const nodes = traceNodes(trace);
      const observationCount = nodes.filter((node) => node.kind === "observation").length;
      const relationCount = nodes.filter((node) => node.kind === "relation").length;
      const enrichmentCount = nodes.filter((node) => node.kind === "enrichment").length;
      const colSpan = row.asset_type === "ip" ? 8 : 7;

      const identityBadges = [
        row.asset_type === "domain" && row.domain_kind ? "<span class=\"pill pill-subtle\">" + escapeHTML(formatDomainKind(row.domain_kind)) + "</span>" : "",
        row.asset_type === "domain" && row.registrable_domain ? "<span class=\"pill pill-subtle\">" + escapeHTML(row.registrable_domain) + "</span>" : "",
        row.asset_type === "domain" && row.resolution_status ? "<span class=\"pill pill-subtle\">" + escapeHTML(formatResolutionStatus(row.resolution_status)) + "</span>" : "",
        row.asset_type === "ip" && row.ptr ? "<span class=\"pill pill-subtle\">" + escapeHTML(row.ptr) + "</span>" : "",
        renderOwnershipBadge(row.ownership_state),
      ].filter(Boolean).join("");

      const evidenceHTML = groupedEvidence || (previewPairs.length > 0
        ? "<ul class=\"detail-preview-list\">" + previewPairs.map((pair) => {
            return "<li><span class=\"detail-preview-label\">" + escapeHTML(pair.label) + "</span><span class=\"detail-preview-value\">" + escapeHTML(pair.value) + "</span></li>";
          }).join("") + "</ul>"
        : "<p class=\"muted\">No additional evidence preview was exported for this row.</p>");
      const contributorSummary = [
        "<div class=\"detail-summary-meta\">",
        "<span><strong>Enumeration</strong> " + escapeHTML(row.enumeration_id || "-") + "</span>",
        "<span><strong>Seed</strong> " + escapeHTML(row.seed_id || "-") + "</span>",
        row.asset_type === "ip" ? "<span><strong>ASN</strong> " + escapeHTML(row.asn ? String(row.asn) : "-") + "</span>" : "",
        row.asset_type === "ip" ? "<span><strong>Org</strong> " + escapeHTML(row.organization || "-") + "</span>" : "",
        "</div>",
        "<div class=\"detail-summary-sources\">",
        "<div><strong>Discovered By</strong>" + renderSourceCell(row.discovered_by || row.source) + "</div>",
        "<div><strong>Enriched By</strong>" + renderSourceCell(row.enriched_by) + "</div>",
        "</div>",
      ].join("");

      const traceStats = [
        contributors.length ? String(contributors.length) + " contributor" + (contributors.length === 1 ? "" : "s") : "",
        observationCount ? String(observationCount) + " observation" + (observationCount === 1 ? "" : "s") : "",
        relationCount ? String(relationCount) + " relation" + (relationCount === 1 ? "" : "s") : "",
        enrichmentCount ? String(enrichmentCount) + " enrichment stage" + (enrichmentCount === 1 ? "" : "s") : "",
      ].filter(Boolean);

      const relatedPreview = related.length > 0
        ? "<div class=\"detail-related-inline\">" + related.slice(0, 3).map((link) => {
            return "<span class=\"pill pill-subtle\">" + escapeHTML(link.identifier || link.asset_id) + "</span>";
          }).join("") + (related.length > 3 ? "<span class=\"pill pill-subtle\">+" + String(related.length - 3) + " more</span>" : "") + "</div>"
        : "<p class=\"muted\">No related assets were linked in this export.</p>";

      return [
        "<td colspan=\"" + String(colSpan) + "\">",
        "<div class=\"detail-panel\">",
        "<article class=\"detail-card detail-summary-card\">",
        "<div class=\"detail-summary-head\">",
        "<div>",
        "<div class=\"eyebrow\">Inline Summary</div>",
        "<h4>" + escapeHTML(row.identifier) + "</h4>",
        "<p class=\"muted\">Asset " + escapeHTML(row.asset_id) + " · " + escapeHTML(row.status || "unknown") + "</p>",
        "</div>",
        "<div class=\"detail-summary-badges\">" + identityBadges + "</div>",
        "</div>",
        row.inclusion_reason ? "<p class=\"detail-summary-reason\">" + escapeHTML(row.inclusion_reason) + "</p>" : "<p class=\"detail-summary-reason muted\">No inclusion reason was exported for this asset.</p>",
        contributorSummary,
        "</article>",
        "<article class=\"detail-card detail-compact-card\">",
        "<h4>Evidence Preview</h4>",
        evidenceHTML,
        "</article>",
        "<article class=\"detail-card detail-compact-card\">",
        "<h4>Trace Summary</h4>",
        (traceStats.length > 0 ? "<div class=\"detail-related-inline\">" + traceStats.map((item) => "<span class=\"pill pill-subtle\">" + escapeHTML(item) + "</span>").join("") + "</div>" : "<p class=\"muted\">Trace statistics are not available for this asset.</p>"),
        relatedPreview,
        "<div class=\"detail-actions\">",
        "<a href=\"" + escapeHTML(row.trace_path || "#") + "\" class=\"result-trace-link\" data-trace-link data-run-id=\"" + escapeHTML(state.runId) + "\" data-asset-id=\"" + escapeHTML(row.asset_id) + "\">Open Trace</a>",
        "</div>",
        "</article>",
        "</div>",
        "</td>",
      ].join("");
    }

    /* --- Trace View Rendering --- */
    function renderTraceSummary(trace) {
      const pills = [];
      const nodes = traceNodes(trace);
      const contributors = Array.isArray(trace.contributors) ? trace.contributors : [];
      const uniqueContributorValues = (key) => {
        const seen = new Set();
        return contributors
          .map((item) => String(item && item[key] || "").trim())
          .filter((value) => { if (!value || seen.has(value)) { return false; } seen.add(value); return true; });
      };
      if (trace.asset_type) { pills.push("<span class=\"pill\">" + escapeHTML(trace.asset_type) + "</span>"); }
      if (trace.domain_kind) { pills.push("<span class=\"pill\">" + escapeHTML(formatDomainKind(trace.domain_kind)) + "</span>"); }
      if (trace.registrable_domain) { pills.push("<span class=\"pill\">" + escapeHTML(trace.registrable_domain) + "</span>"); }
      if (trace.resolution_status) { pills.push("<span class=\"pill\">" + escapeHTML(formatResolutionStatus(trace.resolution_status)) + "</span>"); }
      if (trace.discovered_by) {
        splitSources(trace.discovered_by).forEach((source) => {
          pills.push("<span class=\"pill\">Discovered via " + escapeHTML(source) + "</span>");
        });
      }
      if (trace.enriched_by) {
        splitSources(trace.enriched_by).forEach((source) => {
          pills.push("<span class=\"pill\">Enriched via " + escapeHTML(source) + "</span>");
        });
      } else if (trace.source && !trace.discovered_by) {
        splitSources(trace.source).forEach((source) => {
          pills.push("<span class=\"pill\">" + escapeHTML(source) + "</span>");
        });
      }
      if (contributors.length > 0) {
        pills.push("<span class=\"pill\">" + escapeHTML(String(contributors.length)) + " contributor" + (contributors.length === 1 ? "" : "s") + "</span>");
        const enumerations = uniqueContributorValues("enumeration_id");
        if (enumerations.length === 1) { pills.push("<span class=\"pill\">Enum " + escapeHTML(enumerations[0]) + "</span>"); }
        else if (enumerations.length > 1) { pills.push("<span class=\"pill\">" + escapeHTML(String(enumerations.length)) + " enumerations</span>"); }
        const seeds = uniqueContributorValues("seed_id");
        if (seeds.length === 1) { pills.push("<span class=\"pill\">Seed " + escapeHTML(seeds[0]) + "</span>"); }
        else if (seeds.length > 1) { pills.push("<span class=\"pill\">" + escapeHTML(String(seeds.length)) + " seeds</span>"); }
      } else {
        if (trace.enumeration_id) { pills.push("<span class=\"pill\">Enum " + escapeHTML(trace.enumeration_id) + "</span>"); }
        if (trace.seed_id) { pills.push("<span class=\"pill\">Seed " + escapeHTML(trace.seed_id) + "</span>"); }
      }
      const observationCount = nodes.filter((node) => node.kind === "observation").length;
      const relationCount = nodes.filter((node) => node.kind === "relation").length;
      if (observationCount > 0) { pills.push("<span class=\"pill\">" + escapeHTML(String(observationCount)) + " observations</span>"); }
      if (relationCount > 0) { pills.push("<span class=\"pill\">" + escapeHTML(String(relationCount)) + " relations</span>"); }
      return pills.join("");
    }

    function traceNodes(trace) {
      return trace && Array.isArray(trace.nodes) ? trace.nodes : [];
    }

    function ensureTraceNodeSelection(trace) {
      const nodes = traceNodes(trace);
      if (nodes.length === 0) {
        state.traceNodeId = "";
        return null;
      }
      const existing = nodes.find((node) => node.id === state.traceNodeId);
      if (existing) { return existing; }
      state.traceNodeId = trace.root_node_id || nodes[0].id;
      return nodes.find((node) => node.id === state.traceNodeId) || nodes[0];
    }

    function currentTraceNode(trace) {
      return ensureTraceNodeSelection(trace);
    }

    function buildTraceChildren(trace) {
      const children = new Map();
      traceNodes(trace).forEach((node) => {
        const parentKey = node.parent_id || "__root__";
        if (!children.has(parentKey)) { children.set(parentKey, []); }
        children.get(parentKey).push(node);
      });
      return children;
    }

    function renderTraceTree(trace) {
      const nodes = traceNodes(trace);
      const selected = ensureTraceNodeSelection(trace);
      if (nodes.length === 0 || !selected) {
        return "<div class=\"trace-panel-empty\"><p class=\"muted\">This result does not include exported trace nodes.</p></div>";
      }

      const children = buildTraceChildren(trace);
      const rootParent = "__root__";
      return "<div class=\"trace-tree-list\">" + renderTraceNodeBranch(children, rootParent, selected.id, 0) + "</div>";
    }

    function renderTraceNodeBranch(children, parentID, selectedID, depth) {
      const nodes = children.get(parentID) || [];
      return nodes.map((node) => renderTraceNodeEntry(node, children, selectedID, depth)).join("");
    }

    function renderTraceNodeEntry(node, children, selectedID, depth) {
      const childHTML = renderTraceNodeBranch(children, node.id, selectedID, depth + 1);
      const badges = [node.kind ? humanizeKey(node.kind) : ""].concat(Array.isArray(node.badges) ? node.badges : []).filter(Boolean);
      return [
        "<div class=\"trace-tree-node\" style=\"--trace-depth:" + String(depth) + "\">",
        "<button type=\"button\" class=\"trace-node-button" + (node.id === selectedID ? " is-active" : "") + "\" data-trace-node-id=\"" + escapeHTML(node.id) + "\">",
        "<span class=\"trace-node-copy\">",
        "<span class=\"trace-node-label\">" + escapeHTML(node.label || "Trace Node") + "</span>",
        node.subtitle ? "<span class=\"trace-node-subtitle\">" + escapeHTML(node.subtitle) + "</span>" : "",
        "</span>",
        badges.length > 0 ? "<span class=\"trace-node-badges\">" + badges.map((badge) => "<span class=\"pill pill-subtle\">" + escapeHTML(badge) + "</span>").join("") + "</span>" : "",
        "</button>",
        childHTML ? "<div class=\"trace-tree-children\">" + childHTML + "</div>" : "",
        "</div>",
      ].join("");
    }

    function renderTraceNodePanel(trace) {
      const node = currentTraceNode(trace);
      if (!node) {
        return "<div class=\"trace-panel-empty\"><p class=\"muted\">Select a node to inspect its exported properties and evidence.</p></div>";
      }

      const details = Array.isArray(node.details) ? node.details : [];
      const meta = [
        node.kind ? "<span class=\"pill pill-subtle\">" + escapeHTML(humanizeKey(node.kind)) + "</span>" : "",
        node.linked_asset_id ? "<span class=\"pill pill-subtle\">Asset " + escapeHTML(node.linked_asset_id) + "</span>" : "",
        node.linked_observation_id ? "<span class=\"pill pill-subtle\">Observation " + escapeHTML(node.linked_observation_id) + "</span>" : "",
        node.linked_relation_id ? "<span class=\"pill pill-subtle\">Relation " + escapeHTML(node.linked_relation_id) + "</span>" : "",
      ].filter(Boolean).join("");

      return [
        "<article class=\"trace-panel-body\">",
        "<div class=\"trace-panel-hero\">",
        "<div>",
        "<h3>" + escapeHTML(node.label || "Trace Node") + "</h3>",
        node.subtitle ? "<p class=\"muted\">" + escapeHTML(node.subtitle) + "</p>" : "<p class=\"muted\">No subtitle exported for this node.</p>",
        "</div>",
        meta ? "<div class=\"trace-panel-meta\">" + meta + "</div>" : "",
        "</div>",
        details.length > 0 ? details.map((section) => renderTraceDetailSection(section)).join("") : "<div class=\"trace-card\"><h3>No Details</h3><p class=\"muted\">This node does not include additional exported details.</p></div>",
        "</article>",
      ].join("");
    }

    function renderTraceDetailSection(section) {
      const items = Array.isArray(section.items) ? section.items : [];
      return [
        "<article class=\"trace-card\">",
        "<h3>" + escapeHTML(section.title || "Trace Details") + "</h3>",
        items.length > 0 ? "<ul class=\"trace-items\">" + items.map((item) => "<li>" + escapeHTML(item) + "</li>").join("") + "</ul>" : "<p class=\"muted\">No exported items.</p>",
        "</article>",
      ].join("");
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
      if (state.view !== "trace") { return; }
      const trace = currentTrace(run);
      if (trace) {
        ensureTraceNodeSelection(trace);
        return;
      }
      const firstTrace = run && Array.isArray(run.traces) ? run.traces[0] : null;
      const firstRow = rows[0] || null;
      if (firstTrace) { state.traceAssetId = firstTrace.asset_id; state.traceNodeId = ""; return; }
      if (firstRow) { state.traceAssetId = firstRow.asset_id; state.traceNodeId = ""; return; }
      state.view = "domains";
      state.traceAssetId = "";
      state.traceNodeId = "";
    }

    function openTrace(runId, assetId) {
      if (!runId || !assetId) { return; }
      state.runId = runId;
      state.view = "trace";
      state.traceAssetId = assetId;
      state.traceNodeId = "";
      fillRunSelect();
      updateFiltersForRun();
      renderTable();
      syncHash();
    }

    function openTraceFromCurrentSelection() {
      const run = currentRun();
      const rows = visibleRows(run);
      if (!run) { return; }
      if (state.traceAssetId && currentTrace(run)) { state.view = "trace"; }
      else if (rows[0]) { state.view = "trace"; state.traceAssetId = rows[0].asset_id; }
      else if (Array.isArray(run.traces) && run.traces[0]) { state.view = "trace"; state.traceAssetId = run.traces[0].asset_id; }
      else { state.view = "domains"; state.traceAssetId = ""; }
      state.traceNodeId = "";
      renderTable();
      syncHash();
    }

    function compareRows(left, right) {
      if (state.view === "domains") {
         const leftGroup = left.registrable_domain || "";
         const rightGroup = right.registrable_domain || "";
         const groupDiff = collator.compare(leftGroup, rightGroup);
         if (groupDiff !== 0) { return groupDiff; }
      }

      const key = state.sortKey;
      const direction = state.sortDirection === "asc" ? 1 : -1;
      let leftValue = left[key] || "";
      let rightValue = right[key] || "";
      if (key === "discovery_date") {
        leftValue = leftValue ? Date.parse(leftValue) : 0;
        rightValue = rightValue ? Date.parse(rightValue) : 0;
        if (leftValue < rightValue) { return -1 * direction; }
        if (leftValue > rightValue) { return 1 * direction; }
        return collator.compare(left.identifier, right.identifier);
      }
      return collator.compare(String(leftValue), String(rightValue)) * direction;
    }

    function visibleRows(run) {
      if (!run) { return []; }
      return run.rows
        .filter((row) => {
          if (state.view === "domains") return row.asset_type === "domain";
          if (state.view === "ips") return row.asset_type === "ip";
          return true;
        })
        .filter((row) => !state.domainKind || row.domain_kind === state.domainKind)
        .filter((row) => {
          if (!state.resolutionStatus || row.asset_type !== "domain") { return true; }
          return row.resolution_status === state.resolutionStatus;
        })
        .filter((row) => {
          if (state.sources.length === 0) { return true; }
          const rowSources = splitSources(row.source);
          return state.sources.every((source) => rowSources.includes(source));
        })
        .filter((row) => {
          if (!state.search) { return true; }
          return normalize([
            row.identifier, row.domain_kind, row.registrable_domain,
            row.asset_type, row.source, row.discovered_by, row.enriched_by, row.enumeration_id, row.seed_id,
            row.resolution_status,
            row.status, row.details, row.ownership_state, row.inclusion_reason
          ].join(" ")).includes(state.search);
        })
        .slice()
        .sort(compareRows);
    }

    function renderDownloads(run) {
      downloadLinks.innerHTML = "";
      if (!run || !run.downloads) { return; }
      [["JSON", run.downloads.json], ["CSV", run.downloads.csv], ["XLSX", run.downloads.xlsx]].forEach(([label, href]) => {
        if (!href) { return; }
        const link = document.createElement("a");
        link.href = href;
        link.textContent = label;
        downloadLinks.appendChild(link);
      });
    }

    function humanizeKey(value) {
      return String(value || "").replaceAll("_", " ").replace(/\b\w/g, (char) => char.toUpperCase());
    }

    function formatConfidence(value) {
      const numeric = Number(value);
      if (!Number.isFinite(numeric) || numeric <= 0) { return ""; }
      return numeric.toFixed(2);
    }

    function renderJudgeCandidate(candidate, accepted) {
      const meta = [];
      if (candidate.kind) { meta.push("<span class=\"pill pill-subtle\">" + escapeHTML(humanizeKey(candidate.kind)) + "</span>"); }
      const confidence = formatConfidence(candidate.confidence);
      if (confidence) { meta.push("<span class=\"pill pill-subtle\">Confidence " + escapeHTML(confidence) + "</span>"); }
      meta.push("<span class=\"pill pill-subtle\">" + (candidate.explicit ? "Explicit" : "Implicit") + "</span>");
      const support = Array.isArray(candidate.support) && candidate.support.length > 0
        ? "<ul class=\"judge-support\">" + candidate.support.map((item) => "<li>" + escapeHTML(item) + "</li>").join("") + "</ul>"
        : "";
      return [
        "<details class=\"judge-item\">",
        "<summary class=\"judge-item-summary\">",
        "<div class=\"judge-item-head\">",
        "<strong>", escapeHTML(candidate.root || "unknown"), "</strong>",
        candidate.reason ? "<div class=\"judge-item-reason\">" + escapeHTML(candidate.reason) + "</div>"
          : "<div class=\"judge-item-reason\">No reason was returned for this candidate.</div>",
        "</div>",
        "<span class=\"pill ", accepted ? "pill-accepted" : "pill-discarded", "\">", accepted ? "Accepted" : "Discarded", "</span>",
        "</summary>",
        "<div class=\"judge-item-copy\">",
        meta.length > 0 ? "<div class=\"judge-meta\">" + meta.join("") + "</div>" : "",
        support,
        "</div>",
        "</details>",
      ].join("");
    }

    function renderJudgeGroup(group) {
      const seedLabel = group.seed_label || group.seed_id || "Unknown seed";
      const seedDomains = Array.isArray(group.seed_domains) && group.seed_domains.length > 0 ? group.seed_domains.join(", ") : "";
      const accepted = Array.isArray(group.accepted) ? group.accepted : [];
      const discarded = Array.isArray(group.discarded) ? group.discarded : [];
      const groupMeta = ["<span class=\"pill pill-subtle\">", escapeHTML(humanizeKey(group.collector || "judge")), "</span>"];
      if (group.scenario) { groupMeta.push("<span class=\"pill pill-subtle\">", escapeHTML(humanizeKey(group.scenario)), "</span>"); }
      if (seedDomains) { groupMeta.push("<span class=\"pill pill-subtle\">", escapeHTML(seedDomains), "</span>"); }
      const groupStats = [
        "<span class=\"pill pill-count\">Accepted " + String(accepted.length) + "</span>",
        "<span class=\"pill pill-count\">Discarded " + String(discarded.length) + "</span>",
      ];
      const renderColumn = (title, items, acceptedItems) => {
        if (items.length === 0) {
          return ["<section class=\"judge-column\">", "<h4>", escapeHTML(title), "</h4>", "<p class=\"muted\">No candidates in this bucket.</p>", "</section>"].join("");
        }
        return [
          "<section class=\"judge-column\">", "<h4>", escapeHTML(title), "</h4>",
          "<div class=\"judge-list\">", items.map((item) => renderJudgeCandidate(item, acceptedItems)).join(""), "</div>",
          "</section>"
        ].join("");
      };
      return [
        "<details class=\"judge-group\">",
        "<summary class=\"judge-group-toggle\">",
        "<div>",
        "<h3>", escapeHTML(humanizeKey(group.collector || "judge")), " for ", escapeHTML(seedLabel), "</h3>",
        group.seed_id && group.seed_id !== seedLabel ? "<div class=\"muted\">Seed ID: " + escapeHTML(group.seed_id) + "</div>" : "",
        "</div>",
        "<div>",
        "<div class=\"judge-group-stats\">", groupStats.join(""), "</div>",
        "<div class=\"judge-group-meta\">", groupMeta.join(""), "</div>",
        "</div>",
        "</summary>",
        "<div class=\"judge-columns\">",
        renderColumn("Accepted Candidates (" + String(accepted.length) + ")", accepted, true),
        renderColumn("Discarded Candidates (" + String(discarded.length) + ")", discarded, false),
        "</div>",
        "</details>",
      ].join("");
    }

    function renderJudgeSummary(run) {
      const summary = run && run.judge_summary ? run.judge_summary : null;
      const groups = summary && Array.isArray(summary.groups) ? summary.groups : [];
      judgeGroups.innerHTML = "";
      if (!summary || groups.length === 0) {
        judgeShell.hidden = true;
        judgeCaption.textContent = "No judge evaluations were captured for this run.";
        return;
      }
      judgeShell.hidden = false;
      judgeCaption.textContent =
        "Captured " + String(summary.evaluation_count || 0) +
        " judge evaluation" + ((summary.evaluation_count || 0) === 1 ? "" : "s") +
        " across " + String((summary.accepted_count || 0) + (summary.discarded_count || 0)) +
        " unique candidate roots.";
      judgeGroups.innerHTML = groups.map((group) => renderJudgeGroup(group)).join("");
    }

    /* --- LLM Summary --- */
    function renderLLMSummary(run) {
      if (!run) {
        llmSummaryText.textContent = "No run loaded.";
        return;
      }
      const domains = new Set();
      const sources = new Set();
      const types = { domain: 0, ip: 0 };
      const kinds = { registrable: 0, subdomain: 0 };
      (run.rows || []).forEach((row) => {
        if (row.registrable_domain) { domains.add(row.registrable_domain); }
        splitSources(row.source).forEach((s) => sources.add(s));
        if (row.asset_type === "domain") { types.domain++; } else { types.ip++; }
        if (row.domain_kind === "registrable") { kinds.registrable++; }
        else if (row.domain_kind === "subdomain") { kinds.subdomain++; }
      });
      const judge = run.judge_summary || {};
      const lines = [
        "Run: " + run.id,
        "Date: " + run.label,
        "Total Assets: " + run.asset_count,
        "  Domains: " + types.domain + " (registrable: " + kinds.registrable + ", subdomain: " + kinds.subdomain + ")",
        "  IPs: " + types.ip,
        "Unique Registrable Domains: " + domains.size + " [" + [...domains].sort().join(", ") + "]",
        "Sources Used: " + sources.size + " [" + [...sources].sort().join(", ") + "]",
        "Enumerations: " + (run.enumeration_count || 0),
        "Seeds: " + (run.seed_count || 0),
        "Judge Evaluations: " + (judge.evaluation_count || 0),
        "  Accepted Candidates: " + (judge.accepted_count || 0),
        "  Discarded Candidates: " + (judge.discarded_count || 0),
      ];
      llmSummaryText.textContent = lines.join("\n");
    }

    /* --- Main Render --- */
    function renderTable() {
      const run = currentRun();
      const rows = visibleRows(run);
      const allDomainRows = run ? rowsForSourceFilter(run.rows) : [];
      const domainGroups = state.view === "domains" ? buildDomainGroups(rows, allDomainRows) : [];
      const displayedCount = state.view === "domains" ? displayedDomainRowCount(domainGroups) : rows.length;
      ensureTraceSelection(run, rows);
      const trace = currentTrace(run);
      hideTooltip();
      body.innerHTML = "";

      document.getElementById("selected-run").textContent = run ? run.label : "No runs";
      document.getElementById("asset-count").textContent = String(run ? run.asset_count : 0);
      document.getElementById("enumeration-count").textContent = String(run ? run.enumeration_count : 0);
      document.getElementById("seed-count").textContent = String(run ? run.seed_count : 0);
      document.getElementById("visible-count").textContent = String(displayedCount);
      document.getElementById("judge-accepted-count").textContent = String(run && run.judge_summary ? run.judge_summary.accepted_count || 0 : 0);
      document.getElementById("judge-discarded-count").textContent = String(run && run.judge_summary ? run.judge_summary.discarded_count || 0 : 0);
      if (!run) {
        tableCaption.textContent = "No archived runs loaded.";
      } else if (state.view === "domains") {
        tableCaption.textContent = "Showing " + domainGroups.length + " registrable domains from " + rows.length + " matching domain assets in " + run.label + ".";
      } else {
        tableCaption.textContent = "Showing " + rows.length + " of " + run.rows.length + " rows from " + run.label + ".";
      }

      renderDownloads(run);

      const showTrace = state.view === "trace" && trace;
      const showJudge = state.view === "judge";
      const showDomains = state.view === "domains";
      const showIPs = state.view === "ips";
      const showResults = showDomains || showIPs || (!showTrace && !showJudge);

      renderJudgeSummary(run);
      document.getElementById("domain-kind-filter").closest(".field").hidden = !showDomains;
      judgeShell.hidden = !showJudge || (judgeShell.hidden);
      if (showJudge) { judgeShell.hidden = false; }
      resultsView.hidden = !showResults;
      traceView.hidden = !showTrace;

      domainsViewButton.classList.toggle("is-active", showDomains);
      ipsViewButton.classList.toggle("is-active", showIPs);
      judgeViewButton.classList.toggle("is-active", showJudge);
      traceViewButton.classList.toggle("is-active", showTrace);

      if (showTrace) {
        ensureTraceNodeSelection(trace);
        traceTitle.textContent = trace.identifier || trace.asset_id || "Result Trace";
        traceSubtitle.textContent = "Trace for asset " + String(trace.asset_id || "unknown") + ". Follow related results to pivot across the exported dataset.";
        traceSummary.innerHTML = renderTraceSummary(trace);
        traceTree.innerHTML = renderTraceTree(trace);
        tracePanel.innerHTML = renderTraceNodePanel(trace);
        traceRelated.innerHTML = renderTraceRelated(trace);
      } else {
        traceTitle.textContent = "Select a result";
        traceSubtitle.textContent = "Choose any result row to inspect its exported provenance, context, and related results.";
        traceSummary.innerHTML = "";
        traceTree.innerHTML = "";
        tracePanel.innerHTML = "<div class=\"trace-panel-empty\"><p class=\"muted\">No trace selected.</p></div>";
        traceRelated.innerHTML = "<p class=\"muted\">No trace selected.</p>";
      }

      resolutionStatusField.hidden = !showDomains;
      if (showResults) {
        if (showDomains) {
        resultsHead.innerHTML = `<tr>
          <th style="width:2.5rem"></th>
          <th><button type="button" data-key="identifier" data-tooltip="The domain or hostname identifier for this asset.">Identifier</button></th>
          <th><button type="button" data-key="domain_kind" data-tooltip="Whether this domain is a registrable root or a discovered subdomain.">Kind</button></th>
          <th><button type="button" data-key="resolution_status" data-tooltip="Whether the domain currently resolves, was enriched but did not resolve, or has not been checked yet.">Resolution</button></th>
          <th><button type="button" data-key="source" data-tooltip="Collectors and enrichers that contributed this exported result.">Source</button></th>
          <th><button type="button" data-key="status" data-tooltip="Merged enumeration status for the contributing discovery runs.">Status</button></th>
          <th><button type="button" data-key="discovery_date" data-tooltip="When this result was first observed in the current exported run.">Discovered</button></th>
        </tr>`;
      } else if (showIPs) {
        resultsHead.innerHTML = `<tr>
          <th style="width:2.5rem"></th>
          <th><button type="button" data-key="identifier" data-tooltip="The IP address identifier for this asset.">Identifier</button></th>
          <th><button type="button" data-key="asn" data-tooltip="Autonomous System Number associated with this IP address.">ASN</button></th>
          <th><button type="button" data-key="organization" data-tooltip="Organization name returned by the IP ownership enrichment lookup.">Organization</button></th>
          <th><button type="button" data-key="ptr" data-tooltip="Reverse DNS hostname returned for this IP address, when one exists.">PTR</button></th>
          <th><button type="button" data-key="source" data-tooltip="Collectors and enrichers that contributed this exported result.">Source</button></th>
          <th><button type="button" data-key="status" data-tooltip="Merged enumeration status for the contributing discovery runs.">Status</button></th>
          <th><button type="button" data-key="discovery_date" data-tooltip="When this result was first observed in the current exported run.">Discovered</button></th>
        </tr>`;
      }
        resultsHead.querySelectorAll("button").forEach((button) => {
          button.addEventListener("click", () => {
            const { key } = button.dataset;
            if (state.sortKey === key) { state.sortDirection = state.sortDirection === "asc" ? "desc" : "asc"; }
            else { state.sortKey = key; state.sortDirection = key === "discovery_date" ? "desc" : "asc"; }
            renderTable();
          });
        });
      }
      
      if (showDomains) {
        domainGroups.forEach((group) => {
          const expanded = isDomainGroupExpanded(group.key);
          const summaryRow = group.summaryRow || null;
          const displayRow = summaryRow || group.rows[0] || null;
          const childRows = group.rows.filter((row) => !summaryRow || row.asset_id !== summaryRow.asset_id);
          const summaryExpanded = Boolean(summaryRow) && state.expandedRows.has(summaryRow.asset_id);
          const summaryDiscovered = displayRow && displayRow.discovery_date ? new Date(displayRow.discovery_date).toLocaleString() : "";
          const summaryKind = displayRow && displayRow.domain_kind ? formatDomainKind(displayRow.domain_kind) : "Domain";
          const summaryResolution = displayRow ? formatResolutionStatus(displayRow.resolution_status || "-") : "-";
          const summarySource = displayRow ? renderSourceCell(displayRow.source) : "<span class=\"muted\">-</span>";
          const summaryStatus = displayRow ? escapeHTML(displayRow.status || "-") : "-";
          const summaryIdentifier = escapeHTML(group.key);
          const groupTr = document.createElement("tr");
          groupTr.className = "domain-group-row";
          groupTr.innerHTML = [
            "<td><div class=\"domain-group-controls\"><button type=\"button\" class=\"domain-group-toggle\" data-domain-group=\"" + escapeHTML(group.key) + "\" aria-expanded=\"" + (expanded ? "true" : "false") + "\">" + (expanded ? "▼" : "▶") + "</button></div></td>",
            "<td><div class=\"domain-group-summary\"><div class=\"domain-group-copy\">" + (summaryRow ? "<button type=\"button\" class=\"domain-summary-trigger\" data-summary-asset-id=\"" + escapeHTML(summaryRow.asset_id) + "\" aria-expanded=\"" + (summaryExpanded ? "true" : "false") + "\"><strong>" + summaryIdentifier + "</strong></button>" : "<strong>" + summaryIdentifier + "</strong>") + "<span class=\"pill\">" + group.rows.length + " asset" + (group.rows.length === 1 ? "" : "s") + "</span></div></div></td>",
            "<td><span class=\"pill\">" + escapeHTML(summaryKind) + "</span></td>",
            "<td><span class=\"pill pill-subtle\">" + escapeHTML(summaryResolution) + "</span></td>",
            "<td>" + summarySource + "</td>",
            "<td>" + summaryStatus + "</td>",
            "<td>" + escapeHTML(summaryDiscovered) + "</td>",
          ].join("");
          body.appendChild(groupTr);

          if (summaryExpanded && summaryRow) {
            const detailTr = document.createElement("tr");
            detailTr.className = "detail-row";
            detailTr.innerHTML = renderDetailPanel(summaryRow, run);
            body.appendChild(detailTr);
          }

          if (!expanded) {
            return;
          }

          childRows.forEach((row) => {
            const discovered = row.discovery_date ? new Date(row.discovery_date).toLocaleString() : "";
            const kindLabel = row.domain_kind ? formatDomainKind(row.domain_kind) : row.asset_type || "-";
            const isExpanded = state.expandedRows.has(row.asset_id);

            const tr = document.createElement("tr");
            tr.className = "domain-child-row";
            tr.innerHTML = [
              "<td></td>",
              "<td><div class=\"domain-child-identifier\"><button type=\"button\" class=\"domain-child-trigger\" data-child-asset-id=\"" + escapeHTML(row.asset_id) + "\" aria-expanded=\"" + (isExpanded ? "true" : "false") + "\"><strong>" + escapeHTML(row.identifier) + "</strong></button></div></td>",
              "<td><span class=\"pill\">" + escapeHTML(kindLabel) + "</span></td>",
              "<td><span class=\"pill pill-subtle\">" + escapeHTML(formatResolutionStatus(row.resolution_status || "-")) + "</span></td>",
              "<td>" + renderSourceCell(row.source) + "</td>",
              "<td>" + escapeHTML(row.status || "-") + "</td>",
              "<td>" + escapeHTML(discovered) + "</td>",
            ].join("");
            body.appendChild(tr);

            if (isExpanded) {
              const detailTr = document.createElement("tr");
              detailTr.className = "detail-row";
              detailTr.innerHTML = renderDetailPanel(row, run);
              body.appendChild(detailTr);
            }
          });
        });
      } else {
        rows.forEach((row) => {
          const discovered = row.discovery_date ? new Date(row.discovery_date).toLocaleString() : "";
          const isExpanded = state.expandedRows.has(row.asset_id);

          const tr = document.createElement("tr");
          tr.innerHTML = [
            "<td><button type=\"button\" class=\"detail-toggle\" data-asset-id=\"" + escapeHTML(row.asset_id) + "\">" + (isExpanded ? "▼" : "▶") + "</button></td>",
            "<td><strong>" + escapeHTML(row.identifier) + "</strong></td>",
            "<td>" + escapeHTML(row.asn ? String(row.asn) : "-") + "</td>",
            "<td>" + escapeHTML(row.organization || "-") + "</td>",
            "<td><span class=\"pill pill-subtle\">" + escapeHTML(row.ptr || "-") + "</span></td>",
            "<td>" + renderSourceCell(row.source) + "</td>",
            "<td>" + escapeHTML(row.status || "-") + "</td>",
            "<td>" + escapeHTML(discovered) + "</td>",
          ].join("");
          body.appendChild(tr);

          if (isExpanded) {
            const detailTr = document.createElement("tr");
            detailTr.className = "detail-row";
            detailTr.innerHTML = renderDetailPanel(row, run);
            body.appendChild(detailTr);
          }
        });
      }

      emptyState.style.display = showResults && (showDomains ? domainGroups.length === 0 : rows.length === 0) ? "block" : "none";
      updateSortIndicators();
      renderLLMSummary(run);
    }

    function updateFiltersForRun() {
      const run = currentRun();
      const rows = run ? run.rows : [];
      const domainRows = rows.filter((row) => row.asset_type === "domain");
      const sourceRows = rowsForSourceFilter(rows);
      refillFilter(domainKindFilter, uniqueValues(rows, "domain_kind"), "All domain kinds", state.domainKind);
      Array.from(domainKindFilter.options).forEach((option) => {
        if (option.value) { option.textContent = formatDomainKind(option.value); }
      });
      state.domainKind = domainKindFilter.value;
      refillFilter(resolutionStatusFilter, uniqueValues(domainRows, "resolution_status"), "All resolution states", state.resolutionStatus);
      Array.from(resolutionStatusFilter.options).forEach((option) => {
        if (option.value) { option.textContent = formatResolutionStatus(option.value); }
      });
      state.resolutionStatus = resolutionStatusFilter.value;
      refillSourceFilter(sourceRows);
    }

    function updateSortIndicators() {
      document.querySelectorAll("thead button").forEach((button) => {
        const key = button.dataset.key;
        const suffix = key === state.sortKey ? (state.sortDirection === "asc" ? " ▲" : " ▼") : "";
        const labels = {
          identifier: "Identifier",
          domain_kind: "Kind",
          resolution_status: "Resolution",
          asset_type: "Type",
          asn: "ASN",
          organization: "Organization",
          ptr: "PTR",
          source: "Source",
          status: "Status",
          discovery_date: "Discovered",
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

    /* --- Event Listeners --- */
    runSelect.addEventListener("change", (event) => {
      state.runId = event.target.value;
      state.expandedRows.clear();
      state.expandedDomainGroups.clear();
      state.traceNodeId = "";
      updateFiltersForRun();
      if (state.view === "trace") {
        const run = currentRun();
        const trace = currentTrace(run);
        if (!trace) {
          state.traceAssetId = run && Array.isArray(run.traces) && run.traces[0] ? run.traces[0].asset_id : "";
          if (!state.traceAssetId) { state.view = "domains"; }
        }
      }
      renderTable();
      syncHash();
    });

    searchInput.addEventListener("input", (event) => { state.search = normalize(event.target.value); renderTable(); });
    domainKindFilter.addEventListener("change", (event) => { state.domainKind = event.target.value; renderTable(); });
    resolutionStatusFilter.addEventListener("change", (event) => { state.resolutionStatus = event.target.value; renderTable(); });

    domainsViewButton.addEventListener("click", () => { state.view = "domains"; state.traceNodeId = ""; updateFiltersForRun(); renderTable(); syncHash(); });
    ipsViewButton.addEventListener("click", () => { state.view = "ips"; state.traceNodeId = ""; updateFiltersForRun(); renderTable(); syncHash(); });
    judgeViewButton.addEventListener("click", () => { state.view = "judge"; state.traceNodeId = ""; renderTable(); syncHash(); });
    traceViewButton.addEventListener("click", () => { openTraceFromCurrentSelection(); });
    traceBackButton.addEventListener("click", () => { state.view = "domains"; state.traceNodeId = ""; updateFiltersForRun(); renderTable(); syncHash(); });

    sourceFilter.addEventListener("click", (event) => { event.stopPropagation(); });

    sourceFilterTrigger.addEventListener("click", () => {
      sourceFilterMenu.hidden = !sourceFilterMenu.hidden;
      sourceFilter.classList.toggle("is-open", !sourceFilterMenu.hidden);
      syncSourceFilterUI();
    });

    sourceFilterMenu.addEventListener("change", (event) => {
      const target = event.target;
      if (!(target instanceof HTMLInputElement) || target.type !== "checkbox") { return; }
      if (target.dataset.role === "all") { state.sources = []; }
      else { state.sources = Array.from(sourceFilterOptions.querySelectorAll("input[type=\"checkbox\"]:checked")).map((input) => input.value); }
      syncSourceFilterUI();
      renderTable();
    });

    document.addEventListener("click", (event) => {
      const toggle = event.target.closest(".detail-toggle, .domain-summary-trigger, .domain-child-trigger");
      if (toggle) {
        const assetId = toggle.dataset.assetId || toggle.dataset.summaryAssetId || toggle.dataset.childAssetId;
        if (state.expandedRows.has(assetId)) { state.expandedRows.delete(assetId); }
        else { state.expandedRows.add(assetId); }
        renderTable();
        return;
      }

      const groupToggle = event.target.closest("[data-domain-group]");
      if (groupToggle) {
        const groupKey = groupToggle.dataset.domainGroup || "";
        if (!groupKey || hasActiveDomainFilters()) {
          return;
        }
        if (state.expandedDomainGroups.has(groupKey)) { state.expandedDomainGroups.delete(groupKey); }
        else { state.expandedDomainGroups.add(groupKey); }
        renderTable();
        return;
      }

      const link = event.target.closest("[data-trace-link]");
      if (link) { event.preventDefault(); openTrace(link.dataset.runId || state.runId, link.dataset.assetId); return; }

      const traceNode = event.target.closest("[data-trace-node-id]");
      if (traceNode) {
        state.traceNodeId = traceNode.dataset.traceNodeId || "";
        renderTable();
        return;
      }

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

    document.addEventListener("pointerover", (event) => { const target = tooltipTarget(event); if (target) { showTooltip(target, target.dataset.tooltip); } });
    document.addEventListener("pointermove", (event) => { const target = tooltipTarget(event); if (target) { showTooltip(target, target.dataset.tooltip); } });
    document.addEventListener("pointerout", (event) => { if (tooltipTarget(event)) { hideTooltip(); } });
    document.addEventListener("focusin", (event) => { const target = tooltipTarget(event); if (target) { showTooltip(target, target.dataset.tooltip); } });
    document.addEventListener("focusout", (event) => { if (tooltipTarget(event)) { hideTooltip(); } });

    window.addEventListener("scroll", hideTooltip, true);
    window.addEventListener("resize", hideTooltip);

    window.addEventListener("hashchange", () => {
      const next = parseHash();
      state.view = next.view;
      if (next.runId) { state.runId = next.runId; }
      state.traceAssetId = next.assetId;
      state.traceNodeId = "";
      fillRunSelect();
      updateFiltersForRun();
      renderTable();
    });

    fillRunSelect();
    updateFiltersForRun();
    renderTable();
