package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all runtime configuration for Aceso.
// Every field is sourced from an environment variable so the agent
// stays 12-factor friendly and easy to ship in containers.
type Config struct {
	PrometheusURL string
	LokiURL       string
	OllamaURL     string
	OllamaModel   string

	// BackendOrder is the comma-separated list of backends the FallbackChain
	// tries in order. V0 only knows one backend ("ollama"); the field exists
	// so future revisions can add a second local path (e.g. an on-VPS small
	// model) without restructuring the chain. Default: ["ollama"].
	BackendOrder []string

	// IncidentsPath is the on-disk file Aceso appends incident records to.
	// In Docker this is mounted from a named volume so history survives restarts.
	IncidentsPath string

	// PollInterval controls how often Aceso pulls firing alerts from Prometheus.
	PollInterval time.Duration

	// HTTPTimeout is applied to every outbound HTTP call (Prometheus, Loki,
	// and every LLM backend). Ollama generations on small models can take a
	// while, so we keep this generous.
	HTTPTimeout time.Duration
}

// loadConfig reads configuration from the process environment.
// Required URLs cause a hard failure; everything else falls back to sane defaults.
func loadConfig() (*Config, error) {
	cfg := &Config{
		PrometheusURL: os.Getenv("PROMETHEUS_URL"),
		LokiURL:       os.Getenv("LOKI_URL"),
		OllamaURL:     os.Getenv("OLLAMA_URL"),
		OllamaModel:   getenvDefault("OLLAMA_MODEL", "gemma2:2b"),
		BackendOrder:  parseCSVDefault("BACKEND_ORDER", []string{"ollama"}),
		IncidentsPath: getenvDefault("INCIDENTS_PATH", "/data/incidents.json"),
		PollInterval:  parseSecondsDefault("POLL_INTERVAL_SECONDS", 30),
		HTTPTimeout:   parseSecondsDefault("HTTP_TIMEOUT_SECONDS", 120),
	}

	missing := []string{}
	if cfg.PrometheusURL == "" {
		missing = append(missing, "PROMETHEUS_URL")
	}
	if cfg.LokiURL == "" {
		missing = append(missing, "LOKI_URL")
	}
	if cfg.OllamaURL == "" {
		missing = append(missing, "OLLAMA_URL")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required environment variables: %v", missing)
	}

	return cfg, nil
}

func getenvDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func parseSecondsDefault(key string, fallback int) time.Duration {
	raw := os.Getenv(key)
	if raw == "" {
		return time.Duration(fallback) * time.Second
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return time.Duration(fallback) * time.Second
	}
	return time.Duration(n) * time.Second
}

// parseCSVDefault reads a comma-separated env var and returns a trimmed,
// lower-cased, empty-stripped slice.
//
// Fallback semantics: the function returns the supplied default when the
// env var is unset *or* when every entry is empty after trimming (e.g.
// ", , ,"). This favours "broken value → safe default" over "broken
// value → no backends" for the BACKEND_ORDER caller in this file. If you
// ever need a parser where an explicit-but-empty value should override
// the default, write a sibling helper rather than changing this contract.
func parseCSVDefault(key string, fallback []string) []string {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		t := strings.ToLower(strings.TrimSpace(p))
		if t != "" {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return fallback
	}
	return out
}
