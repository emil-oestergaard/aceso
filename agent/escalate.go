package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// Escalator is the terminal layer of the diagnose pipeline. When every
// backend in the chain fails, Aceso must NOT invent a diagnosis — it must
// surface the alert to a human and stop. This matches the architecture in
// handoff.md ("alert Emil, wait") and the local-only constraint in
// CLAUDE.md: with no cloud fallback, an Ollama outage means we have no
// inference path, and silent recovery is a category error.
//
// Escalate is expected to never block on remote IO for longer than the
// surrounding context allows; the default implementation honours ctx and
// applies a per-call HTTP timeout.
type Escalator interface {
	Escalate(ctx context.Context, alert Alert, backendErr error) error
}

// NtfyEscalator emits a structured log line and, if a topic URL is configured,
// POSTs a short summary to ntfy.sh so the operator gets a push notification.
//
// The log line is always emitted — it is free, lands in whatever log
// shipper is already capturing aceso's stdout (Promtail, journald, etc.),
// and lets LogQL queries surface escalations as a first-class signal.
//
// The ntfy POST is best-effort. ntfy.sh is a public service; on a private
// deployment NtfyURL can point at a self-hosted instance. The chosen topic
// should be unguessable (treat the URL as a low-grade secret).
type NtfyEscalator struct {
	NtfyURL string
	HTTP    *http.Client
}

func newNtfyEscalator(ntfyURL string, timeout time.Duration) *NtfyEscalator {
	return &NtfyEscalator{
		NtfyURL: strings.TrimRight(ntfyURL, "/"),
		HTTP:    &http.Client{Timeout: timeout},
	}
}

// Escalate logs the escalation and, if configured, sends an ntfy push.
// A failure to deliver the ntfy push is not fatal — the structured log
// line is the source of truth, and an unreachable ntfy server should not
// itself become a paging incident.
func (e *NtfyEscalator) Escalate(ctx context.Context, alert Alert, backendErr error) error {
	// Structured log line first. Single line, key=value, so it's both
	// human-readable and trivially parseable by LogQL / awk / jq pipelines.
	log.Printf("[escalate] alert=%q severity=%q state=%q backend_error=%q",
		alert.Name(), alert.Severity(), alert.State, truncate(errString(backendErr), 300))

	if e.NtfyURL == "" {
		return nil
	}
	return e.postNtfy(ctx, alert, backendErr)
}

func (e *NtfyEscalator) postNtfy(ctx context.Context, alert Alert, _ error) error {
	// Body is deliberately metadata-only. The wrapped backend error can
	// transitively include truncated model output (see ollama.go's
	// "decoding diagnosis: ... (raw=...)" path), which is downstream of
	// production log content. Sending that to ntfy.sh — a third-party
	// service — would re-create the exfiltration risk we just removed
	// by deleting the cloud backends.
	//
	// Alert name, severity, and state are all operator-configured fields
	// from Prometheus alert rules (not raw log content), so they're safe
	// to send. The full backend error remains in the local [escalate]
	// log line and in the on-disk incident's `error` field.
	body := fmt.Sprintf(
		"alert=%s severity=%s state=%s\nLLM chain failed; check aceso logs for the diagnostic chain.",
		alert.Name(),
		alert.Severity(),
		alert.State,
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.NtfyURL, strings.NewReader(body))
	if err != nil {
		return fmt.Errorf("escalate: building ntfy request: %w", err)
	}
	// ntfy convention: headers carry metadata (title, priority, tags) so the
	// body stays a plain human-readable summary that renders well in the
	// mobile app and in email forwards.
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	req.Header.Set("Title", "aceso: LLM unreachable, alert escalated")
	req.Header.Set("Priority", "high")
	req.Header.Set("Tags", "warning,robot")

	resp, err := e.HTTP.Do(req)
	if err != nil {
		log.Printf("[escalate] ntfy delivery failed: %v", err)
		return fmt.Errorf("escalate: ntfy POST failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		excerpt, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		log.Printf("[escalate] ntfy non-2xx status=%d body=%q", resp.StatusCode, truncate(string(excerpt), 200))
		return fmt.Errorf("escalate: ntfy returned status %d", resp.StatusCode)
	}
	return nil
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
