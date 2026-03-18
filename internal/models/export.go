package models

// DomainKind distinguishes apex domains from subdomains in exported domain assets.
type DomainKind string

const (
	DomainKindApex      DomainKind = "apex"
	DomainKindSubdomain DomainKind = "subdomain"
)

// ExportAsset is the flat JSON export shape with explicit domain classification metadata.
type ExportAsset struct {
	Asset
	DomainKind DomainKind `json:"domain_kind,omitempty"`
	ApexDomain string     `json:"apex_domain,omitempty"`
}
