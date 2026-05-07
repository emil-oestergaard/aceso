package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixtureAlert returns a populated Alert that exercises every branch
// buildPrompt has (labels, annotations, value, threshold, active_since).
func fixtureAlert() Alert {
	return Alert{
		Labels: map[string]string{
			"alertname": "HighCPU",
			"severity":  "warning",
			"instance":  "vps-01",
			"job":       "node",
		},
		Annotations: map[string]string{
			"summary":   "CPU above 80%",
			"threshold": "80",
		},
		State:    "firing",
		ActiveAt: time.Date(2026, 4, 29, 22, 0, 0, 0, time.UTC),
		Value:    "0.92",
	}
}

func TestBuildPromptIncludesAllAlertFields(t *testing.T) {
	t.Parallel()

	logs := []LogLine{
		{
			Timestamp: time.Date(2026, 4, 29, 22, 1, 0, 0, time.UTC),
			Line:      "kernel: oom-kill: process 1234 (nginx)",
		},
		{
			Timestamp: time.Date(2026, 4, 29, 22, 2, 0, 0, time.UTC),
			Line:      "systemd: nginx.service: main process exited",
		},
	}

	got := buildPrompt(fixtureAlert(), logs)

	wantSubstrs := []string{
		// header / schema pin
		`{"cause": string, "suggested_action": string}`,
		// alert fields
		"name: HighCPU",
		"severity: warning",
		"state: firing",
		"current_value: 0.92",
		"threshold: 80",
		"active_since: 2026-04-29T22:00:00Z",
		// labels (sorted alphabetically)
		"  alertname: HighCPU",
		"  instance: vps-01",
		"  job: node",
		"  severity: warning",
		// annotations
		"  summary: CPU above 80%",
		"  threshold: 80",
		// log lines
		"kernel: oom-kill: process 1234 (nginx)",
		"systemd: nginx.service: main process exited",
		"[2026-04-29T22:01:00Z]",
		// section headers
		"ALERT",
		"RECENT LOGS",
	}
	for _, s := range wantSubstrs {
		if !strings.Contains(got, s) {
			t.Errorf("prompt missing substring %q\n--- prompt ---\n%s", s, got)
		}
	}
}

func TestBuildPromptLabelsAreSorted(t *testing.T) {
	t.Parallel()

	got := buildPrompt(fixtureAlert(), nil)

	// Verify labels appear in alphabetical order so the prompt is stable
	// across polls (matters for caching and for diffing prompts during review).
	idxAlertname := strings.Index(got, "  alertname: HighCPU")
	idxInstance := strings.Index(got, "  instance: vps-01")
	idxJob := strings.Index(got, "  job: node")
	idxSeverity := strings.Index(got, "  severity: warning")

	for _, idx := range []int{idxAlertname, idxInstance, idxJob, idxSeverity} {
		if idx < 0 {
			t.Fatalf("expected all four label lines present, got prompt:\n%s", got)
		}
	}
	if !(idxAlertname < idxInstance && idxInstance < idxJob && idxJob < idxSeverity) {
		t.Errorf("labels not in alphabetical order: alertname=%d instance=%d job=%d severity=%d",
			idxAlertname, idxInstance, idxJob, idxSeverity)
	}
}

func TestBuildPromptHandlesNoLogs(t *testing.T) {
	t.Parallel()

	got := buildPrompt(fixtureAlert(), nil)

	if !strings.Contains(got, "(no log lines retrieved)") {
		t.Errorf("expected no-logs sentinel in prompt, got:\n%s", got)
	}
}

func TestBuildPromptTruncatesLongLogLines(t *testing.T) {
	t.Parallel()

	// 800-char log line — comfortably above the 500-char defensive limit
	// in buildPrompt (truncate(line, 500)).
	longLine := strings.Repeat("A", 800)
	logs := []LogLine{
		{
			Timestamp: time.Date(2026, 4, 29, 22, 1, 0, 0, time.UTC),
			Line:      longLine,
		},
	}

	got := buildPrompt(fixtureAlert(), logs)

	// The full 800-char line must NOT appear verbatim.
	if strings.Contains(got, longLine) {
		t.Errorf("prompt contains untruncated 800-char line; truncation did not fire")
	}
	// truncate() appends "..." so a truncated line is exactly 500 As followed by "...".
	truncated := strings.Repeat("A", 500) + "..."
	if !strings.Contains(got, truncated) {
		t.Errorf("expected truncated 500-char body + '...' in prompt; got len=%d", len(got))
	}
	// And the prompt itself must remain bounded.
	if len(got) > 4_000 {
		t.Errorf("prompt unexpectedly large: %d bytes (truncation should keep it bounded)", len(got))
	}
}

func TestBuildPromptOmitsOptionalFieldsWhenAbsent(t *testing.T) {
	t.Parallel()

	minimal := Alert{
		Labels: map[string]string{"alertname": "BareAlert"},
		State:  "firing",
		// no Value, no ActiveAt, no Threshold annotation, no annotations map
	}

	got := buildPrompt(minimal, nil)

	if !strings.Contains(got, "name: BareAlert") {
		t.Errorf("expected name in prompt, got:\n%s", got)
	}
	if !strings.Contains(got, "state: firing") {
		t.Errorf("expected state in prompt, got:\n%s", got)
	}
	for _, banned := range []string{"current_value:", "active_since:", "threshold:"} {
		if strings.Contains(got, banned) {
			t.Errorf("expected %q to be omitted when absent; full prompt:\n%s", banned, got)
		}
	}
}

// fakeFailingBackend always returns an error from Diagnose. Used by the
// escalation test to drive the failure path of Brain.diagnoseAlert without
// any HTTP traffic.
type fakeFailingBackend struct{ err error }

func (f *fakeFailingBackend) Diagnose(context.Context, string) (Diagnosis, error) {
	return Diagnosis{}, f.err
}

// recordingEscalator captures the args of the last Escalate call so the
// test can assert that escalation actually fired with the right alert and
// the underlying backend error.
type recordingEscalator struct {
	calls       int
	lastAlert   Alert
	lastErr     error
	returnError error
}

func (r *recordingEscalator) Escalate(_ context.Context, alert Alert, err error) error {
	r.calls++
	r.lastAlert = alert
	r.lastErr = err
	return r.returnError
}

// TestDiagnoseAlertEscalatesOnBackendFailure pins the new escalation
// behaviour: when the backend chain returns an error, the Brain must
//
//  1. notify the human via Escalator.Escalate (single call, with the
//     original backend error preserved), AND
//  2. persist an Incident with `escalated: true` so the on-disk log
//     reflects what the agent could not see at decision time.
//
// Both halves are load-bearing — silently dropping the alert or silently
// inventing a diagnosis would each be category errors per handoff.md.
func TestDiagnoseAlertEscalatesOnBackendFailure(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	incidentsPath := filepath.Join(tmp, "incidents.json")

	cfg := &Config{IncidentsPath: incidentsPath, HTTPTimeout: time.Second}
	failing := &fakeFailingBackend{err: errors.New("synthetic-failure")}
	rec := &recordingEscalator{}

	// We don't actually exercise prometheus / loki here — diagnoseAlert
	// is called directly with a hand-built Alert, and Loki calls are
	// allowed to fail because the logs are nil-safe in the prompt and
	// in the persisted incident.
	brain := newBrain(cfg, nil, newLokiClient("http://example.invalid", time.Second), failing, rec)

	alert := Alert{
		Labels: map[string]string{"alertname": "BareAlert"},
		State:  "firing",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	brain.diagnoseAlert(ctx, alert)

	if rec.calls != 1 {
		t.Fatalf("escalator.calls = %d, want 1", rec.calls)
	}
	if rec.lastAlert.Name() != "BareAlert" {
		t.Errorf("escalated alert name = %q, want BareAlert", rec.lastAlert.Name())
	}
	if rec.lastErr == nil || !strings.Contains(rec.lastErr.Error(), "synthetic-failure") {
		t.Errorf("escalated err = %v, want substring 'synthetic-failure'", rec.lastErr)
	}

	raw, err := os.ReadFile(incidentsPath)
	if err != nil {
		t.Fatalf("reading incidents file: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("incidents file has %d lines, want 1\n--- contents ---\n%s", len(lines), raw)
	}

	var inc Incident
	if err := json.Unmarshal([]byte(lines[0]), &inc); err != nil {
		t.Fatalf("decoding persisted incident: %v\n--- raw ---\n%s", err, lines[0])
	}
	if !inc.Escalated {
		t.Errorf("persisted incident has Escalated=false, want true; raw=%s", lines[0])
	}
	if inc.Diagnosis.Cause != "" || inc.Diagnosis.SuggestedAction != "" {
		t.Errorf("escalated incident must not carry an invented diagnosis, got %+v", inc.Diagnosis)
	}
	if !strings.Contains(inc.Error, "backend: synthetic-failure") {
		t.Errorf("inc.Error = %q, want 'backend: synthetic-failure' substring", inc.Error)
	}
}
