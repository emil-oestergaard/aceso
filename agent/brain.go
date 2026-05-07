package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Brain is the orchestrator: it pulls alerts, hydrates them with logs,
// asks the configured backend (single or fallback chain) for a diagnosis,
// and persists the incident to disk. When the backend chain fails, the
// alert is escalated to a human via the Escalator and a record with
// `escalated: true` is persisted instead of a (made-up) diagnosis.
type Brain struct {
	cfg        *Config
	prometheus *PrometheusClient
	loki       *LokiClient
	backend    Backend
	escalator  Escalator
}

func newBrain(cfg *Config, p *PrometheusClient, l *LokiClient, b Backend, e Escalator) *Brain {
	return &Brain{cfg: cfg, prometheus: p, loki: l, backend: b, escalator: e}
}

// Incident is the durable record persisted to incidents.json.
// Each Tick that produces a diagnosis OR escalates appends one of these.
//
// Schema notes (see CLAUDE.md rule 10):
//   - `diagnosis` is always present. For escalated incidents it is
//     zero-valued ({"cause":"","suggested_action":""}); consumers should
//     branch on `escalated` rather than empty-string-checking the
//     diagnosis fields.
//   - `error` carries partial-failure context regardless of escalation
//     (e.g., logs unavailable but diagnosis succeeded; or backend chain
//     failure that triggered escalation).
//   - `escalated` is omitted on success-path lines so existing readers
//     see the original schema unchanged. New readers should treat
//     `escalated: true` as "no diagnosis was obtained — a human was
//     notified".
type Incident struct {
	Timestamp time.Time `json:"timestamp"`
	Alert     Alert     `json:"alert"`
	LogLines  []LogLine `json:"log_lines"`
	Diagnosis Diagnosis `json:"diagnosis"`
	Error     string    `json:"error,omitempty"`
	Escalated bool      `json:"escalated,omitempty"`
}

// Tick runs one full polling cycle. It is safe to call repeatedly and
// never panics — every external failure is logged and swallowed so the
// loop keeps running.
func (b *Brain) Tick(ctx context.Context) {
	alerts, err := b.prometheus.fetchFiringAlerts(ctx)
	if err != nil {
		log.Printf("[brain] prometheus poll failed: %v", err)
		return
	}
	if len(alerts) == 0 {
		log.Printf("[brain] no firing alerts")
		return
	}

	log.Printf("[brain] %d firing alert(s) — diagnosing", len(alerts))
	for _, a := range alerts {
		b.diagnoseAlert(ctx, a)
	}
}

// diagnoseAlert handles a single alert end-to-end: pull logs, ask the LLM,
// log the result, append to disk. Errors are partial-failure friendly.
func (b *Brain) diagnoseAlert(ctx context.Context, alert Alert) {
	var partialErr string

	logs, err := b.loki.fetchRecentLogs(ctx, alert, 10*time.Minute, 50)
	if err != nil {
		log.Printf("[brain] loki query failed for %s: %v", alert.Name(), err)
		partialErr = "loki: " + err.Error()
		// Continue without logs — the LLM can still reason about alert metadata.
		logs = nil
	}

	prompt := buildPrompt(alert, logs)
	diagnosis, err := b.backend.Diagnose(ctx, prompt)
	if err != nil {
		// Every backend failed. Per handoff.md, Aceso must NOT invent a
		// diagnosis when the inference plane is unreachable — it must
		// escalate to a human and persist the escalation as a durable
		// record (so V1's review UI can replay missed cases).
		log.Printf("[brain] diagnose failed for %s: %v — escalating", alert.Name(), err)
		b.escalateAlert(ctx, alert, logs, partialErr, err)
		return
	}

	log.Printf("[brain] diagnosis %s (sev=%s): cause=%q action=%q",
		alert.Name(), alert.Severity(), diagnosis.Cause, diagnosis.SuggestedAction)

	incident := Incident{
		Timestamp: time.Now().UTC(),
		Alert:     alert,
		LogLines:  logs,
		Diagnosis: diagnosis,
		Error:     partialErr,
	}
	if err := appendIncident(b.cfg.IncidentsPath, incident); err != nil {
		log.Printf("[brain] persist failed: %v", err)
	}
}

// escalateAlert is the failure-mode counterpart to the success path:
// notify the human, then persist a record so the incident log shows
// what we couldn't see at decision time. Both steps are best-effort —
// a notification failure must not block persistence, and a persistence
// failure must not silently swallow the escalation.
func (b *Brain) escalateAlert(ctx context.Context, alert Alert, logs []LogLine, partialErr string, backendErr error) {
	if escErr := b.escalator.Escalate(ctx, alert, backendErr); escErr != nil {
		log.Printf("[brain] escalation notification failed: %v", escErr)
	}

	// Compose the on-disk error string so a future reader can tell what
	// failed where: a Loki failure that preceded an Ollama failure looks
	// different from a clean backend-only failure, and we want both
	// signals preserved.
	combinedErr := "backend: " + backendErr.Error()
	if partialErr != "" {
		combinedErr = partialErr + "; " + combinedErr
	}

	incident := Incident{
		Timestamp: time.Now().UTC(),
		Alert:     alert,
		LogLines:  logs,
		Error:     combinedErr,
		Escalated: true,
	}
	if err := appendIncident(b.cfg.IncidentsPath, incident); err != nil {
		log.Printf("[brain] persist (escalated) failed: %v", err)
	}
}

// buildPrompt produces the text prompt sent to Ollama.
//
// We deliberately:
//   - Pin the schema in the prompt itself (belt and braces; format=json is the suspenders).
//   - Sort labels alphabetically so prompt text is stable across polls.
//   - Truncate log lines defensively to keep the small model in its context window.
func buildPrompt(alert Alert, logs []LogLine) string {
	var sb strings.Builder

	sb.WriteString("You are Aceso, a Site Reliability AI. ")
	sb.WriteString("Diagnose the alert below and suggest a single concrete remediation action. ")
	sb.WriteString("Respond ONLY with a JSON object of the form ")
	sb.WriteString(`{"cause": string, "suggested_action": string}`)
	sb.WriteString(". Do not include any other text, markdown, or commentary.\n\n")

	sb.WriteString("ALERT\n")
	sb.WriteString("-----\n")
	fmt.Fprintf(&sb, "name: %s\n", alert.Name())
	fmt.Fprintf(&sb, "severity: %s\n", alert.Severity())
	fmt.Fprintf(&sb, "state: %s\n", alert.State)
	if !alert.ActiveAt.IsZero() {
		fmt.Fprintf(&sb, "active_since: %s\n", alert.ActiveAt.UTC().Format(time.RFC3339))
	}
	if alert.Value != "" {
		fmt.Fprintf(&sb, "current_value: %s\n", alert.Value)
	}
	if t := alert.Threshold(); t != "" {
		fmt.Fprintf(&sb, "threshold: %s\n", t)
	}

	if len(alert.Labels) > 0 {
		sb.WriteString("labels:\n")
		for _, k := range sortedKeys(alert.Labels) {
			fmt.Fprintf(&sb, "  %s: %s\n", k, alert.Labels[k])
		}
	}
	if len(alert.Annotations) > 0 {
		sb.WriteString("annotations:\n")
		for _, k := range sortedKeys(alert.Annotations) {
			fmt.Fprintf(&sb, "  %s: %s\n", k, alert.Annotations[k])
		}
	}

	sb.WriteString("\nRECENT LOGS\n")
	sb.WriteString("-----------\n")
	if len(logs) == 0 {
		sb.WriteString("(no log lines retrieved)\n")
	} else {
		for _, l := range logs {
			line := truncate(strings.TrimSpace(l.Line), 500)
			fmt.Fprintf(&sb, "[%s] %s\n", l.Timestamp.UTC().Format(time.RFC3339), line)
		}
	}

	sb.WriteString("\nReturn the JSON now.")
	return sb.String()
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// appendIncident appends one incident as a JSON line to the configured path.
// We use NDJSON so concurrent appends are tolerable and tail-style consumers
// (e.g., `tail -F incidents.json`) work naturally.
func appendIncident(path string, inc Incident) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("incidents: ensuring dir: %w", err)
		}
	}

	encoded, err := json.Marshal(inc)
	if err != nil {
		return fmt.Errorf("incidents: encoding: %w", err)
	}
	encoded = append(encoded, '\n')

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("incidents: opening file: %w", err)
	}
	defer f.Close()

	if _, err := f.Write(encoded); err != nil {
		return fmt.Errorf("incidents: writing: %w", err)
	}
	return nil
}
