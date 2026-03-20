package lineage

import "time"

type TraceSection struct {
	Title string   `json:"title"`
	Items []string `json:"items,omitempty"`
}

type TraceLink struct {
	AssetID     string `json:"asset_id"`
	Identifier  string `json:"identifier"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	TracePath   string `json:"trace_path,omitempty"`
}

type TraceContributor struct {
	AssetID       string    `json:"asset_id,omitempty"`
	EnumerationID string    `json:"enumeration_id,omitempty"`
	SeedID        string    `json:"seed_id,omitempty"`
	SeedLabel     string    `json:"seed_label,omitempty"`
	Source        string    `json:"source,omitempty"`
	DiscoveryDate time.Time `json:"discovery_date,omitempty"`
}

type Trace struct {
	AssetID           string             `json:"asset_id"`
	Identifier        string             `json:"identifier"`
	AssetType         string             `json:"asset_type"`
	Source            string             `json:"source"`
	EnumerationID     string             `json:"enumeration_id"`
	SeedID            string             `json:"seed_id"`
	DomainKind        string             `json:"domain_kind,omitempty"`
	RegistrableDomain string             `json:"registrable_domain,omitempty"`
	Contributors      []TraceContributor `json:"contributors,omitempty"`
	Sections          []TraceSection     `json:"sections,omitempty"`
	Related           []TraceLink        `json:"related,omitempty"`
}

type JudgeCandidate struct {
	Root       string   `json:"root"`
	Confidence float64  `json:"confidence,omitempty"`
	Kind       string   `json:"kind,omitempty"`
	Reason     string   `json:"reason,omitempty"`
	Explicit   bool     `json:"explicit,omitempty"`
	Support    []string `json:"support,omitempty"`
}

type JudgeGroup struct {
	Collector   string           `json:"collector"`
	SeedID      string           `json:"seed_id,omitempty"`
	SeedLabel   string           `json:"seed_label,omitempty"`
	SeedDomains []string         `json:"seed_domains,omitempty"`
	Scenario    string           `json:"scenario,omitempty"`
	Accepted    []JudgeCandidate `json:"accepted,omitempty"`
	Discarded   []JudgeCandidate `json:"discarded,omitempty"`
}

type JudgeSummary struct {
	EvaluationCount int          `json:"evaluation_count"`
	AcceptedCount   int          `json:"accepted_count"`
	DiscardedCount  int          `json:"discarded_count"`
	Groups          []JudgeGroup `json:"groups,omitempty"`
}
