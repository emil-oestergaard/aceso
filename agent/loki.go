package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// LogLine is a flattened log entry pulled from Loki.
// We carry only the fields the LLM needs in its prompt.
type LogLine struct {
	Timestamp time.Time         `json:"timestamp"`
	Line      string            `json:"line"`
	Stream    map[string]string `json:"stream"`
}

// LokiClient queries the Loki HTTP API for recent log lines.
type LokiClient struct {
	baseURL string
	http    *http.Client
}

func newLokiClient(baseURL string, timeout time.Duration) *LokiClient {
	return &LokiClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: timeout},
	}
}

// lokiQueryResponse mirrors a subset of the /loki/api/v1/query_range envelope.
// Only the streams result type is supported in V0 — that's what label queries
// return. Matrix responses are rejected upstream.
type lokiQueryResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Stream map[string]string `json:"stream"`
			// Values is an array of [unixNanoString, line] tuples.
			Values [][2]string `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

// fetchRecentLogs returns the most recent log lines that match the alert.
// labelSelector is built from the alert's label set; lookback controls how
// far back we read; limit caps the number of returned lines.
func (c *LokiClient) fetchRecentLogs(ctx context.Context, alert Alert, lookback time.Duration, limit int) ([]LogLine, error) {
	selector := buildLogQL(alert.Labels)
	if selector == "" {
		// Nothing to scope by — refuse to scrape the entire cluster's logs.
		return nil, nil
	}

	end := time.Now().UTC()
	start := end.Add(-lookback)

	endpoint, err := url.JoinPath(c.baseURL, "/loki/api/v1/query_range")
	if err != nil {
		return nil, fmt.Errorf("loki: building URL: %w", err)
	}

	q := url.Values{}
	q.Set("query", selector)
	q.Set("start", strconv.FormatInt(start.UnixNano(), 10))
	q.Set("end", strconv.FormatInt(end.UnixNano(), 10))
	q.Set("limit", strconv.Itoa(limit))
	q.Set("direction", "backward")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+q.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("loki: building request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("loki: request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("loki: reading body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("loki: unexpected status %d: %s", resp.StatusCode, truncate(string(body), 200))
	}

	var parsed lokiQueryResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("loki: decoding json: %w", err)
	}
	if parsed.Status != "success" {
		return nil, fmt.Errorf("loki: api status %q", parsed.Status)
	}
	if parsed.Data.ResultType != "streams" {
		return nil, fmt.Errorf("loki: unsupported resultType %q", parsed.Data.ResultType)
	}

	lines := make([]LogLine, 0)
	for _, stream := range parsed.Data.Result {
		for _, v := range stream.Values {
			ts, err := strconv.ParseInt(v[0], 10, 64)
			if err != nil {
				continue
			}
			lines = append(lines, LogLine{
				Timestamp: time.Unix(0, ts).UTC(),
				Line:      v[1],
				Stream:    stream.Stream,
			})
		}
	}

	// Newest first — matches "direction=backward" semantically and gives
	// the LLM the freshest context first.
	sort.Slice(lines, func(i, j int) bool {
		return lines[i].Timestamp.After(lines[j].Timestamp)
	})
	if len(lines) > limit {
		lines = lines[:limit]
	}
	return lines, nil
}

// buildLogQL turns alert labels into a LogQL stream selector.
// We prefer high-cardinality identity labels (instance, job, container,
// namespace, pod, service) and fall back to nothing if none are present.
//
// Example: {instance="vps01", job="node"}
func buildLogQL(labels map[string]string) string {
	preferred := []string{"job", "instance", "container", "namespace", "pod", "service", "app"}

	pairs := make([]string, 0, len(preferred))
	for _, key := range preferred {
		if v, ok := labels[key]; ok && v != "" {
			pairs = append(pairs, fmt.Sprintf("%s=%q", key, v))
		}
	}
	if len(pairs) == 0 {
		return ""
	}
	return "{" + strings.Join(pairs, ",") + "}"
}
