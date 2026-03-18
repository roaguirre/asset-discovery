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
	AssetID       string    `json:"asset_id"`
	Identifier    string    `json:"identifier"`
	AssetType     string    `json:"asset_type"`
	DomainKind    string    `json:"domain_kind,omitempty"`
	ApexDomain    string    `json:"apex_domain,omitempty"`
	Source        string    `json:"source"`
	EnumerationID string    `json:"enumeration_id"`
	SeedID        string    `json:"seed_id"`
	Status        string    `json:"status"`
	DiscoveryDate time.Time `json:"discovery_date,omitempty"`
	Details       string    `json:"details,omitempty"`
}

// VisualizerRun stores the full dataset for a single visualizer snapshot.
type VisualizerRun struct {
	VisualizerRunSummary
	Rows []VisualizerRow `json:"rows"`
}
