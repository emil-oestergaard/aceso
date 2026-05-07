# docs/status.md — capability matrix

> Last updated: 2026-05-07 (cloud LLM backends removed)
>
> **This file is the source of truth for what Aceso can actually do
> right now.** Do not assume a capability exists in production code
> unless the row below says `shipped` or `wired`. Update this file in
> the same commit as the capability change — never separately.

## Legend

| Status | Meaning |
|--------|---------|
| `shipped` | Implemented, tested, exercised against a real backend at least once. |
| `wired` | Implemented and unit-tested, but not yet exercised end-to-end against real Prometheus / Loki / Ollama. |
| `stubbed` | Code path exists but returns placeholder data or is gated off. |
| `planned` | Designed in the roadmap, not yet started. |
| `not started` | Acknowledged as needed, no design yet. |

## Observe loop

| Capability | Status | Notes |
|------------|--------|-------|
| Prometheus `/api/v1/alerts` polling | `wired` | `agent/prometheus.go`. Filters to `state=firing`. Not yet exercised against a live Prometheus. |
| Loki `/loki/api/v1/query_range` per alert | `wired` | `agent/loki.go`. LogQL built from the first match of `{job, instance, container, namespace, pod, service, app}`. |
| Empty-selector skip | `shipped` | If no preferred labels are present, the agent declines to query Loki rather than scraping the whole cluster. |
| Bounded per-tick deadline | `wired` | `agent/main.go:tickWithTimeout` wraps each tick in `2 × HTTPTimeout`. |
| Graceful SIGINT / SIGTERM shutdown | `wired` | Cancels the root context; the loop exits cleanly mid-tick. |

## Diagnose

| Capability | Status | Notes |
|------------|--------|-------|
| Ollama `/api/generate` request, `format=json` | `wired` | `agent/ollama.go`. `temperature=0.2` for stable output. |
| `{cause, suggested_action}` parsing | `wired` | Includes a prose-fence recovery (`recoverJSON`) for chatty small models. |
| Default model `gemma2:2b` | `wired` | Configurable via `OLLAMA_MODEL`. No A/B between models yet. |
| Prompt stability (sorted labels, deterministic ordering) | `wired` | `agent/brain.go:buildPrompt`. |
| Single-backend chain (`Backend` interface + `FallbackChain`) | `wired` | `agent/backends.go`, `agent/fallback.go`. V0 registers only `OllamaBackend`; `buildBackendChain` errors out if no usable backend remains. The `FallbackChain` shape is preserved so future revisions can add a second local path without restructuring. |
| Ollama-on-Tailscale (Pi as primary backend) | `planned` | Compose env allows `OLLAMA_URL` to point at a Tailscale IP. Validation against a real Pi is the next deploy milestone (separate plan). |

## Persist

| Capability | Status | Notes |
|------------|--------|-------|
| NDJSON append to `/data/incidents.json` | `wired` | `agent/brain.go:appendIncident`. Creates parent dir on first write. |
| Partial-failure recording (`error` field on incident) | `wired` | Loki failure still produces an incident with the LLM's metadata-only diagnosis. |
| Schema versioning | `not started` | When V1 lands a consumer, formalize in `docs/incidents-schema.md`. |

## Remediation

| Capability | Status | Notes |
|------------|--------|-------|
| Read-only HTTP guarantee | `shipped` | V0 makes only `GET` (Prometheus, Loki) and `POST` (Ollama) calls. No host writes. |
| Action proposals with HITL approval | `planned` | V1. |
| Bounded autonomous remediation for whitelisted runbooks | `planned` | V2. |

## Tooling & quality gates

| Capability | Status | Notes |
|------------|--------|-------|
| `go vet ./...` clean | `shipped` | Verified at scaffold time under `go1.26.2`. |
| `go build ./...` clean | `shipped` | Verified at scaffold time. |
| Unit tests | `wired` | 5 of 8 source files have `_test.go` (prometheus, ollama, brain.buildPrompt, backends, fallback). `config.go`, `loki.go`, `main.go`, and the rest of `brain.go` (`appendIncident`, `Tick`, `diagnoseAlert`) remain uncovered. |
| `go test -race -cover ./...` ≥ 80 % | `not started` | Below the 80 % floor. The cloud-backend tests were removed alongside the cloud backends themselves; backfilling `loki.go`, `main.go`, and the remaining `brain.go` helpers is queued. |
| CI pipeline | `not started` | Repo is local-only; no CI yet. |

### Per-file test status

| File | Tests | Notes |
|------|-------|-------|
| `agent/config.go` | `not started` | Need: missing-required-var failure, default fallbacks, bad integers. |
| `agent/prometheus.go` | `wired` | `prometheus_test.go`: happy path, empty list, non-2xx, malformed JSON, api-level error, firing-only filter (case-insensitive), transport failure. |
| `agent/loki.go` | `not started` | Need: LogQL builder priority order, empty-selector skip, timestamp parsing, sort-newest-first. |
| `agent/ollama.go` | `wired` | `ollama_test.go`: happy path, markdown-fenced recovery, malformed output, non-2xx, `done=false`, malformed envelope, transport failure, timeout, plus direct `recoverJSON` table. |
| `agent/brain.go` | `wired` (partial) | `brain_test.go` covers `buildPrompt` only (full-field, alphabetical labels, no-logs sentinel, 800→500-char truncation, optional-field omission). `appendIncident`, `Tick`, `diagnoseAlert`, partial-failure incident shape still need tests. |
| `agent/main.go` | `not started` | Need: signal-driven shutdown exits within the deadline. |
| `agent/backends.go` | `wired` | `backends_test.go`: `OllamaBackend` round-trip via `httptest.Server` confirming the wrapper is transparent. The cloud-backend tests were removed alongside the cloud backends themselves. |
| `agent/fallback.go` | `wired` | `fallback_test.go`: success on first healthy backend, fall-through on failure, all-fail returns wrapped error with every per-backend message, empty chain rejected, pre-cancelled context short-circuits, plus `buildBackendChain` default-order and unknown-name error tests. |

## Deploy

| Capability | Status | Notes |
|------------|--------|-------|
| Multi-stage Dockerfile (`golang:1.26-alpine` → `alpine:3.20`) | `shipped` | `agent/Dockerfile`. Static binary, non-root `aceso` user, `VOLUME /data`. |
| `docker-compose.yml` on external `monitoring` network | `shipped` | Named volume `aceso-data`, `restart: unless-stopped`, JSON-file log rotation. |
| Local dev stack (`docker-compose.dev.yml`) | `shipped` | Prometheus + Loki + Promtail + Ollama + Aceso on a private `aceso-dev-monitoring` bridge. Configs in `config/`. Always-firing test alert (`config/test_alert.yml`) labelled `job=aceso-self-test` so the Loki path is exercised. Verified end-to-end 2026-04-30: `AlwaysFiring` → Aceso poll → Loki query → Ollama diagnosis → NDJSON line in `/data/incidents.json`. See [`dev-stack.md`](dev-stack.md). |
| Live deploy on a real VPS | `not started` | First production deploy will populate this row. |
