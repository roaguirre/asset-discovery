package visualizer

import (
	"time"

	"asset-discovery/internal/tracing/lineage"
)

type Downloads struct {
	JSON string `json:"json,omitempty"`
	CSV  string `json:"csv,omitempty"`
	XLSX string `json:"xlsx,omitempty"`
}

type EvidenceGroup struct {
	Title string   `json:"title"`
	Items []string `json:"items,omitempty"`
}

type RunSummary struct {
	ID               string    `json:"id"`
	Label            string    `json:"label"`
	CreatedAt        time.Time `json:"created_at"`
	AssetCount       int       `json:"asset_count"`
	EnumerationCount int       `json:"enumeration_count"`
	SeedCount        int       `json:"seed_count"`
	DataPath         string    `json:"data_path"`
	Downloads        Downloads `json:"downloads,omitempty"`
}

type Manifest struct {
	Runs []RunSummary `json:"runs"`
}

type Row struct {
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

type Run struct {
	RunSummary
	Rows         []Row                 `json:"rows"`
	Traces       []lineage.Trace       `json:"traces,omitempty"`
	JudgeSummary *lineage.JudgeSummary `json:"judge_summary,omitempty"`
}
