package search

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewProviderFromEnv_UsesOpenAIDefaultsWhenAPIKeyIsPresent(t *testing.T) {
	t.Setenv(AIEnabledEnv, "")
	t.Setenv(AIModelEnv, "")
	t.Setenv(AIAPIKeyEnv, "")
	t.Setenv(AIBaseURLEnv, "")
	t.Setenv(openAIAPIKey, "test-openai-key")

	provider, err := NewProviderFromEnv()
	if err != nil {
		t.Fatalf("expected OpenAI defaults to configure AI search, got %v", err)
	}

	openAIProvider, ok := provider.(*OpenAIWebSearchProvider)
	if !ok {
		t.Fatalf("expected OpenAIWebSearchProvider, got %T", provider)
	}
	if openAIProvider.model != defaultModel {
		t.Fatalf("expected default model %q, got %q", defaultModel, openAIProvider.model)
	}
	if openAIProvider.apiKey != "test-openai-key" {
		t.Fatalf("expected OPENAI_API_KEY fallback to be used, got %q", openAIProvider.apiKey)
	}
	if openAIProvider.endpoint != defaultBaseURL+"/responses" {
		t.Fatalf("expected default endpoint, got %q", openAIProvider.endpoint)
	}
}

func TestNewProviderFromEnv_ExplicitDisableWins(t *testing.T) {
	t.Setenv(AIEnabledEnv, "false")
	t.Setenv(AIModelEnv, "")
	t.Setenv(AIAPIKeyEnv, "")
	t.Setenv(AIBaseURLEnv, "")
	t.Setenv(openAIAPIKey, "test-openai-key")

	provider, err := NewProviderFromEnv()
	if err != nil {
		t.Fatalf("expected explicit disable to skip AI search, got %v", err)
	}
	if provider != nil {
		t.Fatalf("expected nil provider when explicitly disabled, got %T", provider)
	}
}

func TestNewProviderFromEnv_PrefersSearchSpecificAPIKey(t *testing.T) {
	t.Setenv(AIEnabledEnv, "")
	t.Setenv(AIModelEnv, "")
	t.Setenv(AIAPIKeyEnv, "search-specific-key")
	t.Setenv(AIBaseURLEnv, "")
	t.Setenv(openAIAPIKey, "fallback-openai-key")

	provider, err := NewProviderFromEnv()
	if err != nil {
		t.Fatalf("expected search-specific config to succeed, got %v", err)
	}

	openAIProvider, ok := provider.(*OpenAIWebSearchProvider)
	if !ok {
		t.Fatalf("expected OpenAIWebSearchProvider, got %T", provider)
	}
	if openAIProvider.apiKey != "search-specific-key" {
		t.Fatalf("expected search-specific API key to win, got %q", openAIProvider.apiKey)
	}
}

// TestOpenAIWebSearchProvider_SearchParsesStructuredOutput verifies the
// provider accepts strict structured output from the Responses API and
// normalizes it into bounded candidate roots.
func TestOpenAIWebSearchProvider_SearchParsesStructuredOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := responsesResponse{
			OutputText: `{"queries":["example brands"],"candidates":[{"root":"portal.example.com","summary":"Official portal brand.","evidence":[{"title":"Portal","url":"https://portal.example.com","snippet":"Official portal brand."}]}]}`,
		}
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Fatalf("Encode() error = %v", err)
		}
	}))
	defer server.Close()

	provider := &OpenAIWebSearchProvider{
		client:   server.Client(),
		endpoint: server.URL,
		model:    "gpt-5.4-nano",
		apiKey:   "test-key",
	}

	result, err := provider.Search(context.Background(), ContextSummary{
		SeedLabel:   "Example Corp",
		SeedDomains: []string{"example.com"},
		FocusRoot:   "example.com",
	})
	if err != nil {
		t.Fatalf("expected structured response to parse, got %v", err)
	}
	if len(result.Queries) != 1 || result.Queries[0] != "example brands" {
		t.Fatalf("expected queries to be preserved, got %+v", result.Queries)
	}
	if len(result.Candidates) != 1 || result.Candidates[0].Root != "example.com" {
		t.Fatalf("expected registrable root normalization, got %+v", result.Candidates)
	}
}

// TestOpenAIWebSearchProvider_SearchRejectsNonJSONOutput verifies malformed
// structured-output payloads fail fast instead of silently producing search
// candidates from invalid content.
func TestOpenAIWebSearchProvider_SearchRejectsNonJSONOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := responsesResponse{OutputText: "not valid json"}
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Fatalf("Encode() error = %v", err)
		}
	}))
	defer server.Close()

	provider := &OpenAIWebSearchProvider{
		client:   server.Client(),
		endpoint: server.URL,
		model:    "gpt-5.4-nano",
		apiKey:   "test-key",
	}

	if _, err := provider.Search(context.Background(), ContextSummary{
		SeedLabel:   "Example Corp",
		SeedDomains: []string{"example.com"},
		FocusRoot:   "example.com",
	}); err == nil {
		t.Fatalf("expected malformed output to fail parsing")
	}
}
