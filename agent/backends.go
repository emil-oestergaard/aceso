package main

import (
	"context"
	"fmt"
	"log"
)

// Backend is the small interface every LLM backend implements.
// Single-method on purpose — the FallbackChain iterates over heterogeneous
// backends without any type-switching, and tests can fake a Backend with a
// 5-line struct literal.
//
// Aceso is local-only by design: see CLAUDE.md "Inference is local-only".
// Any backend implementation must keep traffic on the local network or a
// Tailscale tunnel. Third-party LLM APIs (DeepSeek, Gemini, OpenAI, etc.)
// are explicitly out of scope — production logs may contain hostnames,
// user IDs, stack traces, and other data that must not leave the
// operator's infrastructure.
type Backend interface {
	Diagnose(ctx context.Context, prompt string) (Diagnosis, error)
}

// namedBackend pairs a Backend with a human-readable name for chain logs.
// Names come from BACKEND_ORDER (e.g. "ollama") so an operator reading
// the logs can tell which hop produced or failed a diagnosis.
type namedBackend struct {
	name string
	b    Backend
}

// ----------------------------------------------------------------------------
// Ollama (local or Tailscale-reachable Pi)
// ----------------------------------------------------------------------------

// OllamaBackend adapts the existing OllamaClient (see ollama.go) to the
// Backend interface. The wrapper deliberately doesn't duplicate HTTP logic —
// it forwards to OllamaClient.diagnose so all Ollama traffic stays in one
// tested code path.
type OllamaBackend struct {
	client *OllamaClient
}

func newOllamaBackend(c *OllamaClient) *OllamaBackend {
	return &OllamaBackend{client: c}
}

func (b *OllamaBackend) Diagnose(ctx context.Context, prompt string) (Diagnosis, error) {
	return b.client.diagnose(ctx, prompt)
}

// ----------------------------------------------------------------------------
// Chain builder
// ----------------------------------------------------------------------------

// buildBackendChain materializes a FallbackChain from the configured order.
//
// V0 only knows one backend: "ollama". The switch's default branch logs and
// skips unknown names so a typo in BACKEND_ORDER doesn't silently break
// startup, but if the resulting chain is empty we hard-fail — running the
// agent without any inference path is a misconfiguration, not a degraded
// mode.
//
// Cloud backends (deepseek, gemini, openai, etc.) are deliberately not
// listed here. The binary cannot exfiltrate to a third-party LLM API even
// if misconfigured because the code paths simply do not exist. This is
// defense in depth on top of the architectural rule documented in
// CLAUDE.md.
func buildBackendChain(cfg *Config, ollama *OllamaClient) (*FallbackChain, error) {
	var chain []namedBackend
	// cfg.BackendOrder is already lower-cased, trimmed, and empty-stripped
	// by parseCSVDefault (see config.go), so the switch can match the raw
	// entry directly.
	for _, name := range cfg.BackendOrder {
		switch name {
		case "":
			continue
		case "ollama":
			chain = append(chain, namedBackend{name: "ollama", b: newOllamaBackend(ollama)})
		default:
			log.Printf("[chain] skipping unknown backend: %q (only \"ollama\" is supported in V0)", name)
		}
	}
	if len(chain) == 0 {
		return nil, fmt.Errorf("backend chain: no usable backends from BACKEND_ORDER=%v", cfg.BackendOrder)
	}
	return &FallbackChain{backends: chain}, nil
}
