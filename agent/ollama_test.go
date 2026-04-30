package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// wireResponse builds the outer Ollama envelope, embedding the model's raw
// response text (which is itself the JSON we want diagnose() to recover).
func wireResponse(t *testing.T, modelOutput string, done bool) string {
	t.Helper()
	env := struct {
		Model    string `json:"model"`
		Response string `json:"response"`
		Done     bool   `json:"done"`
	}{
		Model:    "gemma2:2b",
		Response: modelOutput,
		Done:     done,
	}
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("encode envelope: %v", err)
	}
	return string(b)
}

func TestOllamaDiagnose(t *testing.T) {
	t.Parallel()

	const cleanJSON = `{"cause":"disk full on /var","suggested_action":"rotate logs in /var/log"}`
	const fencedJSON = "Sure! Here you go:\n```json\n" +
		`{"cause":"disk full on /var","suggested_action":"rotate logs in /var/log"}` +
		"\n```\nLet me know if you need more."
	const malformedJSON = `definitely not json at all {{{`

	tests := []struct {
		name            string
		modelOutput     string
		done            bool
		status          int
		rawBodyOverride string // when set, server returns this verbatim instead of envelope
		wantErr         bool
		wantErrSubstr   string
		wantCause       string
		wantAction      string
	}{
		{
			name:        "valid json parses cleanly",
			modelOutput: cleanJSON,
			done:        true,
			status:      http.StatusOK,
			wantCause:   "disk full on /var",
			wantAction:  "rotate logs in /var/log",
		},
		{
			name:        "markdown-fenced json is recovered via prose-fence fallback",
			modelOutput: fencedJSON,
			done:        true,
			status:      http.StatusOK,
			wantCause:   "disk full on /var",
			wantAction:  "rotate logs in /var/log",
		},
		{
			name:          "malformed model output returns useful error",
			modelOutput:   malformedJSON,
			done:          true,
			status:        http.StatusOK,
			wantErr:       true,
			wantErrSubstr: "decoding diagnosis",
		},
		{
			name:            "non-2xx status is surfaced",
			rawBodyOverride: "ollama is unhappy",
			status:          http.StatusBadGateway,
			wantErr:         true,
			wantErrSubstr:   "unexpected status 502",
		},
		{
			name:          "envelope not done is surfaced",
			modelOutput:   cleanJSON,
			done:          false,
			status:        http.StatusOK,
			wantErr:       true,
			wantErrSubstr: "not marked done",
		},
		{
			name:            "malformed envelope (not json at all) is surfaced",
			rawBodyOverride: `{not even close`,
			status:          http.StatusOK,
			wantErr:         true,
			wantErrSubstr:   "decoding envelope",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var gotPath, gotMethod, gotContentType string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				gotMethod = r.Method
				gotContentType = r.Header.Get("Content-Type")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				if tc.rawBodyOverride != "" {
					_, _ = w.Write([]byte(tc.rawBodyOverride))
					return
				}
				_, _ = w.Write([]byte(wireResponse(t, tc.modelOutput, tc.done)))
			}))
			t.Cleanup(srv.Close)

			client := newOllamaClient(srv.URL, "gemma2:2b", 2*time.Second)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			diag, err := client.diagnose(ctx, "diagnose this please")

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

			if gotPath != "/api/generate" {
				t.Errorf("hit path = %q, want %q", gotPath, "/api/generate")
			}
			if gotMethod != http.MethodPost {
				t.Errorf("method = %q, want POST", gotMethod)
			}
			if gotContentType != "application/json" {
				t.Errorf("content-type = %q, want application/json", gotContentType)
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

// TestOllamaDiagnoseConnectionRefused covers the transport-error path
// (server closed before the request fires).
func TestOllamaDiagnoseConnectionRefused(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Close()

	client := newOllamaClient(srv.URL, "gemma2:2b", 500*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	_, err := client.diagnose(ctx, "anything")
	if err == nil {
		t.Fatalf("expected error from closed server, got nil")
	}
	if !strings.Contains(err.Error(), "ollama:") {
		t.Errorf("err = %q, want it to be wrapped with ollama: prefix", err.Error())
	}
}

// TestOllamaDiagnoseTimeout covers the deadline path: server holds the
// connection open longer than the client's HTTP timeout.
func TestOllamaDiagnoseTimeout(t *testing.T) {
	t.Parallel()

	released := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-released
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(func() {
		close(released) // unblock the handler before tearing down the server
		srv.Close()
	})

	client := newOllamaClient(srv.URL, "gemma2:2b", 100*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := client.diagnose(ctx, "anything")
	if err == nil {
		t.Fatalf("expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "ollama:") {
		t.Errorf("err = %q, want ollama: prefix", err.Error())
	}
}

// TestRecoverJSON pins the prose-fence-recovery helper directly so the
// behavior is asserted independently of the HTTP path.
func TestRecoverJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      string
		wantOK  bool
		wantOut string
	}{
		{"plain object", `{"a":1}`, true, `{"a":1}`},
		{"prose around object", "blah blah {\"a\":1} trailing", true, `{"a":1}`},
		{"markdown fences", "```json\n{\"a\":1}\n```", true, `{"a":1}`},
		{"no braces at all", "no json here", false, ""},
		{"only opening brace", "blah { blah", false, ""},
		{"reversed braces", "} foo {", false, ""},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := recoverJSON(tc.in)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (got=%q)", ok, tc.wantOK, got)
			}
			if ok && got != tc.wantOut {
				t.Errorf("got = %q, want %q", got, tc.wantOut)
			}
		})
	}
}
