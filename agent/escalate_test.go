package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fixtureEscalationAlert returns a populated Alert suitable for asserting
// on the ntfy body. The labels are deliberately a mix of operational and
// non-operational so a regression that leaks the wrong field would show
// up in body assertions.
func fixtureEscalationAlert() Alert {
	return Alert{
		Labels: map[string]string{
			"alertname": "OllamaUnreachable",
			"severity":  "critical",
			"instance":  "vps-01",
		},
		State: "firing",
	}
}

func TestNtfyEscalatorEmptyURLLogsOnly(t *testing.T) {
	t.Parallel()

	// No NtfyURL: Escalate must succeed without performing any HTTP call.
	// The structured log line is still emitted (verified implicitly by
	// "no panic, no error").
	esc := newNtfyEscalator("", time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := esc.Escalate(ctx, fixtureEscalationAlert(), errors.New("synthetic-backend-failure")); err != nil {
		t.Fatalf("expected nil error when NtfyURL is empty, got %v", err)
	}
}

func TestNtfyEscalatorPostsBodyAndHeaders(t *testing.T) {
	t.Parallel()

	type captured struct {
		method      string
		contentType string
		title       string
		priority    string
		tags        string
		body        string
	}
	var (
		mu  sync.Mutex
		got captured
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = captured{
			method:      r.Method,
			contentType: r.Header.Get("Content-Type"),
			title:       r.Header.Get("Title"),
			priority:    r.Header.Get("Priority"),
			tags:        r.Header.Get("Tags"),
			body:        string(body),
		}
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	esc := newNtfyEscalator(srv.URL, 2*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := esc.Escalate(ctx, fixtureEscalationAlert(), errors.New("synthetic-backend-failure"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if got.method != http.MethodPost {
		t.Errorf("method = %q, want POST", got.method)
	}
	if !strings.HasPrefix(got.contentType, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain prefix", got.contentType)
	}
	if got.title == "" {
		t.Errorf("Title header missing")
	}
	if got.priority != "high" {
		t.Errorf("Priority = %q, want high", got.priority)
	}
	if !strings.Contains(got.tags, "warning") {
		t.Errorf("Tags = %q, want substring 'warning'", got.tags)
	}
	for _, want := range []string{
		"alert=OllamaUnreachable",
		"severity=critical",
		"state=firing",
		"check aceso logs",
	} {
		if !strings.Contains(got.body, want) {
			t.Errorf("body missing %q, full body:\n%s", want, got.body)
		}
	}
	// Defense-in-depth assertion: the ntfy body must NOT carry the
	// backend error string, since wrapped chain errors can transitively
	// include model output that's downstream of production log content.
	// See escalate.go:postNtfy for the rationale.
	for _, banned := range []string{
		"synthetic-backend-failure",
		"backend error:",
		"raw=",
	} {
		if strings.Contains(got.body, banned) {
			t.Errorf("body MUST NOT contain %q (potential leak surface), full body:\n%s", banned, got.body)
		}
	}
}

func TestNtfyEscalatorNon2xxIsSurfaced(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("ntfy is having a bad day"))
	}))
	t.Cleanup(srv.Close)

	esc := newNtfyEscalator(srv.URL, 2*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := esc.Escalate(ctx, fixtureEscalationAlert(), errors.New("primary down"))
	if err == nil {
		t.Fatal("expected error on non-2xx ntfy response, got nil")
	}
	if !strings.Contains(err.Error(), "status 500") {
		t.Errorf("err = %q, want 'status 500' substring", err.Error())
	}
}

func TestNtfyEscalatorTransportFailureIsSurfaced(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Close() // immediate close → connection refused

	esc := newNtfyEscalator(srv.URL, 500*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	err := esc.Escalate(ctx, fixtureEscalationAlert(), errors.New("primary down"))
	if err == nil {
		t.Fatal("expected error from closed ntfy server, got nil")
	}
	if !strings.Contains(err.Error(), "escalate:") {
		t.Errorf("err = %q, want 'escalate:' prefix", err.Error())
	}
}
