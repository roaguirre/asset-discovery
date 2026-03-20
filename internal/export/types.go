package export

import (
	"time"

	"asset-discovery/internal/models"
	"asset-discovery/internal/tracing/lineage"
)

type DomainKind string

const (
	DomainKindRegistrable DomainKind = "registrable"
	DomainKindSubdomain   DomainKind = "subdomain"
)

type ExportAsset struct {
	models.Asset
	DomainKind        DomainKind `json:"domain_kind,omitempty"`
	RegistrableDomain string     `json:"registrable_domain,omitempty"`
}

type Downloads struct {
	JSON string `json:"json,omitempty"`
	CSV  string `json:"csv,omitempty"`
	XLSX string `json:"xlsx,omitempty"`
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

type Run struct {
	RunSummary
	Rows         []Row                 `json:"rows"`
	Traces       []lineage.Trace       `json:"traces,omitempty"`
	JudgeSummary *lineage.JudgeSummary `json:"judge_summary,omitempty"`
}

type ClassifiedAsset struct {
	DomainKind        DomainKind
	RegistrableDomain string
}
