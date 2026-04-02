package models

import (
	"sync"
	"time"
)

const defaultCandidatePromotionConfidenceThreshold = 0.50

// Seed represents the starting point for discovery.
// A Seed can contain various indicators that help OSINT collectors find assets.
type Seed struct {
	ID          string         `json:"id"`
	CompanyName string         `json:"company_name,omitempty"`
	Domains     []string       `json:"domains,omitempty"` // e.g., ["google.com", "alphabet.com"]
	Address     string         `json:"address,omitempty"`
	Industry    string         `json:"industry,omitempty"`
	Confidence  float64        `json:"confidence,omitempty"`
	Evidence    []SeedEvidence `json:"evidence,omitempty"`

	// Additional Discovery Vectors
	ASN  []int    `json:"asn,omitempty"`  // Autonomous System Numbers owned by the company
	CIDR []string `json:"cidr,omitempty"` // Known IP ranges (e.g., 192.168.1.0/24)

	// Metadata
	Tags []string `json:"tags,omitempty"` // e.g., ["internal", "acquisition", "out-of-scope"]
}

// SeedEvidence describes the provenance behind an auto-discovered seed.
type SeedEvidence struct {
	Source     string  `json:"source"`
	Kind       string  `json:"kind"`
	Value      string  `json:"value,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
	Reasoned   bool    `json:"reasoned,omitempty"`
}

// JudgeCandidateOutcome records the outcome of one ownership or web-hint judge
// candidate evaluation during a pipeline run.
type JudgeCandidateOutcome struct {
	Root       string   `json:"root"`
	Collect    bool     `json:"collect"`
	Confidence float64  `json:"confidence,omitempty"`
	Kind       string   `json:"kind,omitempty"`
	Reason     string   `json:"reason,omitempty"`
	Explicit   bool     `json:"explicit,omitempty"`
	Support    []string `json:"support,omitempty"`
}

// JudgeEvaluation stores one judge request and all candidate outcomes so the
// live read model can explain what was accepted and what was discarded for a run.
type JudgeEvaluation struct {
	Collector   string                  `json:"collector"`
	SeedID      string                  `json:"seed_id,omitempty"`
	SeedLabel   string                  `json:"seed_label,omitempty"`
	SeedDomains []string                `json:"seed_domains,omitempty"`
	Scenario    string                  `json:"scenario,omitempty"`
	Outcomes    []JudgeCandidateOutcome `json:"outcomes,omitempty"`
}

type CandidatePromotionDecision string

const (
	CandidatePromotionAccepted      CandidatePromotionDecision = "accepted"
	CandidatePromotionPendingReview CandidatePromotionDecision = "pending_review"
	CandidatePromotionRejected      CandidatePromotionDecision = "rejected"
)

type CandidatePromotionRequest struct {
	Key        string         `json:"key"`
	Seed       Seed           `json:"seed"`
	Evidence   []SeedEvidence `json:"evidence,omitempty"`
	Confidence float64        `json:"confidence,omitempty"`
	Reasoned   bool           `json:"reasoned,omitempty"`
}

// CandidatePromotionResult reports whether a discovered seed was accepted for
// registration and whether that acceptance opened another collection frontier.
type CandidatePromotionResult struct {
	Decision  CandidatePromotionDecision `json:"decision,omitempty"`
	Scheduled bool                       `json:"scheduled,omitempty"`
}

// ExecutionEvent describes a runtime activity update that should be streamed
// to live observers without relying on infrastructure logs.
type ExecutionEvent struct {
	Kind     string                 `json:"kind"`
	Message  string                 `json:"message"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

type CandidatePromotionHandler interface {
	HandleCandidatePromotion(candidate CandidatePromotionRequest) CandidatePromotionDecision
}

type MutationListener interface {
	OnAssetUpsert(asset Asset)
	OnObservationAdded(observation AssetObservation)
	OnRelationAdded(relation AssetRelation)
	OnJudgeEvaluationRecorded(evaluation JudgeEvaluation)
	OnExecutionEvent(event ExecutionEvent)
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
	ID               string                     `json:"id"`
	EnumerationID    string                     `json:"enumeration_id"` // Links the asset to a specific enumeration run.
	Type             AssetType                  `json:"type"`           // e.g., "domain", "ip"
	Identifier       string                     `json:"identifier"`     // e.g., "api.google.com" or "192.168.1.100"
	Source           string                     `json:"source"`         // Where was this found? (e.g., "dns_collector", "subfinder")
	DiscoveryDate    time.Time                  `json:"discovery_date"`
	Provenance       []AssetProvenance          `json:"provenance,omitempty"`
	OwnershipState   OwnershipState             `json:"ownership_state,omitempty"`
	InclusionReason  string                     `json:"inclusion_reason,omitempty"`
	EnrichmentStates map[string]EnrichmentState `json:"enrichment_states,omitempty"`

	// Type-specific details. Only the relevant struct will be populated.
	DomainDetails *DomainDetails `json:"domain_details,omitempty"`
	IPDetails     *IPDetails     `json:"ip_details,omitempty"`

	// EnrichmentData contains flexible attributes such as port scan results or HTTP titles.
	EnrichmentData map[string]interface{} `json:"enrichment_data,omitempty"`
}

// AssetProvenance tracks the contributing raw observations that were merged into an asset.
type AssetProvenance struct {
	AssetID       string    `json:"asset_id,omitempty"`
	EnumerationID string    `json:"enumeration_id,omitempty"`
	Source        string    `json:"source,omitempty"`
	DiscoveryDate time.Time `json:"discovery_date,omitempty"`
}

type OwnershipState string

const (
	OwnershipStateOwned                    OwnershipState = "owned"
	OwnershipStateAssociatedInfrastructure OwnershipState = "associated_infrastructure"
	OwnershipStateUncertain                OwnershipState = "uncertain"
)

type EnrichmentState struct {
	Status    string    `json:"status,omitempty"`
	Cached    bool      `json:"cached,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
	Error     string    `json:"error,omitempty"`
}

type DomainResolutionStatus string

const (
	DomainResolutionStatusResolved     DomainResolutionStatus = "resolved"
	DomainResolutionStatusUnresolved   DomainResolutionStatus = "unresolved"
	DomainResolutionStatusLookupFailed DomainResolutionStatus = "lookup_failed"
	DomainResolutionStatusNotChecked   DomainResolutionStatus = "not_checked"
)

type ObservationKind string

const (
	ObservationKindDiscovery  ObservationKind = "discovery"
	ObservationKindEnrichment ObservationKind = "enrichment"
)

type AssetObservation struct {
	Kind             ObservationKind            `json:"kind,omitempty"`
	ID               string                     `json:"id"`
	AssetID          string                     `json:"asset_id,omitempty"`
	EnumerationID    string                     `json:"enumeration_id,omitempty"`
	Type             AssetType                  `json:"type"`
	Identifier       string                     `json:"identifier"`
	Source           string                     `json:"source,omitempty"`
	DiscoveryDate    time.Time                  `json:"discovery_date,omitempty"`
	OwnershipState   OwnershipState             `json:"ownership_state,omitempty"`
	InclusionReason  string                     `json:"inclusion_reason,omitempty"`
	DomainDetails    *DomainDetails             `json:"domain_details,omitempty"`
	IPDetails        *IPDetails                 `json:"ip_details,omitempty"`
	EnrichmentData   map[string]interface{}     `json:"enrichment_data,omitempty"`
	EnrichmentStates map[string]EnrichmentState `json:"enrichment_states,omitempty"`
}

type AssetRelation struct {
	ID             string    `json:"id"`
	FromAssetID    string    `json:"from_asset_id,omitempty"`
	FromAssetType  AssetType `json:"from_asset_type,omitempty"`
	FromIdentifier string    `json:"from_identifier,omitempty"`
	ToAssetID      string    `json:"to_asset_id,omitempty"`
	ToAssetType    AssetType `json:"to_asset_type,omitempty"`
	ToIdentifier   string    `json:"to_identifier,omitempty"`
	ObservationID  string    `json:"observation_id,omitempty"`
	EnumerationID  string    `json:"enumeration_id,omitempty"`
	Source         string    `json:"source,omitempty"`
	Kind           string    `json:"kind,omitempty"`
	Label          string    `json:"label,omitempty"`
	Reason         string    `json:"reason,omitempty"`
	DiscoveryDate  time.Time `json:"discovery_date,omitempty"`
}

// RDAPData represents domain registration data from the RDAP protocol.
type RDAPData struct {
	RegistrarName   string    `json:"registrar_name,omitempty"`
	RegistrarIANAID string    `json:"registrar_iana_id,omitempty"`
	RegistrarURL    string    `json:"registrar_url,omitempty"`
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
	mu                    sync.Mutex
	Seeds                 []Seed
	Enumerations          []Enumeration
	Assets                []Asset
	Observations          []AssetObservation
	Relations             []AssetRelation
	Errors                []error `json:"-"`
	JudgeEvaluations      []JudgeEvaluation
	DNSVariantSweepLabels []string
	AISearchExecutedRoots []string

	collectionSeeds               []Seed
	pendingSeeds                  []Seed
	knownSeedKeys                 map[string]struct{}
	candidateSeeds                map[string]*seedCandidate
	collectionDepth               int
	maxCollectionDepth            int
	extraCollectionWaveReserved   bool
	extraCollectionWaveInProgress bool
	assetStateInitialized         bool
	assetIndexByKey               map[string]int
	observationIndexByID          map[string]int
	relationIndexByKey            map[string]int
	candidateHandler              CandidatePromotionHandler
	mutationListener              MutationListener
	candidatePromotionThreshold   float64
	candidatePromotionPolicySet   bool
}

type SchedulerState struct {
	CollectionSeeds               []Seed   `json:"collection_seeds,omitempty"`
	PendingSeeds                  []Seed   `json:"pending_seeds,omitempty"`
	CollectionDepth               int      `json:"collection_depth"`
	MaxCollectionDepth            int      `json:"max_collection_depth"`
	ExtraCollectionWaveReserved   bool     `json:"extra_collection_wave_reserved,omitempty"`
	ExtraCollectionWaveInProgress bool     `json:"extra_collection_wave_in_progress,omitempty"`
	AISearchExecutedRoots         []string `json:"ai_search_executed_roots,omitempty"`
}

type seedCandidate struct {
	seed          Seed
	evidence      []SeedEvidence
	signalKeys    map[string]struct{}
	maxConfidence float64
	reasoned      bool
}
