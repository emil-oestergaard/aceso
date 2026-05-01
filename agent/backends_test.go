package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ----------------------------------------------------------------------------
// DeepSeek
// ----------------------------------------------------------------------------

// wireDeepSeekResponse builds the OpenAI-compatible envelope DeepSeek returns,
// embedding modelOutput as the assistant message content.
func wireDeepSeekResponse(t *testing.T, modelOutput string) string {
	t.Helper()
	env := map[string]any{
		"choices": []map[string]any{
			{
				"message": map[string]any{
					"role":    "assistant",
					"content": modelOutput,
				},
			},
		},
	}
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("encode envelope: %v", err)
	}
	return string(b)
}

func TestDeepSeekBackendDiagnose(t *testing.T) {
	t.Parallel()

	const cleanJSON = `{"cause":"oom on api","suggested_action":"restart api pod"}`
	const fencedJSON = "Here you go:\n```json\n" +
		`{"cause":"oom on api","suggested_action":"restart api pod"}` +
		"\n```"

	tests := []struct {
		name            string
		modelOutput     string
		status          int
		rawBodyOverride string
		wantErr         bool
		wantErrSubstr   string
		wantCause       string
		wantAction      string
	}{
		{
			name:        "valid json parses cleanly",
			modelOutput: cleanJSON,
			status:      http.StatusOK,
			wantCause:   "oom on api",
			wantAction:  "restart api pod",
		},
		{
			name:        "markdown-fenced json is recovered via prose-fence fallback",
			modelOutput: fencedJSON,
			status:      http.StatusOK,
			wantCause:   "oom on api",
			wantAction:  "restart api pod",
		},
		{
			name:          "non-2xx status is surfaced",
			status:        http.StatusUnauthorized,
			wantErr:       true,
			wantErrSubstr: "unexpected status 401",
		},
		{
			name:            "malformed envelope is surfaced",
			rawBodyOverride: `{not json`,
			status:          http.StatusOK,
			wantErr:         true,
			wantErrSubstr:   "decoding envelope",
		},
		{
			name:            "empty choices array is surfaced",
			rawBodyOverride: `{"choices":[]}`,
			status:          http.StatusOK,
			wantErr:         true,
			wantErrSubstr:   "empty choices",
		},
		{
			name:          "model returns garbage content",
			modelOutput:   `definitely not json {{{`,
			status:        http.StatusOK,
			wantErr:       true,
			wantErrSubstr: "decoding diagnosis",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var gotPath, gotMethod, gotAuth, gotContentType string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				gotMethod = r.Method
				gotAuth = r.Header.Get("Authorization")
				gotContentType = r.Header.Get("Content-Type")
				body, _ := io.ReadAll(r.Body)
				if !strings.Contains(string(body), `"deepseek-chat"`) {
					t.Errorf("request body missing model name: %s", string(body))
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				if tc.rawBodyOverride != "" {
					_, _ = w.Write([]byte(tc.rawBodyOverride))
					return
				}
				_, _ = w.Write([]byte(wireDeepSeekResponse(t, tc.modelOutput)))
			}))
			t.Cleanup(srv.Close)

			backend := &DeepSeekBackend{
				baseURL: srv.URL,
				apiKey:  "sk-test",
				model:   deepSeekDefaultModel,
				http:    &http.Client{Timeout: 2 * time.Second},
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			diag, err := backend.Diagnose(ctx, "diagnose this please")

			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (diag=%+v)", diag)
				}
				if tc.wantErrSubstr != "" && !strings.Contains(err.Error(), tc.wantErrSubstr) {
					t.Errorf("err = %q, want substring %q", err.Error(), tc.wantErrSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotPath != "/chat/completions" {
				t.Errorf("path = %q, want /chat/completions", gotPath)
			}
			if gotMethod != http.MethodPost {
				t.Errorf("method = %q, want POST", gotMethod)
			}
			if gotAuth != "Bearer sk-test" {
				t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer sk-test")
			}
			if gotContentType != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", gotContentType)
			}
			if diag.Cause != tc.wantCause {
				t.Errorf("Cause = %q, want %q", diag.Cause, tc.wantCause)
			}
			if diag.SuggestedAction != tc.wantAction {
				t.Errorf("SuggestedAction = %q, want %q", diag.SuggestedAction, tc.wantAction)
			}
		})
	}
}

func TestDeepSeekBackendTransportFailure(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Close()

	backend := &DeepSeekBackend{
		baseURL: srv.URL,
		apiKey:  "sk-test",
		model:   deepSeekDefaultModel,
		http:    &http.Client{Timeout: 500 * time.Millisecond},
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err := backend.Diagnose(ctx, "anything")
	if err == nil {
		t.Fatal("expected error from closed server, got nil")
	}
	if !strings.Contains(err.Error(), "deepseek:") {
		t.Errorf("err = %q, want deepseek: prefix", err.Error())
	}
}

// ----------------------------------------------------------------------------
// Gemini
// ----------------------------------------------------------------------------

// wireGeminiResponse builds the generateContent envelope, embedding modelOutput
// as the first candidate's first part text.
func wireGeminiResponse(t *testing.T, modelOutput string) string {
	t.Helper()
	env := map[string]any{
		"candidates": []map[string]any{
			{
				"content": map[string]any{
					"role": "model",
					"parts": []map[string]any{
						{"text": modelOutput},
					},
				},
			},
		},
	}
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("encode envelope: %v", err)
	}
	return string(b)
}

func TestGeminiBackendDiagnose(t *testing.T) {
	t.Parallel()

	const cleanJSON = `{"cause":"disk pressure on /var","suggested_action":"prune docker images"}`
	const fencedJSON = "Sure!\n```json\n" +
		`{"cause":"disk pressure on /var","suggested_action":"prune docker images"}` +
		"\n```"

	tests := []struct {
		name            string
		modelOutput     string
		status          int
		rawBodyOverride string
		wantErr         bool
		wantErrSubstr   string
		wantCause       string
		wantAction      string
	}{
		{
			name:        "valid json parses cleanly",
			modelOutput: cleanJSON,
			status:      http.StatusOK,
			wantCause:   "disk pressure on /var",
			wantAction:  "prune docker images",
		},
		{
			name:        "markdown-fenced json is recovered",
			modelOutput: fencedJSON,
			status:      http.StatusOK,
			wantCause:   "disk pressure on /var",
			wantAction:  "prune docker images",
		},
		{
			name:          "non-2xx status is surfaced",
			status:        http.StatusForbidden,
			wantErr:       true,
			wantErrSubstr: "unexpected status 403",
		},
		{
			name:            "malformed envelope is surfaced",
			rawBodyOverride: `{not json`,
			status:          http.StatusOK,
			wantErr:         true,
			wantErrSubstr:   "decoding envelope",
		},
		{
			name:            "empty candidates is surfaced",
			rawBodyOverride: `{"candidates":[]}`,
			status:          http.StatusOK,
			wantErr:         true,
			wantErrSubstr:   "empty candidates",
		},
		{
			name:            "candidates without parts is surfaced",
			rawBodyOverride: `{"candidates":[{"content":{"parts":[]}}]}`,
			status:          http.StatusOK,
			wantErr:         true,
			wantErrSubstr:   "empty candidates",
		},
		{
			name:          "model returns garbage content",
			modelOutput:   `definitely not json {{{`,
			status:        http.StatusOK,
			wantErr:       true,
			wantErrSubstr: "decoding diagnosis",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var gotPath, gotMethod, gotAPIKey, gotContentType string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				gotMethod = r.Method
				gotAPIKey = r.Header.Get("x-goog-api-key")
				gotContentType = r.Header.Get("Content-Type")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				if tc.rawBodyOverride != "" {
					_, _ = w.Write([]byte(tc.rawBodyOverride))
					return
				}
				_, _ = w.Write([]byte(wireGeminiResponse(t, tc.modelOutput)))
			}))
			t.Cleanup(srv.Close)

			backend := &GeminiBackend{
				baseURL: srv.URL,
				apiKey:  "ai-test",
				model:   geminiDefaultModel,
				http:    &http.Client{Timeout: 2 * time.Second},
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			diag, err := backend.Diagnose(ctx, "diagnose this please")

			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (diag=%+v)", diag)
				}
				if tc.wantErrSubstr != "" && !strings.Contains(err.Error(), tc.wantErrSubstr) {
					t.Errorf("err = %q, want substring %q", err.Error(), tc.wantErrSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			wantPath := "/v1beta/models/" + geminiDefaultModel + ":generateContent"
			if gotPath != wantPath {
				t.Errorf("path = %q, want %q", gotPath, wantPath)
			}
			if gotMethod != http.MethodPost {
				t.Errorf("method = %q, want POST", gotMethod)
			}
			if gotAPIKey != "ai-test" {
				t.Errorf("x-goog-api-key = %q, want %q", gotAPIKey, "ai-test")
			}
			if gotContentType != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", gotContentType)
			}
			if diag.Cause != tc.wantCause {
				t.Errorf("Cause = %q, want %q", diag.Cause, tc.wantCause)
			}
			if diag.SuggestedAction != tc.wantAction {
				t.Errorf("SuggestedAction = %q, want %q", diag.SuggestedAction, tc.wantAction)
			}
		})
	}
}

func TestGeminiBackendTransportFailure(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Close()

	backend := &GeminiBackend{
		baseURL: srv.URL,
		apiKey:  "ai-test",
		model:   geminiDefaultModel,
		http:    &http.Client{Timeout: 500 * time.Millisecond},
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err := backend.Diagnose(ctx, "anything")
	if err == nil {
		t.Fatal("expected error from closed server, got nil")
	}
	if !strings.Contains(err.Error(), "gemini:") {
		t.Errorf("err = %q, want gemini: prefix", err.Error())
	}
}

// ----------------------------------------------------------------------------
// parseDiagnosisJSON helper
// ----------------------------------------------------------------------------

func TestParseDiagnosisJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		raw        string
		wantErr    bool
		wantCause  string
		wantAction string
	}{
		{
			name:       "plain json",
			raw:        `{"cause":"a","suggested_action":"b"}`,
			wantCause:  "a",
			wantAction: "b",
		},
		{
			name:       "fenced json recovered",
			raw:        "```json\n{\"cause\":\"a\",\"suggested_action\":\"b\"}\n```",
			wantCause:  "a",
			wantAction: "b",
		},
		{
			name:       "prose around object recovered",
			raw:        "Here it is: {\"cause\":\"a\",\"suggested_action\":\"b\"} done.",
			wantCause:  "a",
			wantAction: "b",
		},
		{
			name:    "no braces fails",
			raw:     "absolutely no json here",
			wantErr: true,
		},
		{
			name:    "garbage with braces still fails",
			raw:     "{nope nope}",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			diag, err := parseDiagnosisJSON("test", tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %+v", diag)
				}
				if !strings.Contains(err.Error(), "test:") {
					t.Errorf("err = %q, should be prefixed with backend name 'test:'", err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if diag.Cause != tc.wantCause || diag.SuggestedAction != tc.wantAction {
				t.Errorf("got %+v, want cause=%q action=%q", diag, tc.wantCause, tc.wantAction)
			}
		})
	}
}
