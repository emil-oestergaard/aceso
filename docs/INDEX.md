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

## Planned docs

These are referenced from [`../CLAUDE.md`](../CLAUDE.md) but not yet
written. Create them when the relevant change first touches the area —
not preemptively.

| File | Trigger to create |
|------|-------------------|
| `incidents-schema.md` | First non-additive change to the `/data/incidents.json` line format, or first external consumer. |
| `error-handling.md` | First time partial-failure semantics need to be reasoned about across multiple call sites. |
| `dependencies.md` | First external Go dependency (must include rationale, license, last release, maintenance signal). |
| `prompting.md` | First substantive change to the prompt structure or an A/B between models. |
| `testing.md` | First time the test layout (unit / integration / fixtures) needs cross-file convention. |
| `deploy.md` | First non-default deployment topology (multi-VPS, alternative networks, secrets backends). |

## Adding a new doc

1. Create `docs/<topic>.md`.
2. Add a row to **Current docs** above with a one-line purpose.
3. Link the new doc from `../CLAUDE.md` if it codifies a rule agents must follow.
4. If the doc replaces or splits an existing one, update both rows in the same commit.
