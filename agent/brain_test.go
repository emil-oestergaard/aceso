package main

import (
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
