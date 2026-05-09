package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestComposeBackendOrderDefaultsAreUsable closes a regression class
// surfaced by commit e8451a3: the compose-file BACKEND_ORDER defaults
// drifted out of sync with what the binary accepts after fab6b3c
// deleted the cloud backends. The agent still booted (buildBackendChain
// skips unknown names rather than refusing the whole chain), but every
// startup logged "skipping unknown backend" twice and the
// DEEPSEEK_API_KEY/GEMINI_API_KEY env passthroughs were misleading.
//
// The defense-in-depth test in fallback_test.go
// (TestBuildBackendChainRejectsCloudBackends) verifies the *binary*
// refuses to construct cloud-shaped chains. It does not catch the
// adjacent failure mode where the *deployment surface* ships with
// names the binary cannot use.
//
// This test parses each committed compose file, extracts the
// BACKEND_ORDER default, and asserts every entry resolves through
// buildBackendChain without being skipped. If a future PR adds a
// cloud-shaped name to either compose default — or removes a backend
// from the binary without updating compose — this test fails
// immediately.
//
// Stdlib-only on purpose (CLAUDE.md rule 7): the regex captures the
// `${BACKEND_ORDER:-<default>}` shape directly from the YAML bytes
// rather than pulling in a YAML parser.
func TestComposeBackendOrderDefaultsAreUsable(t *testing.T) {
	composeFiles := []string{
		"../docker-compose.yml",
		"../docker-compose.dev.yml",
	}

	pattern := regexp.MustCompile(`BACKEND_ORDER:\s*\$\{BACKEND_ORDER:-([^}]+)\}`)

	for _, rel := range composeFiles {
		rel := rel
		t.Run(filepath.Base(rel), func(t *testing.T) {
			t.Parallel()

			raw, err := os.ReadFile(rel)
			if err != nil {
				t.Fatalf("read %s: %v", rel, err)
			}
			match := pattern.FindStringSubmatch(string(raw))
			if match == nil {
				t.Fatalf("no `BACKEND_ORDER: ${BACKEND_ORDER:-...}` default found in %s — "+
					"the test's regex assumes the compose env block uses the standard "+
					"docker-compose default-value syntax", rel)
			}
			defaultExpr := strings.TrimSpace(match[1])

			// Mirror config.go's parseCSVDefault: split on commas, lowercase,
			// trim whitespace, drop empties. Keeps the test honest about what
			// the binary actually sees at startup.
			var names []string
			for _, raw := range strings.Split(defaultExpr, ",") {
				if name := strings.ToLower(strings.TrimSpace(raw)); name != "" {
					names = append(names, name)
				}
			}
			if len(names) == 0 {
				t.Fatalf("BACKEND_ORDER default in %s parsed to an empty list", rel)
			}

			cfg := &Config{
				BackendOrder: names,
				HTTPTimeout:  time.Second,
			}
			ollama := newOllamaClient("http://example.invalid", "gemma2:2b", time.Second)

			chain, err := buildBackendChain(cfg, ollama)
			if err != nil {
				t.Fatalf("buildBackendChain(%v) returned error %q — "+
					"compose default contains no usable backend names", names, err)
			}
			if got, want := len(chain.backends), len(names); got != want {
				t.Errorf("chain has %d backend(s), want %d — "+
					"some compose default entries were silently skipped (parsed: %v); "+
					"either remove the unrecognized names from the compose file or "+
					"add support for them in agent/backends.go", got, want, names)
			}
		})
	}
}
