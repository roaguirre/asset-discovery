package ownership

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
	LLMModelEnv    = "ASSET_DISCOVERY_OWNERSHIP_LLM_MODEL"
	LLMAPIKeyEnv   = "ASSET_DISCOVERY_OWNERSHIP_LLM_API_KEY"
	LLMBaseURLEnv  = "ASSET_DISCOVERY_OWNERSHIP_LLM_BASE_URL"
	LLMEndpointEnv = "ASSET_DISCOVERY_OWNERSHIP_LLM_ENDPOINT"

	defaultLLMBaseURL   = "https://api.openai.com/v1"
	defaultLLMModel     = "gpt-5.4-nano"
	defaultDecisionKind = "ownership_judged"
	openAIAPIKeyEnv     = "OPENAI_API_KEY"
)

type Judge interface {
	EvaluateCandidates(ctx context.Context, request Request) ([]Decision, error)
}

type Request struct {
	Scenario   string      `json:"scenario"`
	Seed       models.Seed `json:"seed"`
	Candidates []Candidate `json:"candidates"`
}

type Candidate struct {
	Root     string         `json:"root"`
	Evidence []EvidenceItem `json:"evidence,omitempty"`
}

type EvidenceItem struct {
	Kind    string `json:"kind"`
	Summary string `json:"summary,omitempty"`
}

type Decision struct {
	Root       string
	Kind       string
	Confidence float64
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
	judge, err := NewJudgeFromEnv()
	if err != nil {
		log.Printf("[Ownership Judge] LLM judge disabled: %v", err)
		return nil
	}
	return judge
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

func (j *llmJudge) EvaluateCandidates(ctx context.Context, request Request) ([]Decision, error) {
	if len(request.Candidates) == 0 {
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
				Content: buildJudgePrompt(request),
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

	decisions := make([]Decision, 0, len(decisionByRoot))
	for _, candidate := range request.Candidates {
		decision, exists := decisionByRoot[candidate.Root]
		if !exists || !decision.Collect {
			continue
		}

		kind := strings.TrimSpace(strings.ToLower(decision.Kind))
		if kind == "" {
			kind = defaultDecisionKind
		}

		decisions = append(decisions, Decision{
			Root:       candidate.Root,
			Kind:       kind,
			Confidence: clampUnitFloat(decision.Confidence),
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

func buildJudgePrompt(request Request) string {
	payload := struct {
		Scenario   string      `json:"scenario"`
		Seed       seedContext `json:"seed"`
		Candidates []Candidate `json:"candidates"`
	}{
		Scenario: request.Scenario,
		Seed: seedContext{
			CompanyName: strings.TrimSpace(request.Seed.CompanyName),
			Domains:     append([]string(nil), request.Seed.Domains...),
			Industry:    strings.TrimSpace(request.Seed.Industry),
			ASN:         append([]int(nil), request.Seed.ASN...),
			CIDR:        append([]string(nil), request.Seed.CIDR...),
		},
		Candidates: request.Candidates,
	}

	body, _ := json.MarshalIndent(payload, "", "  ")
	return string(body)
}

type seedContext struct {
	CompanyName string   `json:"company_name,omitempty"`
	Domains     []string `json:"domains,omitempty"`
	Industry    string   `json:"industry,omitempty"`
	ASN         []int    `json:"asn,omitempty"`
	CIDR        []string `json:"cidr,omitempty"`
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

const judgeSystemPrompt = `You judge whether candidate registrable domains should be collected as first-party assets for the organization described in the seed context.

Use the provided evidence items and the seed context together. The evidence may come from registration metadata, DNS/PTR observations, network ownership pivots, or other structured discovery signals.

Be conservative:
- collect only when the candidate root is likely owned, controlled, or clearly in scope for the same organization
- reject customer domains, shared-hosting neighbors, generic SaaS/vendor domains, and tenuous lexical similarities
- do not rely on brand-name substring similarity alone unless the structured evidence also supports ownership

Return strict JSON with this shape and no extra prose:
{"decisions":[{"root":"example.com","collect":true,"confidence":0.93,"kind":"ownership_judged","reason":"brief reason"}]}

Only return decisions for the provided candidate roots.`
