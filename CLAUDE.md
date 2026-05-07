# CLAUDE.md — repo guide for AI agents

**What this repo is.** `aceso` is a self-healing AI agent for VPS
observability, written in Go. It polls Prometheus for firing alerts,
queries Loki for the logs from each affected target, and asks a local
Ollama model to produce a `{cause, suggested_action}` diagnosis. Every
incident is appended as NDJSON to `/data/incidents.json`. **V0 observes
and diagnoses only** — no writes against the host or any service.
Roadmap: V1 adds human-in-the-loop action proposals, V2 adds bounded
autonomous remediation for whitelisted runbooks. The agent is stateless
except for the incident log; the binary is stdlib-only Go shipped in a
multi-stage Docker image.

## First thing to read

Always open [`docs/status.md`](docs/status.md) first. It is the living
matrix of which capabilities are wired end-to-end, which are stubbed,
and which are deferred — which alerts the agent has been tested
against, which Loki label sets are queryable in practice, which Ollama
models have produced reliable diagnoses, and which roadmap milestones
have actually shipped. **Do not assume a capability exists in
production code unless this file says so.** If `docs/status.md` does
not yet exist for the change you are about to make, your first commit
creates it.

Then [`docs/INDEX.md`](docs/INDEX.md) for the full map of topic docs.

## Where code lives

- `agent/main.go` — entrypoint, signal handling, polling ticker
- `agent/config.go` — env-driven configuration loader
- `agent/prometheus.go` — client for `/api/v1/alerts`, firing-state filter
- `agent/loki.go` — client for `/loki/api/v1/query_range`, LogQL built from alert labels
- `agent/ollama.go` — client for `/api/generate`, JSON-output parser with prose-fence recovery
- `agent/backends.go` — `Backend` interface + `OllamaBackend` + `buildBackendChain` resolver. **Local-only**: no third-party LLM API code paths exist in the binary.
- `agent/fallback.go` — `FallbackChain` that tries each backend in order and returns the first success or a wrapped error if all fail
- `agent/escalate.go` — `Escalator` that surfaces backend-chain failures to a human via a structured log line and (optionally) an ntfy.sh push
- `agent/brain.go` — orchestrator: prompt construction + NDJSON incident persistence + escalation routing on chain failure
- `agent/go.mod` — module manifest, toolchain pin (`go1.26.2`)
- `agent/Dockerfile` — multi-stage build, static binary, non-root runtime
- `docker-compose.yml` — `aceso` service, named volume for `/data`, external `monitoring` network
- `scripts/` — Pi inference plane deployment: `pi-setup.sh` (hardening + WG + pinned Ollama + benchmark gate), `cx23-setup.sh` (CX23 WG side + cross-tunnel smoke), `templates/` (WG conf templates, ollama.service, conf examples). Operator runbook in [`docs/pi-deploy.md`](docs/pi-deploy.md).
- `docs/` — topic docs, sized for AI context (see rules below)

## Rules for agents working here

1. **Update the docs in the same change.** When you touch a polling
   cadence, env var, label-selection heuristic, prompt structure,
   incident schema, deliverable, or deploy topology, update the matching
   file under `docs/` in the *same commit*. Start from `docs/INDEX.md`
   to find the right one. If none fits, add a new file and link it from
   `INDEX.md`. **A change without a doc update is unfinished work.**

2. **Flip `docs/status.md`** when a capability moves between
   stub / wired / deferred — when a new alert is supported, a model is
   validated, a remediation moves from V1 plan to V1 ship, **or when
   tests are added or removed for any source file**. Same commit ships
   the wiring (or the tests) and updates the row. Adding tests without
   flipping the per-file test row in `docs/status.md` is unfinished
   work, even if no production code changed.

3. **Keep every `docs/*.md` ≤ 400 lines and ≤ 3 000 words.** If a doc
   grows past the limit, split it into a sibling file rather than
   inflating one. Docs are sized so the next agent can hold the relevant
   one fully in context.

4. **Tests land with code. No exceptions.** Every new function with
   non-trivial behavior ships with a unit test. Every external API
   client (`prometheus.go`, `loki.go`, `ollama.go`) has table-driven
   tests against `httptest.Server` fixtures covering happy path,
   non-2xx, malformed JSON, and timeout. Every prompt builder and
   persistence helper has tests asserting deterministic output. The
   orchestrator (`brain.go`) has integration tests exercising the full
   alert → logs → prompt → diagnosis → persist path against fakes.
   When tests land — either alongside new code or as a backfill task —
   the matching per-file row in `docs/status.md` flips in the same
   commit (see rule 2). No commit that adds or removes a `_test.go`
   file is complete without a matching `status.md` diff.

5. **Coverage floor: 80 %. Race detector mandatory.** PRs must pass
   `go test -race -cover ./...`. The agent runs ticks under context
   deadlines and may grow concurrency over time; a data race here means
   a self-healing system that silently corrupts its own incident log.

6. **Test the *important* components, not every line.** Configuration
   loading, label-selector construction, JSON envelope parsing,
   prompt-text stability, NDJSON append safety, partial-failure
   recording, and the polling loop's bounded-deadline semantics are all
   load-bearing. Trivial getters are not. When in doubt, test it.

7. **Stdlib first.** External dependencies require a written
   justification in `docs/dependencies.md` (rationale, license, last
   release, maintenance signal). The stdlib-only baseline is a feature,
   not an accident — it keeps the binary small, the audit surface tiny,
   and the supply chain trivial to reason about.

8. **Graceful degradation is non-negotiable.** Loki down ≠ skip the
   alert. Ollama timeout ≠ crash. Every external call has explicit
   error handling, and every partial failure is recorded on the
   incident with an `error` field so the history shows *what* the agent
   could and couldn't see at decision time. See
   [`docs/error-handling.md`](docs/error-handling.md).

9. **V0 is read-only.** The agent must not execute writes against the
   host or any monitored service. HTTP `GET` only against Prometheus
   and Loki; `POST` only to Ollama. Any change that introduces a write
   path is an architectural change and ships behind explicit
   human-in-the-loop approval. See [`docs/roadmap.md`](docs/roadmap.md).

10. **The incident schema is a contract.** `/data/incidents.json` will
    be consumed by future tooling (dashboards, the V1 approval UI,
    post-incident review). Schema changes are versioned and documented
    in [`docs/incidents-schema.md`](docs/incidents-schema.md). Breaking
    changes ship a migration note in the same commit.

11. **Inference is local-only. No exceptions.** The only LLM backend
    Aceso talks to is a local Ollama instance — typically a Tailscale-
    reachable Raspberry Pi in production, or a local container in dev.
    Third-party LLM APIs (DeepSeek, Gemini, OpenAI, Anthropic, etc.)
    are out of scope and the binary contains no code paths to them.
    This is not a configuration toggle: defense in depth means flags
    rot and config drifts, so the *implementations themselves do not
    exist in the package*. If a future PR proposes adding a cloud
    backend "for reliability", reject it and propose extending the
    escalation layer instead. Production logs sent through the prompt
    can contain hostnames, user IDs, request paths, and stack traces;
    that data must not leave the operator's infrastructure. When the
    chain fails, Aceso escalates to a human (see `agent/escalate.go`
    and the `escalated: true` incident shape) — it does NOT silently
    route around the outage.

## Running

See [`README.md`](README.md) for the shortest path. The short version:

```bash
# One-time: create the shared monitoring network
docker network create monitoring

# Build and start Aceso
docker compose up --build -d

# Tail diagnoses
docker compose logs -f aceso

# Inspect persisted incidents
docker compose exec aceso cat /data/incidents.json
```

Local development without Docker:

```bash
cd agent
export PROMETHEUS_URL=http://localhost:9090
export LOKI_URL=http://localhost:3100
export OLLAMA_URL=http://localhost:11434
go run .
```

Run the test suite (must pass before any commit):

```bash
cd agent
go test -race -cover ./...
```
