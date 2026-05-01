package main

import (
	"strings"
	"testing"
)

// TestParseCSVDefault locks the edge cases that BACKEND_ORDER may receive
// from operators. None of these should panic, and none should return zero
// elements when the env var is genuinely set — the caller buildBackendChain
// handles unknown names; the parser just normalises whitespace and case.
func TestParseCSVDefault(t *testing.T) {
	const envKey = "ACESO_TEST_BACKEND_ORDER"
	fallback := []string{"ollama", "deepseek", "gemini"}

	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{
			name: "single entry no trailing comma",
			raw:  "ollama",
			want: []string{"ollama"},
		},
		{
			name: "trailing comma is ignored",
			raw:  "ollama,deepseek,gemini,",
			want: []string{"ollama", "deepseek", "gemini"},
		},
		{
			name: "whitespace and case are normalised",
			raw:  "Ollama,  DeepSeek , GEMINI",
			want: []string{"ollama", "deepseek", "gemini"},
		},
		{
			name: "unknown names are passed through (chain builder skips them)",
			raw:  "ollama,nonexistent",
			want: []string{"ollama", "nonexistent"},
		},
		{
			name: "all-empty after trim falls back to default",
			raw:  ", , ,",
			want: fallback,
		},
		{
			name: "duplicate entries are preserved verbatim",
			raw:  "ollama,ollama,deepseek",
			want: []string{"ollama", "ollama", "deepseek"},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			// Cannot t.Parallel() while mutating env in the same key across cases.
			t.Setenv(envKey, tc.raw)
			got := parseCSVDefault(envKey, fallback)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("parseCSVDefault(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

// TestParseCSVDefaultEmptyEnvUsesFallback covers the unset-env path explicitly,
// since t.Setenv cannot unset — only overwrite.
func TestParseCSVDefaultEmptyEnvUsesFallback(t *testing.T) {
	t.Parallel()

	const envKey = "ACESO_TEST_BACKEND_ORDER_UNSET"
	fallback := []string{"ollama", "deepseek", "gemini"}

	got := parseCSVDefault(envKey, fallback)
	if strings.Join(got, ",") != strings.Join(fallback, ",") {
		t.Errorf("unset env: got %v, want fallback %v", got, fallback)
	}
}
