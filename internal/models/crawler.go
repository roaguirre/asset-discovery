package models

import "time"

// CrawlRequest describes a bounded website crawl rooted at one seed frontier item.
type CrawlRequest struct {
	SeedID     string   `json:"seed_id,omitempty"`
	StartURLs  []string `json:"start_urls,omitempty"`
	ScopeRoots []string `json:"scope_roots,omitempty"`
	MaxDepth   int      `json:"max_depth,omitempty"`
	MaxPages   int      `json:"max_pages,omitempty"`
}

// CrawlPage stores the fetched HTML page and the link edges extracted from it.
type CrawlPage struct {
	URL         string      `json:"url"`
	FinalURL    string      `json:"final_url,omitempty"`
	Depth       int         `json:"depth,omitempty"`
	StatusCode  int         `json:"status_code,omitempty"`
	ContentType string      `json:"content_type,omitempty"`
	Title       string      `json:"title,omitempty"`
	FetchedAt   time.Time   `json:"fetched_at,omitempty"`
	Links       []CrawlLink `json:"links,omitempty"`
}

// CrawlLink captures one normalized hyperlink edge discovered while crawling.
type CrawlLink struct {
	SourceURL   string `json:"source_url,omitempty"`
	SourceTitle string `json:"source_title,omitempty"`
	SourceDepth int    `json:"source_depth,omitempty"`
	TargetURL   string `json:"target_url"`
	TargetHost  string `json:"target_host,omitempty"`
	TargetRoot  string `json:"target_root,omitempty"`
	Relation    string `json:"relation,omitempty"`
	AnchorText  string `json:"anchor_text,omitempty"`
	NoFollow    bool   `json:"nofollow,omitempty"`
}
