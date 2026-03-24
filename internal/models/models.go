package models

import (
	"sort"
	"strings"
	"sync"
	"time"
)

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
// visualizer can explain what was accepted and what was discarded for a run.
type JudgeEvaluation struct {
	Collector   string                  `json:"collector"`
	SeedID      string                  `json:"seed_id,omitempty"`
	SeedLabel   string                  `json:"seed_label,omitempty"`
	SeedDomains []string                `json:"seed_domains,omitempty"`
	Scenario    string                  `json:"scenario,omitempty"`
	Outcomes    []JudgeCandidateOutcome `json:"outcomes,omitempty"`
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
	mu               sync.Mutex
	Seeds            []Seed
	Enumerations     []Enumeration
	Assets           []Asset
	Observations     []AssetObservation
	Relations        []AssetRelation
	Errors           []error
	JudgeEvaluations []JudgeEvaluation

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
}

type seedCandidate struct {
	seed          Seed
	evidence      []SeedEvidence
	signalKeys    map[string]struct{}
	maxConfidence float64
	reasoned      bool
}

// Lock acquires the mutex for safe concurrent mutation of the context.
func (p *PipelineContext) Lock() {
	p.mu.Lock()
}

// Unlock releases the mutex.
func (p *PipelineContext) Unlock() {
	p.mu.Unlock()
}

// InitializeSeedFrontier prepares the collection scheduler with the initial seed frontier.
func (p *PipelineContext) InitializeSeedFrontier(maxDepth int) {
	if maxDepth < 0 {
		maxDepth = 0
	}

	p.Lock()
	defer p.Unlock()

	p.maxCollectionDepth = maxDepth
	p.collectionDepth = 0
	p.pendingSeeds = nil
	p.collectionSeeds = nil
	p.extraCollectionWaveReserved = false
	p.extraCollectionWaveInProgress = false
	p.knownSeedKeys = make(map[string]struct{}, len(p.Seeds))
	p.candidateSeeds = make(map[string]*seedCandidate)

	mergedSeeds := make(map[string]Seed, len(p.Seeds))
	seedOrder := make([]string, 0, len(p.Seeds))
	for _, seed := range p.Seeds {
		seed = normalizeSeed(seed)
		key := seedKey(seed)
		if existing, exists := mergedSeeds[key]; exists {
			mergedSeeds[key] = mergeSeeds(existing, seed)
			continue
		}

		mergedSeeds[key] = seed
		seedOrder = append(seedOrder, key)
	}

	p.Seeds = make([]Seed, 0, len(seedOrder))
	p.collectionSeeds = make([]Seed, 0, len(seedOrder))
	for _, key := range seedOrder {
		seed := mergedSeeds[key]
		p.knownSeedKeys[key] = struct{}{}
		p.Seeds = append(p.Seeds, seed)
		p.collectionSeeds = append(p.collectionSeeds, seed)
	}
}

// CollectionSeeds returns the active seed frontier for the current collection wave.
func (p *PipelineContext) CollectionSeeds() []Seed {
	p.Lock()
	defer p.Unlock()

	return append([]Seed(nil), p.collectionSeeds...)
}

// ReserveExtraCollectionWave allows one scheduler-owned collection wave after
// the normal collection depth has been exhausted.
func (p *PipelineContext) ReserveExtraCollectionWave() bool {
	p.Lock()
	defer p.Unlock()

	if p.extraCollectionWaveReserved || p.extraCollectionWaveInProgress {
		return false
	}

	p.extraCollectionWaveReserved = true
	return true
}

// EnqueueSeed schedules a newly discovered seed for the next collection wave.
func (p *PipelineContext) EnqueueSeed(seed Seed) bool {
	p.Lock()
	defer p.Unlock()

	mode := p.seedSchedulingModeLocked()
	if mode == seedSchedulingReject {
		return false
	}

	seed = normalizeSeed(seed)
	key := seedKey(seed)
	if _, exists := p.knownSeedKeys[key]; exists {
		p.mergeSeedAcrossSlicesLocked(seed)
		return false
	}

	delete(p.candidateSeeds, key)
	p.knownSeedKeys[key] = struct{}{}
	p.Seeds = append(p.Seeds, seed)
	if mode == seedSchedulingNextWave {
		p.pendingSeeds = append(p.pendingSeeds, seed)
		return true
	}

	return false
}

// EnqueueSeedCandidate records evidence for an auto-discovered seed and promotes it once
// it has either a reasoned approval, one strong signal, or at least two distinct weaker signals.
func (p *PipelineContext) EnqueueSeedCandidate(seed Seed, evidence SeedEvidence) bool {
	p.Lock()
	defer p.Unlock()

	mode := p.seedSchedulingModeLocked()
	if mode == seedSchedulingReject {
		return false
	}

	seed = normalizeSeed(seed)
	evidence = normalizeSeedEvidence(evidence)

	key := seedKey(seed)
	if _, exists := p.knownSeedKeys[key]; exists {
		p.mergeSeedAcrossSlicesLocked(seed, evidence)
		return false
	}

	candidate, exists := p.candidateSeeds[key]
	if !exists {
		candidate = &seedCandidate{
			seed:       seed,
			evidence:   append([]SeedEvidence(nil), seed.Evidence...),
			signalKeys: make(map[string]struct{}),
		}
		candidate.maxConfidence = seed.Confidence
		if hasReasonedSeedEvidence(seed.Evidence) {
			candidate.reasoned = true
		}
		p.candidateSeeds[key] = candidate
	} else {
		candidate.seed = mergeSeeds(candidate.seed, seed)
		candidate.evidence = mergeSeedEvidence(candidate.evidence, seed.Evidence...)
		if seed.Confidence > candidate.maxConfidence {
			candidate.maxConfidence = seed.Confidence
		}
		if hasReasonedSeedEvidence(seed.Evidence) {
			candidate.reasoned = true
		}
	}

	candidate.evidence = mergeSeedEvidence(candidate.evidence, evidence)
	if evidence.Source != "" && evidence.Kind != "" {
		signalKey := evidence.Source + "|" + evidence.Kind
		if _, seen := candidate.signalKeys[signalKey]; !seen {
			candidate.signalKeys[signalKey] = struct{}{}
		}
		if evidence.Confidence > candidate.maxConfidence {
			candidate.maxConfidence = evidence.Confidence
		}
	}
	if evidence.Reasoned {
		candidate.reasoned = true
	}

	if mode == seedSchedulingRegisterOnly && !shouldPromoteSeedCandidate(candidate) {
		p.materializeSeedCandidateLocked(key, candidate, false)
		return false
	}

	if !shouldPromoteSeedCandidate(candidate) {
		return false
	}

	return p.materializeSeedCandidateLocked(key, candidate, mode == seedSchedulingNextWave)
}

// AdvanceSeedFrontier moves newly discovered seeds into the next collection wave.
func (p *PipelineContext) AdvanceSeedFrontier() bool {
	p.Lock()
	defer p.Unlock()

	if len(p.pendingSeeds) == 0 {
		if p.extraCollectionWaveReserved && !p.extraCollectionWaveInProgress {
			p.extraCollectionWaveReserved = false
		}
		p.collectionSeeds = nil
		return false
	}

	if p.extraCollectionWaveReserved && !p.extraCollectionWaveInProgress {
		p.extraCollectionWaveReserved = false
		p.extraCollectionWaveInProgress = true
		p.collectionSeeds = append([]Seed(nil), p.pendingSeeds...)
		p.pendingSeeds = nil
		return true
	}

	if p.extraCollectionWaveInProgress {
		p.pendingSeeds = nil
		p.collectionSeeds = nil
		return false
	}

	p.collectionDepth++
	p.collectionSeeds = append([]Seed(nil), p.pendingSeeds...)
	p.pendingSeeds = nil

	return true
}

// RecordJudgeEvaluation appends structured judge analysis for the current run.
func (p *PipelineContext) RecordJudgeEvaluation(evaluation JudgeEvaluation) {
	if len(evaluation.Outcomes) == 0 {
		return
	}

	evaluation.Collector = strings.TrimSpace(strings.ToLower(evaluation.Collector))
	evaluation.SeedID = strings.TrimSpace(evaluation.SeedID)
	evaluation.SeedLabel = strings.TrimSpace(evaluation.SeedLabel)
	evaluation.Scenario = strings.TrimSpace(evaluation.Scenario)
	evaluation.SeedDomains = uniqueNormalizedStrings(evaluation.SeedDomains)

	outcomes := make([]JudgeCandidateOutcome, 0, len(evaluation.Outcomes))
	for _, outcome := range evaluation.Outcomes {
		outcome.Root = strings.TrimSpace(strings.ToLower(outcome.Root))
		outcome.Kind = strings.TrimSpace(strings.ToLower(outcome.Kind))
		outcome.Reason = strings.TrimSpace(outcome.Reason)
		outcome.Support = uniqueJudgeSupport(outcome.Support)
		if outcome.Root == "" {
			continue
		}
		outcomes = append(outcomes, outcome)
	}
	if len(outcomes) == 0 {
		return
	}
	evaluation.Outcomes = outcomes

	p.Lock()
	defer p.Unlock()

	p.JudgeEvaluations = append(p.JudgeEvaluations, evaluation)
}

func seedKey(seed Seed) string {
	company := strings.ToLower(strings.TrimSpace(seed.CompanyName))
	domains := make([]string, 0, len(seed.Domains))

	for _, domain := range seed.Domains {
		normalized := strings.ToLower(strings.TrimSpace(domain))
		if normalized == "" {
			continue
		}
		domains = append(domains, normalized)
	}

	sort.Strings(domains)

	if len(domains) > 0 {
		return strings.Join(domains, ",")
	}

	if company != "" {
		return company
	}

	return strings.TrimSpace(seed.ID)
}

type seedSchedulingMode int

const (
	seedSchedulingReject seedSchedulingMode = iota
	seedSchedulingNextWave
	seedSchedulingRegisterOnly
)

func (p *PipelineContext) seedSchedulingModeLocked() seedSchedulingMode {
	switch {
	case p.extraCollectionWaveInProgress:
		return seedSchedulingRegisterOnly
	case p.extraCollectionWaveReserved:
		return seedSchedulingNextWave
	case p.collectionDepth < p.maxCollectionDepth:
		return seedSchedulingNextWave
	default:
		return seedSchedulingReject
	}
}

func shouldPromoteSeedCandidate(candidate *seedCandidate) bool {
	if candidate == nil {
		return false
	}

	if candidate.reasoned {
		return true
	}

	if candidate.maxConfidence >= 0.9 {
		return true
	}

	return len(candidate.signalKeys) >= 2
}

func normalizeSeed(seed Seed) Seed {
	seed.ID = strings.TrimSpace(seed.ID)
	seed.CompanyName = strings.TrimSpace(seed.CompanyName)
	seed.Address = strings.TrimSpace(seed.Address)
	seed.Industry = strings.TrimSpace(seed.Industry)
	seed.Domains = uniqueNormalizedStrings(seed.Domains)
	seed.ASN = uniqueInts(seed.ASN)
	seed.CIDR = uniqueNormalizedStrings(seed.CIDR)
	seed.Tags = uniqueNormalizedStrings(seed.Tags)
	seed.Evidence = normalizeSeedEvidenceSlice(seed.Evidence)
	return seed
}

func normalizeSeedEvidence(evidence SeedEvidence) SeedEvidence {
	evidence.Source = strings.TrimSpace(strings.ToLower(evidence.Source))
	evidence.Kind = strings.TrimSpace(strings.ToLower(evidence.Kind))
	evidence.Value = strings.TrimSpace(strings.ToLower(evidence.Value))
	return evidence
}

func mergeSeeds(existing, incoming Seed) Seed {
	if existing.ID == "" {
		existing.ID = incoming.ID
	}
	if existing.CompanyName == "" {
		existing.CompanyName = incoming.CompanyName
	} else if incoming.CompanyName != "" && !strings.EqualFold(existing.CompanyName, incoming.CompanyName) {
		existing.Evidence = append(existing.Evidence, SeedEvidence{
			Source: "seed_merge",
			Kind:   "company_name",
			Value:  strings.ToLower(strings.TrimSpace(incoming.CompanyName)),
		})
	}
	if existing.Address == "" {
		existing.Address = incoming.Address
	}
	if existing.Industry == "" {
		existing.Industry = incoming.Industry
	}
	if incoming.Confidence > existing.Confidence {
		existing.Confidence = incoming.Confidence
	}
	existing.ASN = append(existing.ASN, incoming.ASN...)
	existing.Domains = append(existing.Domains, incoming.Domains...)
	existing.CIDR = append(existing.CIDR, incoming.CIDR...)
	existing.Tags = append(existing.Tags, incoming.Tags...)
	existing.Evidence = mergeSeedEvidence(existing.Evidence, incoming.Evidence...)
	existing.ASN = uniqueInts(existing.ASN)
	existing.Domains = uniqueNormalizedStrings(existing.Domains)
	existing.CIDR = uniqueNormalizedStrings(existing.CIDR)
	existing.Tags = uniqueNormalizedStrings(existing.Tags)
	return existing
}

func (p *PipelineContext) mergeSeedAcrossSlicesLocked(seed Seed, evidence ...SeedEvidence) {
	key := seedKey(seed)
	p.mergeSeedIntoSlice(p.Seeds, key, seed, evidence...)
	p.mergeSeedIntoSlice(p.collectionSeeds, key, seed, evidence...)
	p.mergeSeedIntoSlice(p.pendingSeeds, key, seed, evidence...)

	if candidate, exists := p.candidateSeeds[key]; exists {
		candidate.seed = mergeSeeds(candidate.seed, seed)
		candidate.evidence = mergeSeedEvidence(candidate.evidence, seed.Evidence...)
		candidate.evidence = mergeSeedEvidence(candidate.evidence, evidence...)
		if seed.Confidence > candidate.maxConfidence {
			candidate.maxConfidence = seed.Confidence
		}
		if hasReasonedSeedEvidence(seed.Evidence) {
			candidate.reasoned = true
		}
		if hasReasonedSeedEvidence(evidence) {
			candidate.reasoned = true
		}
	}
}

func (p *PipelineContext) materializeSeedCandidateLocked(key string, candidate *seedCandidate, schedule bool) bool {
	if candidate == nil {
		return false
	}

	promoted := candidate.seed
	promoted.Confidence = candidate.maxConfidence
	promoted.Evidence = append([]SeedEvidence(nil), candidate.evidence...)

	delete(p.candidateSeeds, key)
	p.knownSeedKeys[key] = struct{}{}
	p.Seeds = append(p.Seeds, promoted)
	if schedule {
		p.pendingSeeds = append(p.pendingSeeds, promoted)
		return true
	}

	return false
}

func (p *PipelineContext) mergeSeedIntoSlice(seeds []Seed, key string, incoming Seed, evidence ...SeedEvidence) {
	for i := range seeds {
		if seedKey(seeds[i]) == key {
			seeds[i] = mergeSeeds(seeds[i], incoming)
			seeds[i].Evidence = mergeSeedEvidence(seeds[i].Evidence, evidence...)
		}
	}
}

func mergeSeedEvidence(existing []SeedEvidence, incoming ...SeedEvidence) []SeedEvidence {
	if len(incoming) == 0 {
		return existing
	}

	index := make(map[string]int, len(existing))
	for i, evidence := range existing {
		index[seedEvidenceKey(evidence)] = i
	}

	for _, evidence := range incoming {
		evidence = normalizeSeedEvidence(evidence)
		if evidence.Source == "" && evidence.Kind == "" && evidence.Value == "" {
			continue
		}

		key := seedEvidenceKey(evidence)
		if idx, exists := index[key]; exists {
			if evidence.Confidence > existing[idx].Confidence {
				existing[idx].Confidence = evidence.Confidence
			}
			if evidence.Reasoned {
				existing[idx].Reasoned = true
			}
			if existing[idx].Value == "" {
				existing[idx].Value = evidence.Value
			}
			continue
		}

		index[key] = len(existing)
		existing = append(existing, evidence)
	}

	return existing
}

func seedEvidenceKey(evidence SeedEvidence) string {
	return strings.Join([]string{strings.ToLower(strings.TrimSpace(evidence.Source)), strings.ToLower(strings.TrimSpace(evidence.Kind)), strings.ToLower(strings.TrimSpace(evidence.Value))}, "|")
}

func hasReasonedSeedEvidence(evidence []SeedEvidence) bool {
	for _, item := range evidence {
		if item.Reasoned {
			return true
		}
	}
	return false
}

func normalizeSeedEvidenceSlice(evidence []SeedEvidence) []SeedEvidence {
	if len(evidence) == 0 {
		return nil
	}

	normalized := make([]SeedEvidence, 0, len(evidence))
	for _, item := range evidence {
		item = normalizeSeedEvidence(item)
		if item.Source == "" && item.Kind == "" && item.Value == "" {
			continue
		}
		normalized = append(normalized, item)
	}
	return mergeSeedEvidence(nil, normalized...)
}

func uniqueNormalizedStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		normalized := strings.ToLower(strings.TrimSpace(value))
		if normalized == "" {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	sort.Strings(out)
	return out
}

func uniqueInts(values []int) []int {
	if len(values) == 0 {
		return nil
	}

	seen := make(map[int]struct{}, len(values))
	out := make([]int, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Ints(out)
	return out
}

func uniqueJudgeSupport(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
