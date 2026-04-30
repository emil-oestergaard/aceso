package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestFetchFiringAlerts exercises the happy paths and the firing-state filter.
// We use httptest.Server fixtures rather than mocks so we test the real
// HTTP wire format Prometheus actually returns.
func TestFetchFiringAlerts(t *testing.T) {
	t.Parallel()

	const firingResponse = `{
		"status": "success",
		"data": {
			"alerts": [
				{
					"labels": {"alertname": "HighCPU", "severity": "warning", "instance": "vps-01"},
					"annotations": {"summary": "CPU above 80%", "threshold": "80"},
					"state": "firing",
					"activeAt": "2026-04-29T22:00:00Z",
					"value": "0.92"
				}
			]
		}
	}`

	const emptyResponse = `{
		"status": "success",
		"data": {"alerts": []}
	}`

	const mixedStatesResponse = `{
		"status": "success",
		"data": {
			"alerts": [
				{"labels": {"alertname": "A"}, "state": "firing"},
				{"labels": {"alertname": "B"}, "state": "pending"},
				{"labels": {"alertname": "C"}, "state": "inactive"},
				{"labels": {"alertname": "D"}, "state": "firing"},
				{"labels": {"alertname": "E"}, "state": "FIRING"}
			]
		}
	}`

	tests := []struct {
		name           string
		status         int
		body           string
		wantErr        bool
		wantErrSubstr  string
		wantCount      int
		wantNamesAtoZ  []string
		assertFirstHit func(t *testing.T, a Alert)
	}{
		{
			name:      "single firing alert is parsed",
			status:    http.StatusOK,
			body:      firingResponse,
			wantCount: 1,
			assertFirstHit: func(t *testing.T, a Alert) {
				t.Helper()
				if a.Name() != "HighCPU" {
					t.Errorf("Name() = %q, want %q", a.Name(), "HighCPU")
				}
				if a.Severity() != "warning" {
					t.Errorf("Severity() = %q, want %q", a.Severity(), "warning")
				}
				if a.Threshold() != "80" {
					t.Errorf("Threshold() = %q, want %q", a.Threshold(), "80")
				}
				if a.Value != "0.92" {
					t.Errorf("Value = %q, want %q", a.Value, "0.92")
				}
				if a.ActiveAt.IsZero() {
					t.Errorf("ActiveAt should be parsed, got zero time")
				}
			},
		},
		{
			name:      "empty alerts list returns empty slice without panic",
			status:    http.StatusOK,
			body:      emptyResponse,
			wantCount: 0,
		},
		{
			name:          "non-2xx status is propagated as error",
			status:        http.StatusInternalServerError,
			body:          `internal server error`,
			wantErr:       true,
			wantErrSubstr: "unexpected status 500",
		},
		{
			name:          "malformed json is propagated as error",
			status:        http.StatusOK,
			body:          `{not json`,
			wantErr:       true,
			wantErrSubstr: "decoding json",
		},
		{
			name:          "api-level error is propagated",
			status:        http.StatusOK,
			body:          `{"status":"error","errorType":"bad_data","error":"oops"}`,
			wantErr:       true,
			wantErrSubstr: "api error",
		},
		{
			name:          "only firing-state alerts are kept",
			status:        http.StatusOK,
			body:          mixedStatesResponse,
			wantCount:     3,
			wantNamesAtoZ: []string{"A", "D", "E"},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var gotPath string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(srv.Close)

			client := newPrometheusClient(srv.URL, 2*time.Second)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			alerts, err := client.fetchFiringAlerts(ctx)

			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if tc.wantErrSubstr != "" && !strings.Contains(err.Error(), tc.wantErrSubstr) {
					t.Errorf("err = %q, want substring %q", err.Error(), tc.wantErrSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if gotPath != "/api/v1/alerts" {
				t.Errorf("hit path = %q, want %q", gotPath, "/api/v1/alerts")
			}
			if len(alerts) != tc.wantCount {
				t.Fatalf("len(alerts) = %d, want %d (alerts=%+v)", len(alerts), tc.wantCount, alerts)
			}
			if tc.wantNamesAtoZ != nil {
				for i, want := range tc.wantNamesAtoZ {
					if alerts[i].Name() != want {
						t.Errorf("alerts[%d].Name() = %q, want %q", i, alerts[i].Name(), want)
					}
				}
			}
			if tc.assertFirstHit != nil && len(alerts) > 0 {
				tc.assertFirstHit(t, alerts[0])
			}
		})
	}
}

// TestFetchFiringAlertsRequestFails covers the transport-error path
// (server is closed before the request fires).
func TestFetchFiringAlertsRequestFails(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Close() // immediately, so the dial fails

	client := newPrometheusClient(srv.URL, 500*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	_, err := client.fetchFiringAlerts(ctx)
	if err == nil {
		t.Fatalf("expected error from closed server, got nil")
	}
	if !strings.Contains(err.Error(), "prometheus:") {
		t.Errorf("err = %q, want it to be wrapped with prometheus: prefix", err.Error())
	}
}
