# docs/roadmap.md — milestones beyond V0

> Last updated: 2026-05-09
>
> This file is **planning, not commitment**. The status matrix in
> [`status.md`](status.md) is the source of truth for what exists in
> the binary today. The roadmap describes what we want next, in what
> order, and — most importantly — what we are deliberately *not*
> building so the scope of each milestone stays small enough to ship.

## Versioning posture

V0 → V1 → V2 are coarse milestones, not semver. Each milestone has:

- A **shipping definition** (what proves it's done).
- A **feature list** (the work).
- A **non-feature list** (what we're refusing to do at this tier — the
  load-bearing part of the doc).
- **Open design questions** that must resolve before the milestone
  starts implementation.

Milestones land sequentially. We do not start V1 implementation until
V0's soak window has produced real-world signal; we do not start V2
implementation until V1 has been operated for long enough that the
*right* set of runbook candidates is obvious from incident history.

## V0 — Observe & Diagnose (current)

**Status:** capability complete, soak pending.

V0 polls Prometheus for firing alerts, fetches per-alert logs from
Loki, asks a local Ollama instance for a `{cause, suggested_action}`
diagnosis, and writes one NDJSON line per incident to
`/data/incidents.json`. Cloud LLM backends do not exist in the binary
([ADR-0001](adr/0001-local-only-inference.md)). Inference-path failures
escalate to a human ([ADR-0002](adr/0002-human-escalation-over-cloud-fallback.md)),
not a third-party model. Production topology is one CX23 + one Pi
joined by a plain WireGuard tunnel
([ADR-0003](adr/0003-plain-wireguard-over-tailscale.md)).

### V0 shipping definition

- [ ] Pi inference plane deployed end-to-end
      (Phases 0–4 in [`pi-deploy.md`](pi-deploy.md)).
- [ ] One-week soak with real CX23 alerts running through the Pi —
      no memory leaks, no tunnel staleness, no SD-card write
      pressure, model output remains parseable across the window.
- [ ] First production `escalated:true` incident exercised
      end-to-end (kill the Pi, watch the structured log line + ntfy
      push land, confirm `incidents.json` records the failure).
- [ ] Test coverage at the 80 % floor (`go test -race -cover` —
      currently 59.6 %; backfill is `loki.go`, `main.go`, and the
      uncovered parts of `brain.go`).

The first three are operator-side; only the fourth is local repo
work. Test backfill can land before the Pi soak completes.

### V0 non-features

- No write actions. `GET` against Prometheus and Loki, `POST` to
  Ollama and ntfy.sh, that is the entire egress surface.
- No deduplication or rate limiting on escalations
  ([ADR-0002](adr/0002-human-escalation-over-cloud-fallback.md)).
- No multi-host fanout, no HA, no failover.
- No structured action vocabulary in the diagnosis — just two
  strings, `cause` and `suggested_action`. The action is for human
  reading; nothing parses it.

## V1 — Action Proposals with HITL approval (next)

**Status:** planned. **Trigger to start:** V0 shipping definition met.

V1 turns Aceso from a diagnoser into an operator copilot. Each
incident gains a *proposed action* alongside the diagnosis, the
operator approves or rejects, and Aceso (on approval) executes the
proposed action and records the outcome. There is no autonomous
execution at this tier — every action requires explicit operator
intent before it runs.

### V1 shipping definition

- [ ] At least one approval surface working end-to-end on the Pi-CX23
      deployment (operator can see a proposal and approve it from
      their phone or laptop without SSH).
- [ ] At least one action type executes successfully under approval
      (the smallest meaningful one: `systemctl restart <unit>` on the
      CX23 itself).
- [ ] Schema-v1 incident format documented in
      `docs/incidents-schema.md` and migrated cleanly from the V0
      `escalated:true` shape.
- [ ] V0 soak data drove at least one design choice (recorded in an
      ADR — e.g. "we picked ntfy actions because soak showed P1 was
      always reachable by phone but P2 was never at a laptop").

### V1 features

The numbered items are mandatory; the lettered ones are explicitly
deferred and listed for clarity.

1. **Structured action proposals.** The diagnosis envelope grows a
   new field, `proposed_action: {kind, params, runbook_ref}`. `kind`
   is drawn from a fixed enum (V1: `systemctl_restart`,
   `docker_restart`, `noop`); the operator-supplied `runbook_ref` is
   a path into a local YAML registry. The free-text
   `suggested_action` continues to exist for human reading.
2. **Approval surface.** A way for the operator to see proposals and
   click approve/reject from a device they actually carry. Specific
   tech choice deferred to ADR-0004 (see open questions).
3. **Action executor.** A new `agent/executor.go` that takes an
   approved `proposed_action` and runs it under the same context
   timeout discipline as the rest of the agent. Single-action only;
   no chaining; per-kind allowlist of binaries; full stdout/stderr
   captured to the incident.
4. **Audit trail.** Every approval, rejection, and execution lands
   on the incident as additive fields: `approval: {by, at, decision,
   reason?}`, `execution: {started_at, finished_at, exit_code,
   stdout_excerpt, stderr_excerpt}`. `incidents.json` remains
   append-only NDJSON; updates are new lines keyed by
   `incident_id`.
5. **Timeout policy.** Proposals expire after a configurable window
   (default 1 hour). Expired proposals are recorded with
   `approval.decision = "expired"` and the action is not executed.
6. **Schema-v1 cutover.** Existing V0 incidents remain valid (the V1
   reader treats absent fields as additive defaults); new incidents
   carry an explicit `schema_version: 1` field. The
   `error: string` field migrates to `backend_errors:
   [{name, error, at}]` per the deferred-decision note in
   `status.md`.

### V1 non-features (deferred to V2 or later)

- a. **No autonomous execution.** If approval times out, nothing
     runs. There is no "auto-approve after N minutes for low-severity
     alerts" — that is V2's whole point.
- b. **No multi-step workflows.** A `proposed_action` is a single
     command. Sequences require V2's runbook engine.
- c. **No cross-incident correlation.** Each incident is judged on
     its own merits. Aggregating "the same alert fired 50x in 10 min"
     is dedup work, deferred.
- d. **No role-based approval.** V1 assumes one operator. Multi-user
     approval, quorum, or escalation chains are out of scope.
- e. **No web dashboard.** A query surface over `incidents.json`
     would be useful but is not on the V1 critical path; `jq` and
     `tail -F` remain the supported review tools.
- f. **No remote execution beyond the CX23 or Pi.** V1 actions only
     target hosts the agent already runs on or has direct SSH-key
     reach to. SSH-into-other-VPS is V2 territory.
- g. **No Coral classifier.** Listed in `status.md` V0-out-of-scope.
     Plausibly a V1+ feature once enough incident history exists to
     train against (estimated 3 months of `incidents.json`).

### V1 open design questions

These must be answered (in ADRs) before implementation starts.

- **ADR-0004: V1 approval surface.** Three serious candidates:
  ntfy.sh action buttons (free, already in the stack, but limited
  payload size and the operator must trust ntfy with action
  metadata); a small self-hosted web UI on the CX23 (full control,
  more code, needs CSP + auth); a CLI/SSH workflow ("`aceso review`"
  on the CX23). Decision is downstream of soak data and operator
  ergonomics.
- **ADR-0005: Action vocabulary.** Fixed enum vs. operator-supplied
  YAML runbook registry vs. both. Bias toward "both, with the enum
  as the safe default and the registry as the escape hatch."
- **ADR-0006: Approval state store.** Additive fields on
  `incidents.json` (simple, but every approval is a new NDJSON line
  the operator has to mentally `tail`) vs. a sidecar SQLite store
  (queryable, but adds a state file to back up). Lean toward
  additive NDJSON until a pain point appears.

## V2 — Bounded autonomous remediation (later)

**Status:** sketched. **Trigger to start:** V1 has been operated for
long enough that the *right* set of runbook candidates is obvious
from incident history (rough estimate: 3 months post-V1-ship).

V2 lets Aceso execute pre-approved runbooks autonomously when an
incident matches with high confidence. The operator's role moves
from per-incident approval to *runbook curation* and post-hoc audit.

### V2 sketch

- A YAML runbook registry on the CX23: `name`, `match_predicate`
  (alert labels + diagnosis cause regex), `action`, `confidence_floor`,
  `cooldown`, `max_executions_per_day`.
- An autonomous-action gate: incident matches a runbook + confidence
  ≥ floor + within cooldown + under daily cap → execute without
  approval.
- A "stop the world" kill switch: a single env flag or sentinel file
  that disables all autonomous execution and falls back to V1's
  approval flow.
- Every autonomous action is recorded with the runbook id, the match
  evidence, and the full execution audit. The operator can review
  and revoke runbooks based on this history.

### V2 explicit non-features

- No machine-learned match predicates. Runbook matching is a
  human-readable predicate the operator can audit.
- No privilege escalation. The `aceso` system user's sudoers entries
  remain a fixed allowlist; runbooks cannot grant themselves new
  privileges.
- No multi-host orchestration. V2 actions still target hosts the
  agent has direct reach to.
- No "self-improvement". The agent does not learn from its own
  execution history beyond the cooldown/cap counters; runbook
  evolution is operator work.

## Cross-cutting concerns

These are not milestones; they are invariants the milestones must
maintain.

### Schema versioning

`/data/incidents.json` is a contract with future tooling
(approval UI, V2 audit, post-incident review). Schema rules:

- Every new field is additive (`omitempty`) until V1 ships an
  explicit schema-version-1 cutover.
- Non-additive changes get a `schema_version` bump and a documented
  migration note in `docs/incidents-schema.md`.
- Readers tolerate missing fields. Writers do not emit fields they
  cannot populate.

### Local-only invariant

[ADR-0001](adr/0001-local-only-inference.md) and CLAUDE.md rule 11
hold across all milestones. V1 actions and V2 runbooks may target
operator-owned infrastructure but never call out to third-party
LLMs. If a future milestone needs cloud LLM access, it does so by
*superseding* ADR-0001, not by side-stepping it.

### Test coverage

Per CLAUDE.md rule 5: 80 % floor, race detector mandatory. New
features land with tests. The V0 backfill brings the package to the
floor before V1 implementation starts; V1 cannot ship below it.

### Out-of-band concerns (not on the roadmap)

- Pi-side `node_exporter` for Pi self-metrics.
- Model-registry trust (signed model weights).
- Multi-host monitoring (more than one CX23 + Pi pair).

These are recorded in `status.md` "V0 out-of-scope" with explicit
revisit triggers; the roadmap inherits those triggers and does not
duplicate them.

## What this doc is not

- Not a schedule. Calendar dates would rot the moment a soak
  surfaces something unexpected.
- Not a feature catalogue. New ideas land in `status.md`'s V0
  out-of-scope section first; they earn a roadmap entry only when
  they're scoped to a specific milestone.
- Not a design doc. ADRs (`docs/adr/`) hold decisions; this file
  holds the *order* in which decisions need to be made.
