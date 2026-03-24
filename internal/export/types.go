package export

import (
	exportshared "asset-discovery/internal/export/shared"
	"asset-discovery/internal/export/visualizer"
	"asset-discovery/internal/models"
)

type DomainKind = exportshared.DomainKind

const (
	DomainKindRegistrable = exportshared.DomainKindRegistrable
	DomainKindSubdomain   = exportshared.DomainKindSubdomain
)

type ExportAsset struct {
	models.Asset
	DomainKind        DomainKind `json:"domain_kind,omitempty"`
	RegistrableDomain string     `json:"registrable_domain,omitempty"`
}

type Downloads = visualizer.Downloads
type EvidenceGroup = visualizer.EvidenceGroup
type RunSummary = visualizer.RunSummary
type Manifest = visualizer.Manifest
type Row = visualizer.Row
type Run = visualizer.Run
type ClassifiedAsset = exportshared.ClassifiedAsset
