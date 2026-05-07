# docs/status.md — capability matrix

> Last updated: 2026-05-07 (Pi inference plane scripts + deploy runbook landed)
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
| Ollama-on-WireGuard (Pi as primary backend) | `wired` | Tailscale was rejected for V0 (third-party trust path conflicts with rule 11). Plain WireGuard + pinned Ollama install scripts are committed under `scripts/`; see [`pi-deploy.md`](pi-deploy.md). The agent uses the existing `OllamaBackend` with `OLLAMA_URL` pointed at the Pi's tunnel IP — no new backend type. Awaiting first-deploy + 1-week soak before flipping to `shipped`. |

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

## Pi inference plane

| Capability | Status | Notes |
|------------|--------|-------|
| WireGuard tunnel scripts (`scripts/pi-setup.sh`, `scripts/cx23-setup.sh`) | `wired` | Plain WG, no Tailscale. Operator-driven key generation; conf files gitignored; templates committed. UDP 51820 open from anywhere on the CX23 (WG auth is cryptographic). See [`pi-deploy.md`](pi-deploy.md). |
| Pi base hardening | `wired` | `pi-setup.sh` Phase 1: ufw default-deny, key-only SSH (with lockout-protection precondition: refuses to disable password auth unless at least one user has a non-empty `authorized_keys`), unattended-upgrades for the security pocket only, unprivileged `aceso` service user. fail2ban deliberately omitted — with password auth disabled there is nothing to brute-force. |
| Ollama install (pinned binary, SHA256-verified) | `wired` | `pi-setup.sh` Phase 3: downloads `ollama-linux-arm64` from the GitHub release tagged `v${OLLAMA_VERSION}`, verifies against operator-provided `OLLAMA_SHA256` (no default — script aborts if unset), installs `scripts/templates/ollama.service` with hardening directives, binds Ollama to the WG IP only. |
| Model pre-pull + benchmark gate | `wired` | `pi-setup.sh` Phase 3b: 3 sequential diagnose-shaped prompts; first run discarded (cold load); both warm runs must complete in ≤60s. Failure: operator switches `OLLAMA_MODEL` to `qwen2.5:3b-instruct-q4_K_M` and re-runs. Default is `qwen2.5:7b-instruct-q4_K_M`. |
| Cross-tunnel smoke test | `wired` | `cx23-setup.sh` ends with `POST /api/generate` over the tunnel and asserts the response decodes to `{cause, suggested_action}`. Same prompt shape the agent uses, so a green smoke run is a strong predictor for Phase 4. |
| Pi-ready receipt (`/etc/aceso/pi-ready`) | `wired` | Stamped at end of `pi-setup.sh` with `ready_at`, `ollama_version`, `ollama_model`, `warm_max_seconds`, `kernel`. Human-readable; not consumed by the agent. |
| First production deploy | `not started` | Phase 4 in `pi-deploy.md`. Flip is a 5-minute `.env` edit + `docker compose restart`. |
| 1-week soak before prod flip | `not started` | Per Phase 5 in `pi-deploy.md`: synthetic alerts via dev stack, watch for memory leaks / tunnel staleness / model drift / SD-card write pressure. 24h is not enough — slow leaks need time. |

## V0 out-of-scope (deliberate deferrals — record only, do not build)

These are known concerns the V0 deploy plan explicitly does *not*
address. Documented here so future-us doesn't waste time rediscovering
them, and so V1 planning has a clear backlog to draw from.

| Concern | Why deferred | Trigger to revisit |
|---------|-------------|--------------------|
| Pi-side `node_exporter` / metrics about the Pi itself | The Pi's metrics would have to flow back to the CX23's Prometheus over the same WG tunnel — fine, but adds another systemd unit, another scrape config, and another failure mode to monitor. V0 relies on `journalctl -u ollama` + the Pi-ready receipt for soak-time observability. | First time an outage's root cause would have been visible only through Pi-side metrics. |
| Model-registry trust (ollama.com) | `ollama pull` fetches model weights from `https://registry.ollama.ai`. That registry's signing/integrity story is opaque from our end — we trust ollama.com the same way `apt-get` trusts Debian's keyring, on TLS + reputation. The pinned binary install closes the *binary* supply-chain hole; the model layer is one level deeper and unaddressed. | First time a model-weight integrity issue is publicly disclosed, or first time we want to ship our own fine-tune (which would put weights under operator control). |
| Escalation rate limiting / dedup | Already flagged in the V0 escalation contract as "loud failure is good failure". The Pi makes this more pressing: with the Pi as the *sole* inference path and no localhost fallback, a Pi outage produces N escalations per tick × ticks-until-fixed. ntfy.sh has its own per-topic rate limits, so the operator gets paged correctly even if the volume is high — but `incidents.json` will accumulate one `escalated:true` line per failed tick, which a V1 review UI will need to coalesce. The "no startup health check" decision in `agent/main.go` is consistent with this stance: fail loudly on the first real tick rather than fail more loudly with a redundant pre-tick probe. | First V1 review-UI work, or first real-world incident where the escalation volume itself becomes a problem. |

## Deploy

| Capability | Status | Notes |
|------------|--------|-------|
| Multi-stage Dockerfile (`golang:1.26-alpine` → `alpine:3.20`) | `shipped` | `agent/Dockerfile`. Static binary, non-root `aceso` user, `VOLUME /data`. |
| `docker-compose.yml` on external `monitoring` network | `shipped` | Named volume `aceso-data`, `restart: unless-stopped`, JSON-file log rotation. |
| Local dev stack (`docker-compose.dev.yml`) | `shipped` | Prometheus + Loki + Promtail + Ollama + Aceso on a private `aceso-dev-monitoring` bridge. Configs in `config/`. Always-firing test alert (`config/test_alert.yml`) labelled `job=aceso-self-test` so the Loki path is exercised. Verified end-to-end 2026-04-30: `AlwaysFiring` → Aceso poll → Loki query → Ollama diagnosis → NDJSON line in `/data/incidents.json`. See [`dev-stack.md`](dev-stack.md). |
| Live deploy on a real VPS | `not started` | First production deploy will populate this row. |
