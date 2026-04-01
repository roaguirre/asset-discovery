package search

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"asset-discovery/internal/discovery"
)

const (
	AIEnabledEnv   = "ASSET_DISCOVERY_AI_SEARCH_ENABLED"
	AIModelEnv     = "ASSET_DISCOVERY_AI_SEARCH_MODEL"
	AIAPIKeyEnv    = "ASSET_DISCOVERY_AI_SEARCH_API_KEY"
	AIBaseURLEnv   = "ASSET_DISCOVERY_AI_SEARCH_BASE_URL"
	defaultBaseURL = "https://api.openai.com/v1"
	defaultModel   = "gpt-5.4-nano"
	openAIAPIKey   = "OPENAI_API_KEY"
	maxCandidates  = 10
	maxEvidence    = 3
	maxQueries     = 5
)

// Provider executes a web-backed search request using a compact run summary.
type Provider interface {
	Search(ctx context.Context, summary ContextSummary) (SearchResult, error)
}

// ContextSummary carries the bounded run context that should inform web search.
type ContextSummary struct {
	SeedLabel         string   `json:"seed_label,omitempty"`
	SeedDomains       []string `json:"seed_domains,omitempty"`
	FocusRoot         string   `json:"focus_root,omitempty"`
	Industry          string   `json:"industry,omitempty"`
	ASN               []int    `json:"asn,omitempty"`
	CIDR              []string `json:"cidr,omitempty"`
	KnownRoots        []string `json:"known_roots,omitempty"`
	AcceptedRoots     []string `json:"accepted_roots,omitempty"`
	DiscardedRoots    []string `json:"discarded_roots,omitempty"`
	RegistrationFacts []string `json:"registration_facts,omitempty"`
	ObservedHosts     []string `json:"observed_hosts,omitempty"`
	ObservedRoots     []string `json:"observed_roots,omitempty"`
}

// SearchEvidence captures one cited web-search item for a candidate root.
type SearchEvidence struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

// SearchCandidate is one proposed registrable root from the search provider.
type SearchCandidate struct {
	Root     string           `json:"root"`
	Summary  string           `json:"summary"`
	Evidence []SearchEvidence `json:"evidence,omitempty"`
}

// SearchResult is the normalized structured output from the search provider.
type SearchResult struct {
	Queries    []string          `json:"queries,omitempty"`
	Candidates []SearchCandidate `json:"candidates,omitempty"`
}

// OpenAIProviderOption configures the OpenAI web-search provider.
type OpenAIProviderOption func(*OpenAIWebSearchProvider)

// OpenAIWebSearchProvider uses the Responses API with the native web_search tool.
type OpenAIWebSearchProvider struct {
	client   *http.Client
	endpoint string
	model    string
	apiKey   string
}

// WithOpenAIClient overrides the HTTP client used for Responses API requests.
func WithOpenAIClient(client *http.Client) OpenAIProviderOption {
	return func(provider *OpenAIWebSearchProvider) {
		if client != nil {
			provider.client = client
		}
	}
}

type responsesRequest struct {
	Model string          `json:"model"`
	Input string          `json:"input"`
	Store bool            `json:"store"`
	Tools []responsesTool `json:"tools,omitempty"`
	Text  *responsesText  `json:"text,omitempty"`
}

type responsesTool struct {
	Type string `json:"type"`
}

type responsesText struct {
	Format *responsesTextFormat `json:"format,omitempty"`
}

type responsesTextFormat struct {
	Type   string                 `json:"type"`
	Name   string                 `json:"name"`
	Strict bool                   `json:"strict"`
	Schema map[string]interface{} `json:"schema"`
}

type responsesResponse struct {
	OutputText string `json:"output_text,omitempty"`
	Output     []struct {
		Type    string `json:"type,omitempty"`
		Text    string `json:"text,omitempty"`
		Content []struct {
			Type string `json:"type,omitempty"`
			Text string `json:"text,omitempty"`
		} `json:"content,omitempty"`
	} `json:"output,omitempty"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// NewProviderFromEnv builds an OpenAI-backed search provider when AI-search
// configuration is present. ASSET_DISCOVERY_AI_SEARCH_ENABLED still supports an
// explicit opt-out so operators can disable the stage without removing keys.
func NewProviderFromEnv(options ...OpenAIProviderOption) (Provider, error) {
	enabled, configured := envBool(AIEnabledEnv)
	if configured && !enabled {
		return nil, nil
	}

	model := strings.TrimSpace(os.Getenv(AIModelEnv))
	apiKey := firstNonEmptyEnv(AIAPIKeyEnv, openAIAPIKey)
	baseURL := strings.TrimSpace(os.Getenv(AIBaseURLEnv))

	if model == "" && (apiKey != "" || baseURL != "" || (configured && enabled)) {
		model = defaultModel
	}
	if model == "" {
		return nil, nil
	}

	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	endpoint := strings.TrimRight(baseURL, "/") + "/responses"

	if apiKey == "" && strings.HasPrefix(endpoint, defaultBaseURL) {
		return nil, fmt.Errorf("AI search is configured but %s is empty", AIAPIKeyEnv)
	}

	provider := &OpenAIWebSearchProvider{
		client:   &http.Client{Timeout: 45 * time.Second},
		endpoint: endpoint,
		model:    model,
		apiKey:   apiKey,
	}
	for _, option := range options {
		if option != nil {
			option(provider)
		}
	}

	return provider, nil
}

// Search executes a bounded web-backed search request and validates the strict
// JSON-schema response before returning normalized candidate roots.
func (p *OpenAIWebSearchProvider) Search(ctx context.Context, summary ContextSummary) (SearchResult, error) {
	requestBody, err := json.Marshal(responsesRequest{
		Model: p.model,
		Input: buildSearchPrompt(summary),
		Store: false,
		Tools: []responsesTool{{Type: "web_search"}},
		Text: &responsesText{
			Format: &responsesTextFormat{
				Type:   "json_schema",
				Name:   "ai_search_result",
				Strict: true,
				Schema: searchResultSchema(),
			},
		},
	})
	if err != nil {
		return SearchResult{}, err
	}

	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		p.endpoint,
		bytes.NewReader(requestBody),
	)
	if err != nil {
		return SearchResult{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	response, err := p.client.Do(request)
	if err != nil {
		return SearchResult{}, err
	}
	defer response.Body.Close()

	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 1024*1024))
	if err != nil {
		return SearchResult{}, err
	}
	if response.StatusCode >= 400 {
		return SearchResult{}, fmt.Errorf(
			"unexpected AI search status %d: %s",
			response.StatusCode,
			strings.TrimSpace(string(responseBody)),
		)
	}

	var completion responsesResponse
	if err := json.Unmarshal(responseBody, &completion); err != nil {
		return SearchResult{}, err
	}
	if completion.Error != nil {
		return SearchResult{}, fmt.Errorf("AI search error: %s", completion.Error.Message)
	}

	result, err := parseSearchResult(extractOutputText(completion))
	if err != nil {
		return SearchResult{}, err
	}
	return normalizeSearchResult(result), nil
}

func buildSearchPrompt(summary ContextSummary) string {
	body, _ := json.MarshalIndent(summary, "", "  ")
	return strings.TrimSpace(searchPromptPreamble) + "\n\nContext:\n" + string(body)
}

func searchResultSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"queries": map[string]interface{}{
				"type":     "array",
				"maxItems": maxQueries,
				"items": map[string]interface{}{
					"type":      "string",
					"minLength": 1,
				},
			},
			"candidates": map[string]interface{}{
				"type":     "array",
				"maxItems": maxCandidates,
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"root": map[string]interface{}{
							"type":      "string",
							"minLength": 1,
						},
						"summary": map[string]interface{}{
							"type":      "string",
							"minLength": 1,
						},
						"evidence": map[string]interface{}{
							"type":     "array",
							"maxItems": maxEvidence,
							"items": map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"title": map[string]interface{}{
										"type":      "string",
										"minLength": 1,
									},
									"url": map[string]interface{}{
										"type":      "string",
										"minLength": 1,
									},
									"snippet": map[string]interface{}{
										"type":      "string",
										"minLength": 1,
									},
								},
								"required":             []string{"title", "url", "snippet"},
								"additionalProperties": false,
							},
						},
					},
					"required":             []string{"root", "summary", "evidence"},
					"additionalProperties": false,
				},
			},
		},
		"required":             []string{"queries", "candidates"},
		"additionalProperties": false,
	}
}

func extractOutputText(response responsesResponse) string {
	if text := strings.TrimSpace(response.OutputText); text != "" {
		return text
	}

	var builder strings.Builder
	for _, item := range response.Output {
		if text := strings.TrimSpace(item.Text); text != "" {
			if builder.Len() > 0 {
				builder.WriteString("\n")
			}
			builder.WriteString(text)
		}
		for _, content := range item.Content {
			if text := strings.TrimSpace(content.Text); text != "" {
				if builder.Len() > 0 {
					builder.WriteString("\n")
				}
				builder.WriteString(text)
			}
		}
	}
	return builder.String()
}

func parseSearchResult(raw string) (SearchResult, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start == -1 || end == -1 || end < start {
		return SearchResult{}, fmt.Errorf("AI search response did not contain a JSON object")
	}

	var result SearchResult
	if err := json.Unmarshal([]byte(raw[start:end+1]), &result); err != nil {
		return SearchResult{}, err
	}
	return result, nil
}

func normalizeSearchResult(result SearchResult) SearchResult {
	result.Queries = normalizeQueries(result.Queries)

	seenRoots := make(map[string]struct{}, len(result.Candidates))
	normalizedCandidates := make([]SearchCandidate, 0, minInt(len(result.Candidates), maxCandidates))
	for _, candidate := range result.Candidates {
		root := discovery.RegistrableDomain(candidate.Root)
		if root == "" {
			continue
		}
		if _, exists := seenRoots[root]; exists {
			continue
		}

		evidence := normalizeEvidence(candidate.Evidence)
		if len(evidence) == 0 {
			continue
		}

		summary := strings.TrimSpace(candidate.Summary)
		if summary == "" {
			continue
		}

		seenRoots[root] = struct{}{}
		normalizedCandidates = append(normalizedCandidates, SearchCandidate{
			Root:     root,
			Summary:  summary,
			Evidence: evidence,
		})
		if len(normalizedCandidates) >= maxCandidates {
			break
		}
	}

	result.Candidates = normalizedCandidates
	return result
}

func normalizeQueries(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	normalized := make([]string, 0, minInt(len(values), maxQueries))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, trimmed)
		if len(normalized) >= maxQueries {
			break
		}
	}
	return normalized
}

func normalizeEvidence(values []SearchEvidence) []SearchEvidence {
	seen := make(map[string]struct{}, len(values))
	normalized := make([]SearchEvidence, 0, minInt(len(values), maxEvidence))
	for _, value := range values {
		title := strings.TrimSpace(value.Title)
		snippet := strings.TrimSpace(value.Snippet)
		rawURL := strings.TrimSpace(value.URL)
		if title == "" || snippet == "" || rawURL == "" {
			continue
		}
		parsed, err := url.Parse(rawURL)
		if err != nil {
			continue
		}
		switch strings.ToLower(parsed.Scheme) {
		case "http", "https":
		default:
			continue
		}
		normalizedURL := parsed.String()
		if _, exists := seen[normalizedURL]; exists {
			continue
		}
		seen[normalizedURL] = struct{}{}
		normalized = append(normalized, SearchEvidence{
			Title:   title,
			URL:     normalizedURL,
			Snippet: snippet,
		})
		if len(normalized) >= maxEvidence {
			break
		}
	}

	sort.SliceStable(normalized, func(i, j int) bool {
		if normalized[i].URL == normalized[j].URL {
			return normalized[i].Title < normalized[j].Title
		}
		return normalized[i].URL < normalized[j].URL
	})
	return normalized
}

func envBool(key string) (bool, bool) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return false, false
	}
	switch strings.ToLower(value) {
	case "1", "true", "yes", "on":
		return true, true
	case "0", "false", "no", "off":
		return false, true
	default:
		return false, true
	}
}

func firstNonEmptyEnv(keys ...string) string {
	for _, key := range keys {
		value := strings.TrimSpace(os.Getenv(key))
		if value != "" {
			return value
		}
	}
	return ""
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

const searchPromptPreamble = `Use web search to find additional registrable domains that are likely owned or controlled by the same organization described in the context.

Be conservative.

Only include roots that likely represent:
- another first-party brand, product, business unit, or acquisition
- a legal, privacy, contact, investor, careers, support, help, or security property hosted on another owned root
- a rebrand or migration target clearly associated with the same organization

Exclude vendor, SaaS, social, marketplace, CDN, app-store, messaging, analytics, or generic third-party domains.

Search the web before answering. Return strict JSON matching the provided schema. If the evidence is weak, return an empty candidates array.`
