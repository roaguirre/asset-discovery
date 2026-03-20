package models

import "time"

// VisualizerDownloads contains optional links to exported artifacts for a run.
type VisualizerDownloads struct {
	JSON string `json:"json,omitempty"`
	CSV  string `json:"csv,omitempty"`
	XLSX string `json:"xlsx,omitempty"`
}

// VisualizerRunSummary describes a single archived export run.
type VisualizerRunSummary struct {
	ID               string              `json:"id"`
	Label            string              `json:"label"`
	CreatedAt        time.Time           `json:"created_at"`
	AssetCount       int                 `json:"asset_count"`
	EnumerationCount int                 `json:"enumeration_count"`
	SeedCount        int                 `json:"seed_count"`
	DataPath         string              `json:"data_path"`
	Downloads        VisualizerDownloads `json:"downloads,omitempty"`
}

// VisualizerManifest stores the archived runs displayed by the HTML viewer.
type VisualizerManifest struct {
	Runs []VisualizerRunSummary `json:"runs"`
}

// VisualizerRow is a flattened asset row rendered by the visualizer table.
type VisualizerRow struct {
	AssetID           string    `json:"asset_id"`
	Identifier        string    `json:"identifier"`
	AssetType         string    `json:"asset_type"`
	DomainKind        string    `json:"domain_kind,omitempty"`
	RegistrableDomain string    `json:"registrable_domain,omitempty"`
	Source            string    `json:"source"`
	EnumerationID     string    `json:"enumeration_id"`
	SeedID            string    `json:"seed_id"`
	Status            string    `json:"status"`
	DiscoveryDate     time.Time `json:"discovery_date,omitempty"`
	Details           string    `json:"details,omitempty"`
	TracePath         string    `json:"trace_path,omitempty"`
}

// VisualizerTraceSection groups a set of trace facts for one result.
type VisualizerTraceSection struct {
	Title string   `json:"title"`
	Items []string `json:"items,omitempty"`
}

// VisualizerTraceLink points to another related result inside the visualizer.
type VisualizerTraceLink struct {
	AssetID     string `json:"asset_id"`
	Identifier  string `json:"identifier"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	TracePath   string `json:"trace_path,omitempty"`
}

// VisualizerTraceContributor describes one raw contributor that fed a merged result.
type VisualizerTraceContributor struct {
	AssetID       string    `json:"asset_id,omitempty"`
	EnumerationID string    `json:"enumeration_id,omitempty"`
	SeedID        string    `json:"seed_id,omitempty"`
	SeedLabel     string    `json:"seed_label,omitempty"`
	Source        string    `json:"source,omitempty"`
	DiscoveryDate time.Time `json:"discovery_date,omitempty"`
}

// VisualizerTrace stores the exported trace payload for one result row.
type VisualizerTrace struct {
	AssetID           string                       `json:"asset_id"`
	Identifier        string                       `json:"identifier"`
	AssetType         string                       `json:"asset_type"`
	Source            string                       `json:"source"`
	EnumerationID     string                       `json:"enumeration_id"`
	SeedID            string                       `json:"seed_id"`
	DomainKind        string                       `json:"domain_kind,omitempty"`
	RegistrableDomain string                       `json:"registrable_domain,omitempty"`
	Contributors      []VisualizerTraceContributor `json:"contributors,omitempty"`
	Sections          []VisualizerTraceSection     `json:"sections,omitempty"`
	Related           []VisualizerTraceLink        `json:"related,omitempty"`
}

// VisualizerJudgeCandidate summarizes one accepted or discarded judge outcome.
type VisualizerJudgeCandidate struct {
	Root       string   `json:"root"`
	Confidence float64  `json:"confidence,omitempty"`
	Kind       string   `json:"kind,omitempty"`
	Reason     string   `json:"reason,omitempty"`
	Explicit   bool     `json:"explicit,omitempty"`
	Support    []string `json:"support,omitempty"`
}

// VisualizerJudgeGroup groups run-level judge analysis by collector and seed.
type VisualizerJudgeGroup struct {
	Collector   string                     `json:"collector"`
	SeedID      string                     `json:"seed_id,omitempty"`
	SeedLabel   string                     `json:"seed_label,omitempty"`
	SeedDomains []string                   `json:"seed_domains,omitempty"`
	Scenario    string                     `json:"scenario,omitempty"`
	Accepted    []VisualizerJudgeCandidate `json:"accepted,omitempty"`
	Discarded   []VisualizerJudgeCandidate `json:"discarded,omitempty"`
}

// VisualizerJudgeSummary stores accepted-vs-discarded judge analysis for a run.
type VisualizerJudgeSummary struct {
	EvaluationCount int                    `json:"evaluation_count"`
	AcceptedCount   int                    `json:"accepted_count"`
	DiscardedCount  int                    `json:"discarded_count"`
	Groups          []VisualizerJudgeGroup `json:"groups,omitempty"`
}

// VisualizerRun stores the full dataset for a single visualizer snapshot.
type VisualizerRun struct {
	VisualizerRunSummary
	Rows         []VisualizerRow         `json:"rows"`
	Traces       []VisualizerTrace       `json:"traces,omitempty"`
	JudgeSummary *VisualizerJudgeSummary `json:"judge_summary,omitempty"`
}
