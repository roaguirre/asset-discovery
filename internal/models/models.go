package models

import (
	"sync"
	"time"
)

// Seed represents the starting point for discovery.
// A Seed can contain various indicators that help OSINT collectors find assets.
type Seed struct {
	ID          string   `json:"id"`
	CompanyName string   `json:"company_name,omitempty"`
	Domains     []string `json:"domains,omitempty"` // e.g., ["google.com", "alphabet.com"]
	Address     string   `json:"address,omitempty"`
	Industry    string   `json:"industry,omitempty"`

	// Additional Discovery Vectors
	ASN  []int    `json:"asn,omitempty"`  // Autonomous System Numbers owned by the company
	CIDR []string `json:"cidr,omitempty"` // Known IP ranges (e.g., 192.168.1.0/24)

	// Metadata
	Tags []string `json:"tags,omitempty"` // e.g., ["internal", "acquisition", "out-of-scope"]
}

// Enumeration represents a specific discovery run for a Seed.
// A single Seed can have multiple Enumerations over time.
type Enumeration struct {
	ID        string    `json:"id"`
	SeedID    string    `json:"seed_id"`
	Status    string    `json:"status"` // e.g., "pending", "running", "completed", "failed"
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	StartedAt time.Time `json:"started_at,omitempty"`
	EndedAt   time.Time `json:"ended_at,omitempty"`
}

// DNSRecord represents a resolved DNS record.
type DNSRecord struct {
	Type  string `json:"type"`  // A, AAAA, CNAME, MX, TXT
	Value string `json:"value"` // IP address, target hostname, or text value
}

// AssetType defines the kind of asset discovered.
type AssetType string

const (
	AssetTypeDomain AssetType = "domain"
	AssetTypeIP     AssetType = "ip"
)

// Asset represents any discovered enterprise asset.
// Filtering processes will evaluate records (e.g., checking if CNAMEs point to known SaaS providers)
// to determine true relevance and scope.
type Asset struct {
	ID            string    `json:"id"`
	EnumerationID string    `json:"enumeration_id"` // Links the asset to a specific enumeration run.
	Type          AssetType `json:"type"`           // e.g., "domain", "ip"
	Identifier    string    `json:"identifier"`     // e.g., "api.google.com" or "192.168.1.100"
	Source        string    `json:"source"`         // Where was this found? (e.g., "dns_collector", "subfinder")
	DiscoveryDate time.Time `json:"discovery_date"`

	// Type-specific details. Only the relevant struct will be populated.
	DomainDetails *DomainDetails `json:"domain_details,omitempty"`
	IPDetails     *IPDetails     `json:"ip_details,omitempty"`

	// EnrichmentData contains flexible attributes such as port scan results or HTTP titles.
	EnrichmentData map[string]interface{} `json:"enrichment_data,omitempty"`
}

// RDAPData represents domain registration data from the RDAP protocol.
type RDAPData struct {
	RegistrarName   string    `json:"registrar_name,omitempty"`
	RegistrarIANAID string    `json:"registrar_iana_id,omitempty"`
	CreationDate    time.Time `json:"creation_date,omitempty"`
	ExpirationDate  time.Time `json:"expiration_date,omitempty"`
	UpdatedDate     time.Time `json:"updated_date,omitempty"`
	RegistrantName  string    `json:"registrant_name,omitempty"`
	RegistrantEmail string    `json:"registrant_email,omitempty"`
	RegistrantOrg   string    `json:"registrant_org,omitempty"`
	Statuses        []string  `json:"statuses,omitempty"`
	NameServers     []string  `json:"name_servers,omitempty"`
}

// DomainDetails holds domain-specific attributes.
type DomainDetails struct {
	Records    []DNSRecord `json:"records,omitempty"`
	IsCatchAll bool        `json:"is_catch_all,omitempty"`
	RDAP       *RDAPData   `json:"rdap,omitempty"`
}

// IPDetails holds IP-specific attributes.
type IPDetails struct {
	ASN          int    `json:"asn,omitempty"`
	Organization string `json:"organization,omitempty"`
	PTR          string `json:"ptr,omitempty"`
}

// PipelineContext represents the state passed between DAG nodes.
type PipelineContext struct {
	mu           sync.Mutex
	Seeds        []Seed
	Enumerations []Enumeration
	Assets       []Asset
	Errors       []error

	// Recursion Bounds Control
	Depth       int
	HasNewSeeds bool
}

// Lock acquires the mutex for safe concurrent mutation of the context.
func (p *PipelineContext) Lock() {
	p.mu.Lock()
}

// Unlock releases the mutex.
func (p *PipelineContext) Unlock() {
	p.mu.Unlock()
}
