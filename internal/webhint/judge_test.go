package webhint

import "testing"

func TestNewJudgeFromEnv_UsesOpenAIDefaultsWhenAPIKeyIsPresent(t *testing.T) {
	t.Setenv(LLMModelEnv, "")
	t.Setenv(LLMAPIKeyEnv, "")
	t.Setenv(LLMBaseURLEnv, "")
	t.Setenv(LLMEndpointEnv, "")
	t.Setenv(openAIAPIKeyEnv, "test-openai-key")

	judge, err := NewJudgeFromEnv()
	if err != nil {
		t.Fatalf("expected OpenAI defaults to configure the web hint judge, got %v", err)
	}

	llm, ok := judge.(*llmJudge)
	if !ok {
		t.Fatalf("expected llmJudge, got %T", judge)
	}

	if llm.model != defaultLLMModel {
		t.Fatalf("expected default model %q, got %q", defaultLLMModel, llm.model)
	}
	if llm.apiKey != "test-openai-key" {
		t.Fatalf("expected OPENAI_API_KEY fallback to be used, got %q", llm.apiKey)
	}
	if llm.endpoint != defaultLLMBaseURL+"/chat/completions" {
		t.Fatalf("expected default OpenAI endpoint, got %q", llm.endpoint)
	}
}

func TestNewJudgeFromEnv_PrefersJudgeSpecificAPIKey(t *testing.T) {
	t.Setenv(LLMModelEnv, "")
	t.Setenv(LLMAPIKeyEnv, "judge-specific-key")
	t.Setenv(LLMBaseURLEnv, "")
	t.Setenv(LLMEndpointEnv, "")
	t.Setenv(openAIAPIKeyEnv, "fallback-openai-key")

	judge, err := NewJudgeFromEnv()
	if err != nil {
		t.Fatalf("expected judge-specific config to succeed, got %v", err)
	}

	llm, ok := judge.(*llmJudge)
	if !ok {
		t.Fatalf("expected llmJudge, got %T", judge)
	}

	if llm.apiKey != "judge-specific-key" {
		t.Fatalf("expected judge-specific API key to win, got %q", llm.apiKey)
	}
}
