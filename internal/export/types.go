package export

import (
	exportshared "asset-discovery/internal/export/shared"
	"asset-discovery/internal/models"
)

// Downloads lists the file artifacts emitted for a completed run.
type Downloads struct {
	JSON string `json:"json,omitempty" firestore:"json,omitempty"`
	CSV  string `json:"csv,omitempty" firestore:"csv,omitempty"`
	XLSX string `json:"xlsx,omitempty" firestore:"xlsx,omitempty"`
}

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

type ClassifiedAsset = exportshared.ClassifiedAsset
