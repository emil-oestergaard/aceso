# CLAUDE.md — repo guide for AI agents

**aceso** is a stdlib-only Go self-healing agent for VPS observability: it
polls Prometheus for firing alerts, pulls the matching Loki logs, and asks a
local Ollama model for a `{cause, suggested_action}` diagnosis, appending each
incident to `/data/incidents.json`. **V0 observes and diagnoses only — no
writes.** See [`README.md`](README.md) for the full overview and roadmap.

## Start here

Open [`docs/status.md`](docs/status.md) **first** — the living matrix of which
capabilities are wired, stubbed, or deferred. **Do not assume a capability
exists in production unless this file says so.** If it does not exist yet for
the change you are making, your first commit creates it. Then
[`docs/INDEX.md`](docs/INDEX.md) maps every topic doc.

## Verify your work

Give yourself a feedback loop on every change. All commands run from `agent/`:

```bash
cd agent
go build ./...                 # compiles
go vet ./...                   # CI gate 1
go test -race -cover ./...     # CI gate 2 — race mandatory, 80% coverage floor
gofmt -l .                     # must print nothing
```

CI (`.github/workflows/build.yml`) runs `go vet` then
`go test -race -cover -count=1 ./...`; both must pass before any commit.

Run the agent locally without Docker:

```bash
cd agent
export PROMETHEUS_URL=http://localhost:9090 LOKI_URL=http://localhost:3100 OLLAMA_URL=http://localhost:11434
go run .
```

Docker quickstart and the full env-var reference live in [`README.md`](README.md).

## Where code lives

- `agent/main.go` — entrypoint, signal handling, polling ticker
- `agent/config.go` — env-driven configuration loader
- `agent/prometheus.go` — `/api/v1/alerts` client, firing-state filter
- `agent/loki.go` — `/loki/api/v1/query_range` client, LogQL from alert labels
- `agent/ollama.go` — `/api/generate` client, JSON parser with prose-fence recovery
- `agent/backends.go` — `Backend` interface, `OllamaBackend`, `buildBackendChain`. Local-only — no third-party LLM code paths exist.
- `agent/fallback.go` — `FallbackChain`: first success, or a wrapped error if all fail
- `agent/escalate.go` — `Escalator`: surfaces chain failure to a human (log line + optional ntfy.sh push)
- `agent/brain.go` — orchestrator: prompt construction, NDJSON persistence, escalation routing
- `agent/go.mod` — module manifest, toolchain pin (`go1.26.2`)
- `agent/Dockerfile` — multi-stage static build, non-root runtime
- `docker-compose.yml` — `aceso` service, `/data` volume, external `monitoring` network
- `monitoring/` — CX23 observability stack (Prometheus + Loki + Promtail + node-exporter)
- `scripts/` — Pi inference-plane deploy (`pi-setup.sh`, `cx23-setup.sh`, `templates/`)

## For deeper context, read the matching doc

Each `docs/` file is sized to hold fully in context. Before changing an area,
read its doc:

| Topic | Doc |
|-------|-----|
| What is wired vs. stubbed vs. deferred | [`docs/status.md`](docs/status.md) |
| Full topic-doc map | [`docs/INDEX.md`](docs/INDEX.md) |
| V1/V2 milestones, non-features | [`docs/roadmap.md`](docs/roadmap.md) |
| `/data/incidents.json` line format | [`docs/incidents-schema.md`](docs/incidents-schema.md) |
| Local dev stack + first smoke test | [`docs/dev-stack.md`](docs/dev-stack.md) |
| CX23 observability stack | [`docs/monitoring-stack.md`](docs/monitoring-stack.md) |
| V0 production deploy walkthrough | [`docs/deploy.md`](docs/deploy.md) |
| Pi inference-plane runbook | [`docs/pi-deploy.md`](docs/pi-deploy.md) |
| Why local-only / human-escalation / WireGuard | [`docs/adr/`](docs/adr/README.md) |

`docs/error-handling.md` and `docs/dependencies.md` are planned (see
`INDEX.md`) — create them when rules 8/9 first apply.

## Rules for agents working here

1. **Docs land with code.** Touch a polling cadence, env var, label heuristic,
   prompt, incident schema, deliverable, or deploy topology → update the
   matching `docs/` file in the *same commit*. A change without a doc update is
   unfinished work.

2. **Flip `docs/status.md`** whenever a capability moves between
   stub / wired / deferred — a new alert is supported, a model is validated, a
   remediation ships, *or tests are added/removed for any file*. The same
   commit ships the wiring (or the tests) and updates the row. No `_test.go`
   change is complete without a matching `status.md` diff.

3. **Keep every `docs/*.md` ≤ 400 lines and ≤ 3000 words.** Past the limit,
   split into a sibling file — never inflate one. Docs are sized so the next
   agent can hold the relevant one fully in context.

4. **Tests land with code. No exceptions.** Every non-trivial function ships a
   unit test. Every external API client (`prometheus.go`, `loki.go`,
   `ollama.go`) has table-driven `httptest.Server` tests covering happy path,
   non-2xx, malformed JSON, and timeout. Prompt builders and persistence
   helpers have deterministic-output tests. `brain.go` has integration tests
   over the full alert → logs → prompt → diagnosis → persist path against fakes.

5. **Coverage floor 80%. Race detector mandatory.** `go test -race -cover ./...`
   must pass. The agent runs ticks under context deadlines — a data race here
   silently corrupts its own incident log.

6. **Test the important components, not every line.** Config loading,
   label-selector construction, JSON-envelope parsing, prompt-text stability,
   NDJSON append safety, partial-failure recording, and bounded-deadline
   polling are load-bearing. Trivial getters are not. When in doubt, test it.

7. **Don't volunteer scope.** Land the requested work and stop. Surface
   follow-ons as backlog items in `docs/status.md` ("Documentation debt" / "V0
   out-of-scope") or as a question — don't offer to execute them now.
   Architectural shifts (deploy topology, a new trust surface) are never a
   "while we're here" — they need their own ADR-level session.

8. **Stdlib first.** External dependencies require written justification in
   `docs/dependencies.md` (rationale, license, last release, maintenance
   signal) — create that doc on the first dependency. The stdlib-only baseline
   keeps the binary small and the supply chain trivial to audit.

9. **Graceful degradation is non-negotiable.** Loki down ≠ skip the alert.
   Ollama timeout ≠ crash. Every external call has explicit error handling, and
   every partial failure is recorded on the incident with an `error` field so
   the history shows what the agent could and couldn't see at decision time.

10. **V0 is read-only.** No writes against the host or any monitored service.
    HTTP `GET` only to Prometheus and Loki; `POST` only to Ollama. Any write
    path is an architectural change behind explicit human-in-the-loop approval
    (see [`docs/roadmap.md`](docs/roadmap.md)).

11. **The incident schema is a contract.** `/data/incidents.json` feeds future
    tooling (dashboards, the V1 approval UI, post-incident review). Schema
    changes are versioned in [`docs/incidents-schema.md`](docs/incidents-schema.md);
    breaking changes ship a migration note in the same commit.

12. **Inference is local-only. No exceptions.** The only LLM backend is a local
    Ollama instance (a Raspberry Pi over plain WireGuard in prod, a container
    in dev). Third-party LLM APIs (DeepSeek, Gemini, OpenAI, Anthropic, …) are
    out of scope — the implementations *do not exist in the package*, by
    design, so flags can't rot and config can't drift. Production logs in the
    prompt carry hostnames, user IDs, request paths, and stack traces; that
    data must not leave the operator's infrastructure. On chain failure Aceso
    escalates to a human (`escalate.go`, `escalated: true`) — it never silently
    routes around the outage. Rationale:
    [`adr/001`](docs/adr/001-local-only-inference.md),
    [`adr/002`](docs/adr/002-human-escalation-over-cloud-fallback.md),
    [`adr/003`](docs/adr/003-plain-wireguard-over-tailscale.md).

## Keep this file tuned

CLAUDE.md is a living prompt, not documentation. When an agent gets something
wrong, add a terse line to the right section so it doesn't recur — and keep it
lean: short rules beat long prose.
