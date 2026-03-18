package models

// DomainKind distinguishes registrable domains from subdomains in exported domain assets.
type DomainKind string

const (
	DomainKindRegistrable DomainKind = "registrable"
	DomainKindSubdomain   DomainKind = "subdomain"
)

// ExportAsset is the flat JSON export shape with explicit domain classification metadata.
type ExportAsset struct {
	Asset
	DomainKind        DomainKind `json:"domain_kind,omitempty"`
	RegistrableDomain string     `json:"registrable_domain,omitempty"`
}
