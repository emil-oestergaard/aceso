package main

import (
	"context"
	"fmt"
	"log"
	"strings"
)

// FallbackChain tries each backend in order and returns the first successful
// Diagnosis. If a backend errors, the chain logs the error and continues.
// If every backend fails, the chain returns one wrapped error containing
// every per-backend failure so the operator sees the full failure picture
// without trawling logs.
//
// FallbackChain itself satisfies the Backend interface so Brain can hold a
// single Backend reference without caring whether it is a chain of one or
// a chain of three.
type FallbackChain struct {
	backends []namedBackend
}

// newFallbackChain is the test-friendly constructor; production code goes
// through buildBackendChain (in backends.go) which adds credential filtering
// and BACKEND_ORDER parsing.
func newFallbackChain(backends []namedBackend) *FallbackChain {
	return &FallbackChain{backends: backends}
}

// Diagnose iterates the chain in order. The first backend that returns a
// nil error wins; the rest are not called. Context cancellation short-circuits
// the loop so a stuck backend cannot delay shutdown beyond the per-tick
// deadline set in main.go.
func (c *FallbackChain) Diagnose(ctx context.Context, prompt string) (Diagnosis, error) {
	if len(c.backends) == 0 {
		return Diagnosis{}, fmt.Errorf("fallback: no backends configured")
	}

	errs := make([]string, 0, len(c.backends))
	for _, nb := range c.backends {
		if err := ctx.Err(); err != nil {
			return Diagnosis{}, fmt.Errorf("fallback: context cancelled before %s: %w", nb.name, err)
		}

		diag, err := nb.b.Diagnose(ctx, prompt)
		if err == nil {
			if len(errs) > 0 {
				log.Printf("[chain] %s succeeded after %d earlier failure(s)", nb.name, len(errs))
			}
			return diag, nil
		}
		log.Printf("[chain] backend %s failed: %v", nb.name, err)
		errs = append(errs, fmt.Sprintf("%s: %v", nb.name, err))
	}

	return Diagnosis{}, fmt.Errorf("fallback: all %d backend(s) failed: %s",
		len(c.backends), strings.Join(errs, "; "))
}
