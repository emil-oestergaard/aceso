# docs/status.md — capability matrix

> Last updated: 2026-04-29
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
| Unit tests | `not started` | **Highest-priority gap.** No `_test.go` files exist yet. CLAUDE.md rule 4 requires this; flip rows below as tests land. |
| `go test -race -cover ./...` ≥ 80 % | `not started` | Coverage floor enforced once the first tests exist. |
| CI pipeline | `not started` | Repo is local-only; no CI yet. |

### Per-file test status

| File | Tests | Notes |
|------|-------|-------|
| `agent/config.go` | `not started` | Need: missing-required-var failure, default fallbacks, bad integers. |
| `agent/prometheus.go` | `not started` | Need: `httptest.Server` covering happy path, non-2xx, malformed JSON, status≠success, firing-only filter. |
| `agent/loki.go` | `not started` | Need: LogQL builder priority order, empty-selector skip, timestamp parsing, sort-newest-first. |
| `agent/ollama.go` | `not started` | Need: `httptest.Server` happy path, prose-fence recovery, `done=false` rejection, timeout. |
| `agent/brain.go` | `not started` | Need: prompt determinism, NDJSON append integrity, partial-failure incident shape, end-to-end loop with fakes. |
| `agent/main.go` | `not started` | Need: signal-driven shutdown exits within the deadline. |

## Deploy

| Capability | Status | Notes |
|------------|--------|-------|
| Multi-stage Dockerfile (`golang:1.26-alpine` → `alpine:3.20`) | `shipped` | `agent/Dockerfile`. Static binary, non-root `aceso` user, `VOLUME /data`. |
| `docker-compose.yml` on external `monitoring` network | `shipped` | Named volume `aceso-data`, `restart: unless-stopped`, JSON-file log rotation. |
| Live deploy on a real VPS | `not started` | First production deploy will populate this row. |
