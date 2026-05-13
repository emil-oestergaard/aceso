# docs/INDEX.md — map of topic docs

Every change to Aceso updates the matching doc in the same commit. If
no existing doc fits, add a new file here and link it from this index.

## How to use this index

- Start every task by skimming [`status.md`](status.md). It tells you
  what is real, what is stubbed, and what is deferred.
- Use this file to find the topic doc closest to the area you are
  changing. If nothing fits within ~80 % overlap, add a sibling doc
  rather than stretching an existing one.
- Each doc stays ≤ 400 lines and ≤ 3 000 words so the next agent can
  hold it fully in context.

## Current docs

| File | Purpose | Status |
|------|---------|--------|
| [`status.md`](status.md) | Living capability matrix — which alerts, models, deliverables, and remediation tiers are wired vs. stubbed vs. planned. | active |
| [`dev-stack.md`](dev-stack.md) | How to bring up the local Prometheus + Loki + Promtail + Ollama + Aceso stack via `docker-compose.dev.yml` and run the first end-to-end smoke test. | active |
| [`pi-deploy.md`](pi-deploy.md) | Operator runbook for the Pi inference plane: WireGuard tunnel, pinned-Ollama install, benchmark gate, soak, key rotation, no-rollback semantics. Pairs with `scripts/pi-setup.sh` and `scripts/cx23-setup.sh`. | active |
| [`adr/`](adr/README.md) | Architecture Decision Records — one-time write-ups for decisions that rule out a category of future implementation. See [`adr/README.md`](adr/README.md) for when to write one. | active |
| [`roadmap.md`](roadmap.md) | Milestone planning beyond V0 — V1 (HITL action proposals) and V2 (bounded autonomous remediation). Captures shipping definitions, non-features, and open ADRs per milestone. | active |
| [`incidents-schema.md`](incidents-schema.md) | Authoritative description of the `/data/incidents.json` line format. Documents the v0 shape and the planned v1 cutover (schema_version, incident_id, structured backend_errors, proposal/approval/execution fields). | active |
| [`deploy.md`](deploy.md) | Unified V0 deploy walkthrough — Pi + CX23 + GHCR pull + first-tick verification. Cross-references `pi-deploy.md` (Pi-side detail) and `dev-stack.md` (synthetic soak alert). | active |
| [`monitoring-stack.md`](monitoring-stack.md) | CX23 observability stack (Prometheus + Loki + Promtail + node-exporter) that Aceso reads from. Brought up before Aceso itself via `monitoring/docker-compose.yml`. | active |

## Planned docs

These are referenced from [`../CLAUDE.md`](../CLAUDE.md) but not yet
written. Create them when the relevant change first touches the area —
not preemptively.

| File | Trigger to create |
|------|-------------------|
| `error-handling.md` | First time partial-failure semantics need to be reasoned about across multiple call sites. |
| `dependencies.md` | First external Go dependency (must include rationale, license, last release, maintenance signal). |
| `prompting.md` | First substantive change to the prompt structure or an A/B between models. |
| `testing.md` | First time the test layout (unit / integration / fixtures) needs cross-file convention. |

## Adding a new doc

1. Create `docs/<topic>.md`.
2. Add a row to **Current docs** above with a one-line purpose.
3. Link the new doc from `../CLAUDE.md` if it codifies a rule agents must follow.
4. If the doc replaces or splits an existing one, update both rows in the same commit.
