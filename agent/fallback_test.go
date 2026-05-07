package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeBackend is an in-process Backend used by fallback tests. It records
// how many times Diagnose was called and returns the canned outcome.
//
// calls is a plain int because the chain runs serially — no goroutine is
// ever spawned by FallbackChain.Diagnose, so the field is accessed from
// exactly one goroutine. If the chain ever goes parallel, the race
// detector will flag the test and we switch to atomic.Int32 then.
type fakeBackend struct {
	calls int
	diag  Diagnosis
	err   error
}

func (f *fakeBackend) Diagnose(ctx context.Context, prompt string) (Diagnosis, error) {
	f.calls++
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
	if primary.calls != 1 {
		t.Errorf("primary.calls = %d, want 1", primary.calls)
	}
	if secondary.calls != 0 {
		t.Errorf("secondary should not have been called, got %d calls", secondary.calls)
	}
	if tertiary.calls != 0 {
		t.Errorf("tertiary should not have been called, got %d calls", tertiary.calls)
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
	if primary.calls != 1 {
		t.Errorf("primary.calls = %d, want 1", primary.calls)
	}
	if secondary.calls != 1 {
		t.Errorf("secondary.calls = %d, want 1", secondary.calls)
	}
	if tertiary.calls != 0 {
		t.Errorf("tertiary should not have been called once secondary succeeded; got %d", tertiary.calls)
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
		if b.calls != 1 {
			t.Errorf("each backend should have been called exactly once on full failure, got %d", b.calls)
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
	if primary.calls != 0 || secondary.calls != 0 {
		t.Errorf("no backend should be called when ctx is cancelled before entry, got primary=%d secondary=%d",
			primary.calls, secondary.calls)
	}
}

func TestBuildBackendChainDefaultOrder(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		BackendOrder: []string{"ollama"},
		HTTPTimeout:  time.Second,
	}
	ollama := newOllamaClient("http://example.invalid", "gemma2:2b", time.Second)

	chain, err := buildBackendChain(cfg, ollama)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chain.backends) != 1 {
		t.Fatalf("len(chain.backends) = %d, want 1", len(chain.backends))
	}
	if chain.backends[0].name != "ollama" {
		t.Errorf("first backend name = %q, want ollama", chain.backends[0].name)
	}
}

// TestBuildBackendChainRejectsCloudBackends is the defense-in-depth check
// that goes with the "no third-party LLM APIs" rule in CLAUDE.md.
//
// Even if BACKEND_ORDER were misconfigured to ask for cloud backends, the
// binary must refuse to construct them — not because we filter the names
// here, but because the implementations literally do not exist in the
// package. This test pins that contract: the chain ends up empty and the
// builder errors out, regardless of how many cloud names are listed.
//
// If a future change ever silently restores a cloud backend, this test
// will start failing for the right reason and force the change to be
// debated explicitly.
func TestBuildBackendChainRejectsCloudBackends(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		BackendOrder: []string{"deepseek", "gemini", "openai"},
		HTTPTimeout:  time.Second,
	}
	ollama := newOllamaClient("http://example.invalid", "gemma2:2b", time.Second)

	_, err := buildBackendChain(cfg, ollama)
	if err == nil {
		t.Fatal("expected error when only cloud backends are requested, got nil")
	}
	if !strings.Contains(err.Error(), "no usable backends") {
		t.Errorf("err = %q, want 'no usable backends' substring", err.Error())
	}
}

func TestBuildBackendChainErrorsWhenAllUnknown(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		BackendOrder: []string{"garbage", "alsobad"},
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
