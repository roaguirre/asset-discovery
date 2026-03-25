package lineage

import "time"

type TraceSection struct {
	Title string   `json:"title" firestore:"title"`
	Items []string `json:"items,omitempty" firestore:"items,omitempty"`
}

type TraceLink struct {
	AssetID     string `json:"asset_id" firestore:"asset_id"`
	Identifier  string `json:"identifier" firestore:"identifier"`
	Label       string `json:"label" firestore:"label"`
	Description string `json:"description,omitempty" firestore:"description,omitempty"`
	TracePath   string `json:"trace_path,omitempty" firestore:"trace_path,omitempty"`
}

type TraceContributor struct {
	AssetID       string    `json:"asset_id,omitempty" firestore:"asset_id,omitempty"`
	EnumerationID string    `json:"enumeration_id,omitempty" firestore:"enumeration_id,omitempty"`
	SeedID        string    `json:"seed_id,omitempty" firestore:"seed_id,omitempty"`
	SeedLabel     string    `json:"seed_label,omitempty" firestore:"seed_label,omitempty"`
	Source        string    `json:"source,omitempty" firestore:"source,omitempty"`
	DiscoveryDate time.Time `json:"discovery_date,omitempty" firestore:"discovery_date,omitempty"`
}

type TraceNode struct {
	ID                  string         `json:"id" firestore:"id"`
	ParentID            string         `json:"parent_id,omitempty" firestore:"parent_id,omitempty"`
	Kind                string         `json:"kind,omitempty" firestore:"kind,omitempty"`
	Label               string         `json:"label" firestore:"label"`
	Subtitle            string         `json:"subtitle,omitempty" firestore:"subtitle,omitempty"`
	Badges              []string       `json:"badges,omitempty" firestore:"badges,omitempty"`
	LinkedAssetID       string         `json:"linked_asset_id,omitempty" firestore:"linked_asset_id,omitempty"`
	LinkedObservationID string         `json:"linked_observation_id,omitempty" firestore:"linked_observation_id,omitempty"`
	LinkedRelationID    string         `json:"linked_relation_id,omitempty" firestore:"linked_relation_id,omitempty"`
	Details             []TraceSection `json:"details,omitempty" firestore:"details,omitempty"`
}

type Trace struct {
	AssetID           string             `json:"asset_id" firestore:"asset_id"`
	Identifier        string             `json:"identifier" firestore:"identifier"`
	AssetType         string             `json:"asset_type" firestore:"asset_type"`
	Source            string             `json:"source" firestore:"source"`
	DiscoveredBy      string             `json:"discovered_by,omitempty" firestore:"discovered_by,omitempty"`
	EnrichedBy        string             `json:"enriched_by,omitempty" firestore:"enriched_by,omitempty"`
	EnumerationID     string             `json:"enumeration_id" firestore:"enumeration_id"`
	SeedID            string             `json:"seed_id" firestore:"seed_id"`
	DomainKind        string             `json:"domain_kind,omitempty" firestore:"domain_kind,omitempty"`
	RegistrableDomain string             `json:"registrable_domain,omitempty" firestore:"registrable_domain,omitempty"`
	ResolutionStatus  string             `json:"resolution_status,omitempty" firestore:"resolution_status,omitempty"`
	Contributors      []TraceContributor `json:"contributors,omitempty" firestore:"contributors,omitempty"`
	RootNodeID        string             `json:"root_node_id,omitempty" firestore:"root_node_id,omitempty"`
	Nodes             []TraceNode        `json:"nodes,omitempty" firestore:"nodes,omitempty"`
	Sections          []TraceSection     `json:"sections,omitempty" firestore:"sections,omitempty"`
	Related           []TraceLink        `json:"related,omitempty" firestore:"related,omitempty"`
}

type JudgeCandidate struct {
	Root       string   `json:"root" firestore:"root"`
	Confidence float64  `json:"confidence,omitempty" firestore:"confidence,omitempty"`
	Kind       string   `json:"kind,omitempty" firestore:"kind,omitempty"`
	Reason     string   `json:"reason,omitempty" firestore:"reason,omitempty"`
	Explicit   bool     `json:"explicit,omitempty" firestore:"explicit,omitempty"`
	Support    []string `json:"support,omitempty" firestore:"support,omitempty"`
}

type JudgeGroup struct {
	Collector   string           `json:"collector" firestore:"collector"`
	SeedID      string           `json:"seed_id,omitempty" firestore:"seed_id,omitempty"`
	SeedLabel   string           `json:"seed_label,omitempty" firestore:"seed_label,omitempty"`
	SeedDomains []string         `json:"seed_domains,omitempty" firestore:"seed_domains,omitempty"`
	Scenario    string           `json:"scenario,omitempty" firestore:"scenario,omitempty"`
	Accepted    []JudgeCandidate `json:"accepted,omitempty" firestore:"accepted,omitempty"`
	Discarded   []JudgeCandidate `json:"discarded,omitempty" firestore:"discarded,omitempty"`
}

type JudgeSummary struct {
	EvaluationCount int          `json:"evaluation_count" firestore:"evaluation_count"`
	AcceptedCount   int          `json:"accepted_count" firestore:"accepted_count"`
	DiscardedCount  int          `json:"discarded_count" firestore:"discarded_count"`
	Groups          []JudgeGroup `json:"groups,omitempty" firestore:"groups,omitempty"`
}
