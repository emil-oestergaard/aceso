package main

import (
	"fmt"
	"os"
	"strconv"
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

	// IncidentsPath is the on-disk file Aceso appends incident records to.
	// In Docker this is mounted from a named volume so history survives restarts.
	IncidentsPath string

	// PollInterval controls how often Aceso pulls firing alerts from Prometheus.
	PollInterval time.Duration

	// HTTPTimeout is applied to every outbound HTTP call (Prometheus, Loki, Ollama).
	// Ollama generations on small models can take a while, so we keep this generous.
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
