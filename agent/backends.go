package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Backend is the small interface every LLM backend implements.
// Single-method on purpose — the FallbackChain iterates over heterogeneous
// backends without any type-switching, and tests can fake a Backend with a
// 5-line struct literal.
type Backend interface {
	Diagnose(ctx context.Context, prompt string) (Diagnosis, error)
}

// namedBackend pairs a Backend with a human-readable name for chain logs.
// Names come from BACKEND_ORDER (e.g. "ollama", "deepseek", "gemini") so an
// operator reading the logs can tell which hop produced or failed a diagnosis.
type namedBackend struct {
	name string
	b    Backend
}

// ----------------------------------------------------------------------------
// Ollama (local or Tailscale-reachable Pi)
// ----------------------------------------------------------------------------

// OllamaBackend adapts the existing OllamaClient (see ollama.go) to the
// Backend interface. The wrapper deliberately doesn't duplicate HTTP logic —
// it forwards to OllamaClient.diagnose so all Ollama traffic stays in one
// tested code path.
type OllamaBackend struct {
	client *OllamaClient
}

func newOllamaBackend(c *OllamaClient) *OllamaBackend {
	return &OllamaBackend{client: c}
}

func (b *OllamaBackend) Diagnose(ctx context.Context, prompt string) (Diagnosis, error) {
	return b.client.diagnose(ctx, prompt)
}

// ----------------------------------------------------------------------------
// DeepSeek (free-tier fallback, OpenAI-compatible)
// ----------------------------------------------------------------------------

const (
	deepSeekDefaultBaseURL = "https://api.deepseek.com"
	deepSeekDefaultModel   = "deepseek-chat"
)

// DeepSeekBackend calls DeepSeek's OpenAI-compatible /chat/completions endpoint.
// API ref: https://api-docs.deepseek.com/api/create-chat-completion
type DeepSeekBackend struct {
	baseURL string
	apiKey  string
	model   string
	http    *http.Client
}

func newDeepSeekBackend(apiKey string, timeout time.Duration) *DeepSeekBackend {
	return &DeepSeekBackend{
		baseURL: deepSeekDefaultBaseURL,
		apiKey:  apiKey,
		model:   deepSeekDefaultModel,
		http:    &http.Client{Timeout: timeout},
	}
}

type deepSeekRequest struct {
	Model          string            `json:"model"`
	Messages       []deepSeekMessage `json:"messages"`
	Temperature    float64           `json:"temperature"`
	Stream         bool              `json:"stream"`
	ResponseFormat *deepSeekFormat   `json:"response_format,omitempty"`
}

type deepSeekMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type deepSeekFormat struct {
	Type string `json:"type"`
}

// deepSeekResponse declares only the fields we read.
type deepSeekResponse struct {
	Choices []struct {
		Message deepSeekMessage `json:"message"`
	} `json:"choices"`
}

func (b *DeepSeekBackend) Diagnose(ctx context.Context, prompt string) (Diagnosis, error) {
	endpoint, err := url.JoinPath(b.baseURL, "/chat/completions")
	if err != nil {
		return Diagnosis{}, fmt.Errorf("deepseek: building URL: %w", err)
	}

	body := deepSeekRequest{
		Model: b.model,
		Messages: []deepSeekMessage{
			{Role: "system", Content: `You return ONLY a JSON object of the form {"cause": string, "suggested_action": string}. No prose, no markdown.`},
			{Role: "user", Content: prompt},
		},
		Temperature:    0.2,
		Stream:         false,
		ResponseFormat: &deepSeekFormat{Type: "json_object"},
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return Diagnosis{}, fmt.Errorf("deepseek: encoding request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return Diagnosis{}, fmt.Errorf("deepseek: building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+b.apiKey)

	resp, err := b.http.Do(req)
	if err != nil {
		return Diagnosis{}, fmt.Errorf("deepseek: request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return Diagnosis{}, fmt.Errorf("deepseek: reading body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return Diagnosis{}, fmt.Errorf("deepseek: unexpected status %d: %s", resp.StatusCode, truncate(string(respBody), 200))
	}

	var envelope deepSeekResponse
	if err := json.Unmarshal(respBody, &envelope); err != nil {
		return Diagnosis{}, fmt.Errorf("deepseek: decoding envelope: %w", err)
	}
	if len(envelope.Choices) == 0 {
		return Diagnosis{}, fmt.Errorf("deepseek: empty choices array")
	}
	return parseDiagnosisJSON("deepseek", envelope.Choices[0].Message.Content)
}

// ----------------------------------------------------------------------------
// Gemini (free-tier fallback)
// ----------------------------------------------------------------------------

const (
	geminiDefaultBaseURL = "https://generativelanguage.googleapis.com"
	geminiDefaultModel   = "gemini-1.5-flash"
)

// GeminiBackend calls Google AI Studio's generateContent endpoint with a
// JSON-only response MIME type so the model is constrained to structured output.
// API ref: https://ai.google.dev/api/generate-content
type GeminiBackend struct {
	baseURL string
	apiKey  string
	model   string
	http    *http.Client
}

func newGeminiBackend(apiKey string, timeout time.Duration) *GeminiBackend {
	return &GeminiBackend{
		baseURL: geminiDefaultBaseURL,
		apiKey:  apiKey,
		model:   geminiDefaultModel,
		http:    &http.Client{Timeout: timeout},
	}
}

type geminiRequest struct {
	Contents         []geminiContent `json:"contents"`
	GenerationConfig geminiGenConfig `json:"generationConfig"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text string `json:"text"`
}

type geminiGenConfig struct {
	Temperature      float64 `json:"temperature"`
	ResponseMIMEType string  `json:"responseMimeType"`
}

type geminiResponse struct {
	Candidates []struct {
		Content geminiContent `json:"content"`
	} `json:"candidates"`
}

func (b *GeminiBackend) Diagnose(ctx context.Context, prompt string) (Diagnosis, error) {
	endpoint, err := url.JoinPath(b.baseURL, "/v1beta/models/"+b.model+":generateContent")
	if err != nil {
		return Diagnosis{}, fmt.Errorf("gemini: building URL: %w", err)
	}

	// Gemini has no native "system" role inside contents (it uses a separate
	// top-level systemInstruction field, which we deliberately do not set).
	// Two things keep the model on schema:
	//   1. The prompt built by brain.go:buildPrompt opens with a plain-text
	//      instruction: 'Respond ONLY with a JSON object of the form
	//      {"cause": string, "suggested_action": string}'.
	//   2. responseMimeType="application/json" in generationConfig, which
	//      Gemini honours by emitting JSON-only output.
	// If a model variant ever drifts and wraps the JSON in prose or markdown
	// fences, parseDiagnosisJSON applies the same recovery used elsewhere.
	body := geminiRequest{
		Contents: []geminiContent{
			{Role: "user", Parts: []geminiPart{{Text: prompt}}},
		},
		GenerationConfig: geminiGenConfig{
			Temperature:      0.2,
			ResponseMIMEType: "application/json",
		},
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return Diagnosis{}, fmt.Errorf("gemini: encoding request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return Diagnosis{}, fmt.Errorf("gemini: building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	// Header-based auth keeps the key out of URLs and access logs.
	req.Header.Set("x-goog-api-key", b.apiKey)

	resp, err := b.http.Do(req)
	if err != nil {
		return Diagnosis{}, fmt.Errorf("gemini: request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return Diagnosis{}, fmt.Errorf("gemini: reading body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return Diagnosis{}, fmt.Errorf("gemini: unexpected status %d: %s", resp.StatusCode, truncate(string(respBody), 200))
	}

	var envelope geminiResponse
	if err := json.Unmarshal(respBody, &envelope); err != nil {
		return Diagnosis{}, fmt.Errorf("gemini: decoding envelope: %w", err)
	}
	if len(envelope.Candidates) == 0 || len(envelope.Candidates[0].Content.Parts) == 0 {
		return Diagnosis{}, fmt.Errorf("gemini: empty candidates")
	}
	return parseDiagnosisJSON("gemini", envelope.Candidates[0].Content.Parts[0].Text)
}

// ----------------------------------------------------------------------------
// Shared helpers
// ----------------------------------------------------------------------------

// parseDiagnosisJSON extracts a Diagnosis from a model's text payload.
// Mirrors the prose-fence recovery in ollama.go so all three backends behave
// consistently when a model decorates its JSON with markdown or commentary.
func parseDiagnosisJSON(backend, raw string) (Diagnosis, error) {
	var diag Diagnosis
	if err := json.Unmarshal([]byte(raw), &diag); err == nil {
		return diag, nil
	}
	if cleaned, ok := recoverJSON(raw); ok {
		if err := json.Unmarshal([]byte(cleaned), &diag); err == nil {
			return diag, nil
		}
	}
	return Diagnosis{}, fmt.Errorf("%s: decoding diagnosis: invalid JSON (raw=%s)", backend, truncate(raw, 200))
}

// buildBackendChain materializes a FallbackChain from the configured order.
// Backends whose credentials are missing are skipped with a log line so a
// deployment can run Ollama-only or cloud-only without hard failures —
// the chain only errors out when *no* backend is usable.
func buildBackendChain(cfg *Config, ollama *OllamaClient) (*FallbackChain, error) {
	var chain []namedBackend
	for _, raw := range cfg.BackendOrder {
		name := strings.ToLower(strings.TrimSpace(raw))
		switch name {
		case "":
			continue
		case "ollama":
			chain = append(chain, namedBackend{name: "ollama", b: newOllamaBackend(ollama)})
		case "deepseek":
			if cfg.DeepSeekAPIKey == "" {
				log.Printf("[chain] skipping deepseek: DEEPSEEK_API_KEY not set")
				continue
			}
			chain = append(chain, namedBackend{
				name: "deepseek",
				b:    newDeepSeekBackend(cfg.DeepSeekAPIKey, cfg.HTTPTimeout),
			})
		case "gemini":
			if cfg.GeminiAPIKey == "" {
				log.Printf("[chain] skipping gemini: GEMINI_API_KEY not set")
				continue
			}
			chain = append(chain, namedBackend{
				name: "gemini",
				b:    newGeminiBackend(cfg.GeminiAPIKey, cfg.HTTPTimeout),
			})
		default:
			log.Printf("[chain] skipping unknown backend: %q", raw)
		}
	}
	if len(chain) == 0 {
		return nil, fmt.Errorf("backend chain: no usable backends from BACKEND_ORDER=%v", cfg.BackendOrder)
	}
	return &FallbackChain{backends: chain}, nil
}
