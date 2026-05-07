package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestOllamaBackendForwardsToClient confirms OllamaBackend is a faithful
// wrapper around OllamaClient.diagnose — i.e. the Backend interface adapter
// adds no logic of its own. The full surface of the underlying client
// (status codes, prose-fence recovery, malformed envelopes, etc.) is
// covered by ollama_test.go; this test exists just to lock the
// "wrapper is transparent" contract so future refactors don't quietly
// add a behavior layer here.
func TestOllamaBackendForwardsToClient(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"gemma2:2b","done":true,"response":"{\"cause\":\"x\",\"suggested_action\":\"y\"}"}`))
	}))
	t.Cleanup(srv.Close)

	client := newOllamaClient(srv.URL, "gemma2:2b", 2*time.Second)
	backend := newOllamaBackend(client)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	diag, err := backend.Diagnose(ctx, "anything")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if diag.Cause != "x" || diag.SuggestedAction != "y" {
		t.Errorf("got %+v, want cause=x action=y", diag)
	}
}
