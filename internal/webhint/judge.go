package webhint

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"asset-discovery/internal/discovery"
	"asset-discovery/internal/models"
)

const (
	LLMModelEnv    = "ASSET_DISCOVERY_WEB_HINT_LLM_MODEL"
	LLMAPIKeyEnv   = "ASSET_DISCOVERY_WEB_HINT_LLM_API_KEY"
	LLMBaseURLEnv  = "ASSET_DISCOVERY_WEB_HINT_LLM_BASE_URL"
	LLMEndpointEnv = "ASSET_DISCOVERY_WEB_HINT_LLM_ENDPOINT"

	defaultLLMBaseURL = "https://api.openai.com/v1"
	defaultLLMModel   = "gpt-5.4-nano"
	defaultLLMKind    = "llm_link"
	openAIAPIKeyEnv   = "OPENAI_API_KEY"
)

type Judge interface {
	EvaluateAnchorRoots(ctx context.Context, seed models.Seed, baseDomain string, candidates []Candidate) ([]Decision, error)
}

type Candidate struct {
	Root    string       `json:"root"`
	Samples []LinkSample `json:"samples,omitempty"`
}

type LinkSample struct {
	Href string `json:"href"`
	Text string `json:"text,omitempty"`
}

type Decision struct {
	Root       string
	Collect    bool
	Kind       string
	Confidence float64
	Reason     string
	Explicit   bool
}

type llmJudge struct {
	client   *http.Client
	endpoint string
	model    string
	apiKey   string
}

type openAICompatibleChatCompletionRequest struct {
	Model       string                          `json:"model"`
	Messages    []openAICompatibleChatMessage   `json:"messages"`
	Temperature float64                         `json:"temperature,omitempty"`
	ResponseFmt *openAICompatibleResponseFormat `json:"response_format,omitempty"`
}

type openAICompatibleChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAICompatibleResponseFormat struct {
	Type string `json:"type"`
}

type openAICompatibleChatCompletionResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices,omitempty"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type llmDecisionEnvelope struct {
	Decisions []llmDecision `json:"decisions"`
}

type llmDecision struct {
	Root       string  `json:"root"`
	Collect    bool    `json:"collect"`
	Confidence float64 `json:"confidence"`
	Kind       string  `json:"kind,omitempty"`
	Reason     string  `json:"reason,omitempty"`
}

func NewDefaultJudge() Judge {
	primary, err := NewJudgeFromEnv()
	if err != nil {
		log.Printf("[Web Hint Collector] LLM judge disabled: %v", err)
		return nil
	}
	return primary
}

func NewJudgeFromEnv() (Judge, error) {
	model := strings.TrimSpace(os.Getenv(LLMModelEnv))
	apiKey := firstNonEmptyEnv(LLMAPIKeyEnv, openAIAPIKeyEnv)
	baseURL := strings.TrimSpace(os.Getenv(LLMBaseURLEnv))
	endpoint := strings.TrimSpace(os.Getenv(LLMEndpointEnv))

	if model == "" && apiKey != "" {
		model = defaultLLMModel
	}
	if model == "" {
		return nil, nil
	}

	if endpoint == "" {
		if baseURL == "" {
			baseURL = defaultLLMBaseURL
		}
		endpoint = strings.TrimRight(baseURL, "/") + "/chat/completions"
	}

	if apiKey == "" && strings.HasPrefix(endpoint, defaultLLMBaseURL) {
		return nil, fmt.Errorf("%s is set but %s is empty", LLMModelEnv, LLMAPIKeyEnv)
	}

	return &llmJudge{
		client:   &http.Client{Timeout: 20 * time.Second},
		model:    model,
		apiKey:   apiKey,
		endpoint: endpoint,
	}, nil
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

func (j *llmJudge) EvaluateAnchorRoots(ctx context.Context, seed models.Seed, baseDomain string, candidates []Candidate) ([]Decision, error) {
	if len(candidates) == 0 {
		return nil, nil
	}

	payload := openAICompatibleChatCompletionRequest{
		Model: j.model,
		Messages: []openAICompatibleChatMessage{
			{
				Role:    "system",
				Content: judgeSystemPrompt,
			},
			{
				Role:    "user",
				Content: buildJudgePrompt(seed, baseDomain, candidates),
			},
		},
		Temperature: 0,
		ResponseFmt: &openAICompatibleResponseFormat{Type: "json_object"},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, j.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if j.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+j.apiKey)
	}

	resp, err := j.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("unexpected LLM status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var completion openAICompatibleChatCompletionResponse
	if err := json.Unmarshal(respBody, &completion); err != nil {
		return nil, err
	}
	if completion.Error != nil {
		return nil, fmt.Errorf("LLM error: %s", completion.Error.Message)
	}
	if len(completion.Choices) == 0 {
		return nil, fmt.Errorf("LLM response did not include choices")
	}

	decisionByRoot, err := parseLLMDecisions(completion.Choices[0].Message.Content)
	if err != nil {
		return nil, err
	}

	decisions := make([]Decision, 0, len(candidates))
	for _, candidate := range candidates {
		decision, exists := decisionByRoot[candidate.Root]

		kind := strings.TrimSpace(strings.ToLower(decision.Kind))
		if decision.Collect && kind == "" {
			kind = defaultLLMKind
		}

		decisions = append(decisions, Decision{
			Root:       candidate.Root,
			Collect:    exists && decision.Collect,
			Kind:       kind,
			Confidence: clampUnitFloat(decision.Confidence),
			Reason:     strings.TrimSpace(decision.Reason),
			Explicit:   exists,
		})
	}

	return decisions, nil
}

func parseLLMDecisions(raw string) (map[string]llmDecision, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start == -1 || end == -1 || end < start {
		return nil, fmt.Errorf("LLM response did not contain a JSON object")
	}

	var envelope llmDecisionEnvelope
	if err := json.Unmarshal([]byte(raw[start:end+1]), &envelope); err != nil {
		return nil, err
	}

	decisions := make(map[string]llmDecision, len(envelope.Decisions))
	for _, decision := range envelope.Decisions {
		root := discovery.RegistrableDomain(decision.Root)
		if root == "" {
			continue
		}
		decision.Root = root
		decision.Confidence = clampUnitFloat(decision.Confidence)
		decisions[root] = decision
	}

	return decisions, nil
}

func buildJudgePrompt(seed models.Seed, baseDomain string, candidates []Candidate) string {
	request := struct {
		CompanyName string      `json:"company_name,omitempty"`
		SeedDomains []string    `json:"seed_domains,omitempty"`
		PageDomain  string      `json:"page_domain"`
		Candidates  []Candidate `json:"candidates"`
	}{
		CompanyName: strings.TrimSpace(seed.CompanyName),
		SeedDomains: append([]string(nil), seed.Domains...),
		PageDomain:  discovery.NormalizeDomainIdentifier(baseDomain),
		Candidates:  candidates,
	}

	body, _ := json.MarshalIndent(request, "", "  ")
	return string(body)
}

func clampUnitFloat(value float64) float64 {
	switch {
	case value < 0:
		return 0
	case value > 1:
		return 1
	default:
		return value
	}
}

const judgeSystemPrompt = `You judge whether external domains linked from a company's website should be collected as potential first-party assets.

Be conservative.

Collect only when the external registrable domain likely represents:
- another domain owned or controlled by the same company
- a rebrand, migration, acquisition, or sister brand
- an explicit first-party legal, privacy, about, contact, or security property hosted on another owned root

Reject third-party platforms and utilities, especially:
- messaging, social, or contact platforms like whatsapp.com, facebook.com, instagram.com, linkedin.com, calendly.com
- SaaS, analytics, CDNs, payment providers, booking providers, map providers, app stores, or generic vendor links

Return strict JSON with this shape and no extra prose:
{"decisions":[{"root":"example.com","collect":true,"confidence":0.91,"kind":"llm_link","reason":"brief reason"}]}

Only return decisions for the provided candidate roots.
Confidence should be above 0.9 only for strong first-party ownership evidence.`
