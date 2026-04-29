package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Alert is a normalized view of a single Prometheus alert.
// We intentionally keep it minimal — only the fields Aceso reasons about.
type Alert struct {
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	State       string            `json:"state"`
	ActiveAt    time.Time         `json:"activeAt"`
	Value       string            `json:"value"`
}

// Name returns the alertname label, or "<unnamed>" if absent.
func (a Alert) Name() string {
	if n, ok := a.Labels["alertname"]; ok && n != "" {
		return n
	}
	return "<unnamed>"
}

// Severity returns the severity label, or "unknown" if absent.
func (a Alert) Severity() string {
	if s, ok := a.Labels["severity"]; ok && s != "" {
		return s
	}
	return "unknown"
}

// Threshold returns the threshold annotation if present, else "".
// Prometheus alerting rules conventionally publish thresholds via annotations.
func (a Alert) Threshold() string {
	if t, ok := a.Annotations["threshold"]; ok {
		return t
	}
	return ""
}

// Fingerprint returns a stable identifier for an alert based on its label set.
func (a Alert) Fingerprint() string {
	parts := make([]string, 0, len(a.Labels))
	for k, v := range a.Labels {
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, ",")
}

// PrometheusClient queries the Prometheus HTTP API.
type PrometheusClient struct {
	baseURL string
	http    *http.Client
}

func newPrometheusClient(baseURL string, timeout time.Duration) *PrometheusClient {
	return &PrometheusClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: timeout},
	}
}

// prometheusAlertsResponse mirrors the /api/v1/alerts envelope.
type prometheusAlertsResponse struct {
	Status string `json:"status"`
	Data   struct {
		Alerts []Alert `json:"alerts"`
	} `json:"data"`
	ErrorType string `json:"errorType"`
	Error     string `json:"error"`
}

// fetchFiringAlerts returns only alerts currently in state "firing".
// Pending and resolved alerts are filtered out so the agent doesn't waste
// LLM cycles diagnosing noise.
func (c *PrometheusClient) fetchFiringAlerts(ctx context.Context) ([]Alert, error) {
	endpoint, err := url.JoinPath(c.baseURL, "/api/v1/alerts")
	if err != nil {
		return nil, fmt.Errorf("prometheus: building URL: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("prometheus: building request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("prometheus: request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("prometheus: reading body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("prometheus: unexpected status %d: %s", resp.StatusCode, truncate(string(body), 200))
	}

	var parsed prometheusAlertsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("prometheus: decoding json: %w", err)
	}
	if parsed.Status != "success" {
		return nil, fmt.Errorf("prometheus: api error: %s: %s", parsed.ErrorType, parsed.Error)
	}

	firing := make([]Alert, 0, len(parsed.Data.Alerts))
	for _, a := range parsed.Data.Alerts {
		if strings.EqualFold(a.State, "firing") {
			firing = append(firing, a)
		}
	}
	return firing, nil
}

// truncate is a small helper for safe error logging.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
