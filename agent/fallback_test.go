package main

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeBackend is an in-process Backend used by fallback tests. It records
// how many times Diagnose was called and returns the canned outcome.
type fakeBackend struct {
	calls atomic.Int32
	diag  Diagnosis
	err   error
}

func (f *fakeBackend) Diagnose(ctx context.Context, prompt string) (Diagnosis, error) {
	f.calls.Add(1)
	if f.err != nil {
		return Diagnosis{}, f.err
	}
	return f.diag, nil
}

func TestFallbackChainSucceedsOnFirstHealthyBackend(t *testing.T) {
	t.Parallel()

	primary := &fakeBackend{diag: Diagnosis{Cause: "primary cause", SuggestedAction: "primary action"}}
	secondary := &fakeBackend{diag: Diagnosis{Cause: "secondary cause", SuggestedAction: "secondary action"}}
	tertiary := &fakeBackend{err: errors.New("should never be called")}

	chain := newFallbackChain([]namedBackend{
		{name: "primary", b: primary},
		{name: "secondary", b: secondary},
		{name: "tertiary", b: tertiary},
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	got, err := chain.Diagnose(ctx, "diagnose this")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Cause != "primary cause" || got.SuggestedAction != "primary action" {
		t.Errorf("got %+v, want primary diagnosis", got)
	}
	if primary.calls.Load() != 1 {
		t.Errorf("primary.calls = %d, want 1", primary.calls.Load())
	}
	if secondary.calls.Load() != 0 {
		t.Errorf("secondary should not have been called, got %d calls", secondary.calls.Load())
	}
	if tertiary.calls.Load() != 0 {
		t.Errorf("tertiary should not have been called, got %d calls", tertiary.calls.Load())
	}
}

func TestFallbackChainFallsThroughOnFailure(t *testing.T) {
	t.Parallel()

	primary := &fakeBackend{err: errors.New("primary down")}
	secondary := &fakeBackend{diag: Diagnosis{Cause: "secondary cause", SuggestedAction: "secondary action"}}
	tertiary := &fakeBackend{err: errors.New("should not reach tertiary")}

	chain := newFallbackChain([]namedBackend{
		{name: "primary", b: primary},
		{name: "secondary", b: secondary},
		{name: "tertiary", b: tertiary},
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	got, err := chain.Diagnose(ctx, "diagnose this")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Cause != "secondary cause" {
		t.Errorf("got cause %q, want %q", got.Cause, "secondary cause")
	}
	if primary.calls.Load() != 1 {
		t.Errorf("primary.calls = %d, want 1", primary.calls.Load())
	}
	if secondary.calls.Load() != 1 {
		t.Errorf("secondary.calls = %d, want 1", secondary.calls.Load())
	}
	if tertiary.calls.Load() != 0 {
		t.Errorf("tertiary should not have been called once secondary succeeded; got %d", tertiary.calls.Load())
	}
}

func TestFallbackChainAllBackendsFailReturnsWrappedError(t *testing.T) {
	t.Parallel()

	primary := &fakeBackend{err: errors.New("primary boom")}
	secondary := &fakeBackend{err: errors.New("secondary boom")}
	tertiary := &fakeBackend{err: errors.New("tertiary boom")}

	chain := newFallbackChain([]namedBackend{
		{name: "primary", b: primary},
		{name: "secondary", b: secondary},
		{name: "tertiary", b: tertiary},
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err := chain.Diagnose(ctx, "diagnose this")
	if err == nil {
		t.Fatal("expected error when every backend fails, got nil")
	}
	for _, want := range []string{
		"all 3 backend(s) failed",
		"primary: primary boom",
		"secondary: secondary boom",
		"tertiary: tertiary boom",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want substring %q", err.Error(), want)
		}
	}
	for _, b := range []*fakeBackend{primary, secondary, tertiary} {
		if b.calls.Load() != 1 {
			t.Errorf("each backend should have been called exactly once on full failure, got %d", b.calls.Load())
		}
	}
}

func TestFallbackChainEmptyReturnsError(t *testing.T) {
	t.Parallel()

	chain := newFallbackChain(nil)

	_, err := chain.Diagnose(context.Background(), "anything")
	if err == nil {
		t.Fatal("expected error for empty chain, got nil")
	}
	if !strings.Contains(err.Error(), "no backends configured") {
		t.Errorf("err = %q, want 'no backends configured' substring", err.Error())
	}
}

func TestFallbackChainCancelledContextShortCircuits(t *testing.T) {
	t.Parallel()

	primary := &fakeBackend{err: errors.New("primary boom")}
	secondary := &fakeBackend{diag: Diagnosis{Cause: "should not reach", SuggestedAction: "x"}}

	chain := newFallbackChain([]namedBackend{
		{name: "primary", b: primary},
		{name: "secondary", b: secondary},
	})

	// Pre-cancelled context: ctx.Err() != nil before the loop body runs.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := chain.Diagnose(ctx, "anything")
	if err == nil {
		t.Fatal("expected error for cancelled context, got nil")
	}
	if !strings.Contains(err.Error(), "context cancelled") {
		t.Errorf("err = %q, want 'context cancelled' substring", err.Error())
	}
	if primary.calls.Load() != 0 || secondary.calls.Load() != 0 {
		t.Errorf("no backend should be called when ctx is cancelled before entry, got primary=%d secondary=%d",
			primary.calls.Load(), secondary.calls.Load())
	}
}

func TestBuildBackendChainSkipsBackendsWithoutCredentials(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		BackendOrder: []string{"ollama", "deepseek", "gemini"},
		HTTPTimeout:  time.Second,
		// No DeepSeekAPIKey, no GeminiAPIKey — both should be skipped.
	}
	ollama := newOllamaClient("http://example.invalid", "gemma2:2b", time.Second)

	chain, err := buildBackendChain(cfg, ollama)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chain.backends) != 1 {
		t.Fatalf("len(chain.backends) = %d, want 1 (only ollama is usable)", len(chain.backends))
	}
	if chain.backends[0].name != "ollama" {
		t.Errorf("first backend name = %q, want ollama", chain.backends[0].name)
	}
}

func TestBuildBackendChainErrorsWhenAllSkipped(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		BackendOrder: []string{"deepseek", "gemini", "garbage"},
		HTTPTimeout:  time.Second,
	}
	ollama := newOllamaClient("http://example.invalid", "gemma2:2b", time.Second)

	_, err := buildBackendChain(cfg, ollama)
	if err == nil {
		t.Fatal("expected error when no backends are usable, got nil")
	}
	if !strings.Contains(err.Error(), "no usable backends") {
		t.Errorf("err = %q, want 'no usable backends' substring", err.Error())
	}
}

func TestBuildBackendChainHonoursOrder(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		BackendOrder:   []string{"gemini", "deepseek", "ollama"},
		DeepSeekAPIKey: "sk-test",
		GeminiAPIKey:   "ai-test",
		HTTPTimeout:    time.Second,
	}
	ollama := newOllamaClient("http://example.invalid", "gemma2:2b", time.Second)

	chain, err := buildBackendChain(cfg, ollama)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	gotNames := make([]string, len(chain.backends))
	for i, nb := range chain.backends {
		gotNames[i] = nb.name
	}
	wantNames := []string{"gemini", "deepseek", "ollama"}
	if strings.Join(gotNames, ",") != strings.Join(wantNames, ",") {
		t.Errorf("chain order = %v, want %v", gotNames, wantNames)
	}
}
