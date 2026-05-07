package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Diagnosis is the structured output Aceso expects back from the LLM.
// V0 deliberately keeps this surface tiny — two fields, no nested types —
// so even a 2B parameter model can stay on schema.
type Diagnosis struct {
	Cause           string `json:"cause"`
	SuggestedAction string `json:"suggested_action"`
}

// OllamaClient calls the local Ollama HTTP API.
type OllamaClient struct {
	baseURL string
	model   string
	http    *http.Client
}

func newOllamaClient(baseURL, model string, timeout time.Duration) *OllamaClient {
	return &OllamaClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		model:   model,
		http:    &http.Client{Timeout: timeout},
	}
}

// ollamaGenerateRequest is the wire format for /api/generate.
// We force streaming off so we get a single JSON object back, and ask for
// "json" format so Ollama constrains output to a JSON value.
type ollamaGenerateRequest struct {
	Model   string                 `json:"model"`
	Prompt  string                 `json:"prompt"`
	Stream  bool                   `json:"stream"`
	Format  string                 `json:"format"`
	Options map[string]interface{} `json:"options,omitempty"`
}

// ollamaGenerateResponse is the non-streaming response envelope.
// Only the fields we care about are declared.
type ollamaGenerateResponse struct {
	Model    string `json:"model"`
	Response string `json:"response"`
	Done     bool   `json:"done"`
}

// diagnose sends the prompt to Ollama and parses the JSON-formatted answer
// into a Diagnosis. The model is asked to return JSON via the "format" flag
// and a strict instruction in the prompt itself.
func (c *OllamaClient) diagnose(ctx context.Context, prompt string) (Diagnosis, error) {
	endpoint, err := url.JoinPath(c.baseURL, "/api/generate")
	if err != nil {
		return Diagnosis{}, fmt.Errorf("ollama: building URL: %w", err)
	}

	body := ollamaGenerateRequest{
		Model:  c.model,
		Prompt: prompt,
		Stream: false,
		Format: "json",
		Options: map[string]interface{}{
			// Lower temperature: we want consistent, structured diagnoses,
			// not creative writing. 0.2 is a reasonable middle ground.
			"temperature": 0.2,
		},
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return Diagnosis{}, fmt.Errorf("ollama: encoding request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return Diagnosis{}, fmt.Errorf("ollama: building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return Diagnosis{}, fmt.Errorf("ollama: request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return Diagnosis{}, fmt.Errorf("ollama: reading body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return Diagnosis{}, fmt.Errorf("ollama: unexpected status %d: %s", resp.StatusCode, truncate(string(respBody), 200))
	}

	var envelope ollamaGenerateResponse
	if err := json.Unmarshal(respBody, &envelope); err != nil {
		return Diagnosis{}, fmt.Errorf("ollama: decoding envelope: %w", err)
	}
	if !envelope.Done {
		return Diagnosis{}, fmt.Errorf("ollama: response not marked done")
	}

	// envelope.Response is itself a JSON document (because we asked for format=json).
	var diag Diagnosis
	if err := json.Unmarshal([]byte(envelope.Response), &diag); err != nil {
		// Some models occasionally wrap the JSON in markdown fences or prose.
		// Try a best-effort recovery before giving up.
		if cleaned, ok := recoverJSON(envelope.Response); ok {
			if err2 := json.Unmarshal([]byte(cleaned), &diag); err2 == nil {
				return diag, nil
			}
		}
		// Deferred-decision annotation (see "V0 escalation contract" in
		// docs/status.md). The "raw=" payload is the model's own output,
		// which is downstream of production log content because the
		// prompt fed the model alert labels and Loki excerpts. This
		// error string transitively reaches:
		//   - the local [escalate] log line                  (acceptable: local)
		//   - the on-disk incident's `error` field            (acceptable: local)
		//   - NOT the ntfy.sh body (sanitized in escalate.go) (acceptable: by design)
		// Safe ONLY while the log shipper is local-only (Promtail → Loki
		// today). If logs are ever shipped off-prem, redact the raw=
		// suffix here before that lands.
		return Diagnosis{}, fmt.Errorf("ollama: decoding diagnosis: %w (raw=%s)", err, truncate(envelope.Response, 200))
	}
	return diag, nil
}

// recoverJSON attempts to extract the first balanced JSON object from a string.
// Useful when a model decorates its answer with prose despite format=json.
func recoverJSON(s string) (string, bool) {
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start == -1 || end == -1 || end <= start {
		return "", false
	}
	return s[start : end+1], true
}
