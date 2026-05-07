// Aceso — a self-healing AI agent for VPS observability.
//
// V0 scope: observe and diagnose. Every PollInterval we:
//  1. ask Prometheus what's firing,
//  2. ask Loki for the recent logs from each affected target,
//  3. ask Ollama what it thinks the cause and remediation are,
//  4. log the diagnosis and append the incident to /data/incidents.json.
//
// V0 deliberately does NOT execute remediations. That's a later milestone.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	// Standard library logger for V0. Structured logging can come later
	// once we know what we want to query against.
	log.SetFlags(log.LstdFlags | log.LUTC)
	log.SetPrefix("aceso ")

	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	ntfyConfigured := cfg.EscalateNtfyURL != ""
	log.Printf("starting | prometheus=%s loki=%s ollama=%s model=%s order=%v ntfy=%t interval=%s",
		cfg.PrometheusURL, cfg.LokiURL, cfg.OllamaURL, cfg.OllamaModel, cfg.BackendOrder, ntfyConfigured, cfg.PollInterval)

	prom := newPrometheusClient(cfg.PrometheusURL, cfg.HTTPTimeout)
	loki := newLokiClient(cfg.LokiURL, cfg.HTTPTimeout)
	ollama := newOllamaClient(cfg.OllamaURL, cfg.OllamaModel, cfg.HTTPTimeout)

	chain, err := buildBackendChain(cfg, ollama)
	if err != nil {
		log.Fatalf("backend chain: %v", err)
	}
	escalator := newNtfyEscalator(cfg.EscalateNtfyURL, cfg.HTTPTimeout)
	brain := newBrain(cfg, prom, loki, chain, escalator)

	// Cancellable root context, cancelled on SIGINT/SIGTERM so a `docker stop`
	// or Ctrl-C exits cleanly mid-tick.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		s := <-sigs
		log.Printf("received %s — shutting down", s)
		cancel()
	}()

	// First tick happens immediately so operators don't have to wait a full
	// interval just to confirm the agent is healthy.
	tickWithTimeout(ctx, brain, cfg.HTTPTimeout)

	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Printf("stopped")
			return
		case <-ticker.C:
			tickWithTimeout(ctx, brain, cfg.HTTPTimeout)
		}
	}
}

// tickWithTimeout runs a single polling cycle under a per-tick deadline.
// Each tick is bounded so a hung backend can't stall the loop.
func tickWithTimeout(parent context.Context, brain *Brain, timeout time.Duration) {
	tickCtx, cancel := context.WithTimeout(parent, timeout*2)
	defer cancel()
	brain.Tick(tickCtx)
}
