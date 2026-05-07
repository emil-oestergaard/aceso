# docs/status.md — capability matrix

> Last updated: 2026-05-07 (cloud fallbacks removed, human-escalation layer added)
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
| Local-only `Backend` chain (`Backend` interface + `FallbackChain`) | `wired` | `agent/backends.go`, `agent/fallback.go`. V0 only registers `OllamaBackend`; the `buildBackendChain` switch rejects all unknown names (including `deepseek`/`gemini`/`openai`) so a misconfigured `BACKEND_ORDER` cannot resurrect cloud paths — they are not in the binary. See CLAUDE.md rule 11. |
| Ollama-on-Tailscale (Pi as primary backend) | `planned` | Compose env allows `OLLAMA_URL` to point at a Tailscale IP. Validation against a real Pi is the next deploy milestone (separate plan). |

## Escalation

When every backend in the chain fails, Aceso surfaces the alert to a
human instead of inventing a diagnosis. This is the V0 "human-in-the-
loop" layer that V1's approval UI will eventually formalize.

### V0 escalation contract (deliberate decisions)

- **No deduplication, no rate limiting.** If 50 alerts fire while
  Ollama is down, you get 50 escalations. Rationale: V0 prefers loud
  failure over silent suppression; per-tick HTTP timeouts already
  bound the throughput in practice. Dedup ships with V1 alongside
  the approval UI.
- **ntfy body is metadata-only.** Alert name, severity, state, and
  "check aceso logs". The wrapped backend error can transitively
  include truncated model output (`agent/ollama.go:121`), which is
  downstream of production log content; ntfy.sh is a third party
  even when self-hosted, so the body must not carry it.
- **Local `[escalate]` log line is verbose.** Single-line, key=value,
  includes the full wrapped error so the operator can debug from
  Promtail/Loki without opening a separate tool. Assumes the log
  shipper itself is local — revisit if you ever ship logs off-prem.
- **Incident persistence is unconditional.** A failed ntfy push does
  not stop the on-disk record from being written. The log line is
  the floor; ntfy is best-effort; the NDJSON line is the durable
  audit trail.

| Capability | Status | Notes |
|------------|--------|-------|
| Structured `[escalate]` log line | `wired` | `agent/escalate.go:NtfyEscalator.Escalate`. Single-line, key=value, parseable by LogQL / awk. Always emitted regardless of ntfy config. |
| ntfy.sh push | `wired` | `agent/escalate.go:NtfyEscalator.postNtfy`. Enabled when `ESCALATE_NTFY_URL` is set. Headers carry title/priority/tags; body is the alert summary plus a truncated backend error. Best-effort: ntfy delivery failure does not mask the original problem. |
| Escalated incident persistence (`"escalated": true`) | `wired` | `agent/brain.go:escalateAlert`. The on-disk record carries `Escalated: true`, a zero-valued `Diagnosis`, and a combined error string preserving both Loki partial-failures and the backend error. |
| Live ntfy validation | `not started` | First production deploy will populate this row. |

## Persist

| Capability | Status | Notes |
|------------|--------|-------|
| NDJSON append to `/data/incidents.json` | `wired` | `agent/brain.go:appendIncident`. Creates parent dir on first write. |
| Partial-failure recording (`error` field on incident) | `wired` | Loki failure still produces an incident with the LLM's metadata-only diagnosis. |
| Escalation field (`escalated: true`) | `wired` | Additive change to the incident schema. Success-path lines remain byte-identical (`omitempty`); escalated lines branch on this flag rather than empty-string-checking the diagnosis. |
| Schema versioning | `not started` | When V1 lands a consumer, formalize in `docs/incidents-schema.md`. The `escalated` field's introduction is the first non-trivial schema evolution worth tracking there. |
| `incident.error` as unstructured string | `wired` (with deferred-decision annotation) | Today the field is a single concatenated string ("loki: …; backend: fallback: all 1 backend(s) failed: ollama: connection refused"). It preserves enough signal to grep "Pi was down" vs. "Pi returned garbage" vs. "Pi timed out", but it is **not** structured. **Do not add more unstructured failure metadata to incidents.** When V1's review UI needs to render per-backend error history, migrate this field to a structured `backend_errors: [{name, error, at}]` array as the first formal schema-version bump. |

## Remediation

| Capability | Status | Notes |
|------------|--------|-------|
| Read-only HTTP guarantee | `shipped` | V0's full egress surface is: `GET` (Prometheus, Loki); `POST` to local Ollama; optional `POST` to the configured ntfy.sh topic for escalations. No host writes, no third-party LLM APIs. |
| Action proposals with HITL approval | `planned` | V1. |
| Bounded autonomous remediation for whitelisted runbooks | `planned` | V2. |

## Tooling & quality gates

| Capability | Status | Notes |
|------------|--------|-------|
| `go vet ./...` clean | `shipped` | Verified at scaffold time under `go1.26.2`. |
| `go build ./...` clean | `shipped` | Verified at scaffold time. |
| Unit tests | `wired` | 7 of 9 source files have `_test.go` (prometheus, ollama, brain.buildPrompt + brain.diagnoseAlert escalation path, backends, fallback, config, escalate). `loki.go`, `main.go`, and the rest of `brain.go` (`appendIncident`, `Tick`) remain uncovered. |
| `go test -race -cover ./...` ≥ 80 % | `not started` | Currently 59.6 % package-level (up from 52.2 %). Below the 80 % floor. **The gap is "not yet written," not "hard to test":** `loki.go` is structurally identical to the already-tested `prometheus.go`; `appendIncident` and `Tick` are straightforward with `t.TempDir()` and fakes; only `main.go`'s signal-driven shutdown needs careful goroutine choreography. Backfill is queued behind the local-only architectural change that just landed. |
| CI pipeline | `not started` | Repo is local-only; no CI yet. |

### Per-file test status

| File | Tests | Notes |
|------|-------|-------|
| `agent/config.go` | `wired` (partial) | `config_test.go`: `parseCSVDefault` table covers single entry, trailing comma, whitespace/case normalisation, unknown names pass-through, all-empty fallback, duplicates, unset env. Still need tests for `loadConfig` missing-required-var failure and `parseSecondsDefault` edge cases. |
| `agent/prometheus.go` | `wired` | `prometheus_test.go`: happy path, empty list, non-2xx, malformed JSON, api-level error, firing-only filter (case-insensitive), transport failure. |
| `agent/loki.go` | `not started` | Need: LogQL builder priority order, empty-selector skip, timestamp parsing, sort-newest-first. |
| `agent/ollama.go` | `wired` | `ollama_test.go`: happy path, markdown-fenced recovery, malformed output, non-2xx, `done=false`, malformed envelope, transport failure, timeout, plus direct `recoverJSON` table. |
| `agent/brain.go` | `wired` (partial) | `brain_test.go` covers `buildPrompt` (full-field, alphabetical labels, no-logs sentinel, 800→500-char truncation, optional-field omission) and the escalation path of `diagnoseAlert` (escalator called once with the original error, persisted incident has `Escalated:true` and a zero-valued `Diagnosis`). `appendIncident` directly + `Tick` still need tests. |
| `agent/main.go` | `not started` | Need: signal-driven shutdown exits within the deadline. |
| `agent/backends.go` | `wired` | `backends_test.go`: `OllamaBackend` round-trip via `httptest.Server` confirming the wrapper is transparent. The cloud-backend tests were removed alongside the cloud backends themselves. |
| `agent/fallback.go` | `wired` | `fallback_test.go`: success on first healthy backend, fall-through on failure, all-fail returns wrapped error with every per-backend message, empty chain rejected, pre-cancelled context short-circuits, `buildBackendChain` default order, **rejects cloud backends** (defense-in-depth), errors when only unknown names are supplied. |
| `agent/escalate.go` | `wired` | `escalate_test.go`: empty-URL log-only path (no HTTP), full POST with body + `Title`/`Priority`/`Tags` headers verified against `httptest.Server`, non-2xx surfaced, transport failure surfaced. |

## Deploy

| Capability | Status | Notes |
|------------|--------|-------|
| Multi-stage Dockerfile (`golang:1.26-alpine` → `alpine:3.20`) | `shipped` | `agent/Dockerfile`. Static binary, non-root `aceso` user, `VOLUME /data`. |
| `docker-compose.yml` on external `monitoring` network | `shipped` | Named volume `aceso-data`, `restart: unless-stopped`, JSON-file log rotation. |
| Local dev stack (`docker-compose.dev.yml`) | `shipped` | Prometheus + Loki + Promtail + Ollama + Aceso on a private `aceso-dev-monitoring` bridge. Configs in `config/`. Always-firing test alert (`config/test_alert.yml`) labelled `job=aceso-self-test` so the Loki path is exercised. Verified end-to-end 2026-04-30: `AlwaysFiring` → Aceso poll → Loki query → Ollama diagnosis → NDJSON line in `/data/incidents.json`. See [`dev-stack.md`](dev-stack.md). |
| Live deploy on a real VPS | `not started` | First production deploy will populate this row. |
