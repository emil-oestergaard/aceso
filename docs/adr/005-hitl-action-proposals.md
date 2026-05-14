# ADR-005: V1 — Human-in-the-loop action proposals

- **Status:** proposed (draft)
- **Date:** 2026-05-14
- **Deciders:** Emil Østergaard
- **Supersedes:** —
- **Superseded by:** —
- **Related:** [ADR-001](001-local-only-inference.md), [ADR-002](002-human-escalation-over-cloud-fallback.md), [`../roadmap.md`](../roadmap.md), [`../incidents-schema.md`](../incidents-schema.md), [`../prd/v1-hitl.md`](../prd/v1-hitl.md)

This ADR consolidates the three planned slots in `roadmap.md`
(0004 approval surface, 0005 action vocabulary, 0006 approval state
store) into a single write-up of V1's HITL architecture. The
surface-choice question lives in the PRD; this ADR fixes the
*write path*, *lifecycle*, *storage*, *auth boundary*, *failure modes*,
and *rollback story* that any surface must live within.

## Context

V0 is, by construction, read-only. The agent issues `GET` against
Prometheus and Loki, `POST` to Ollama and ntfy.sh, and persists NDJSON
to a local volume. It writes no state on the host or any monitored
service (CLAUDE.md rule 10). That property is what made V0 cheap to
reason about: an Aceso bug cannot, in principle, break a service it is
monitoring.

V1 changes that. The agent gains the ability to execute commands on
the host it runs on, gated by explicit per-incident operator approval.
This is the largest architectural shift in the project's lifetime.
Everything else V0 established (local-only inference, plain WG tunnel,
GHCR images, NDJSON audit trail) survives the transition; the
*write-path discipline* is the new thing to get right.

The risk surface is asymmetric: V0 bugs are mostly cosmetic (a
malformed diagnosis text); V1 bugs include "the agent restarted the
wrong service in the middle of a real incident." The ADR's job is to
bound that asymmetry — make the new write surface small, auditable,
recoverable, and impossible to expand by accident.

## Decision

V1 introduces exactly one new code path with side effects on the host:
a single `Executor` chokepoint that takes an **approved**
`proposed_action` and runs it under timeout discipline. Everything
else V1 adds (proposal authoring, approval ingestion, expiry sweeping,
audit) is bookkeeping around that chokepoint.

### Write-path architecture

- The executor (`agent/executor.go`, planned) is the *only* new code
  path that calls anything other than `net/http.Client.Do`.
- Actions are dispatched by `proposed_action.kind`, a fixed
  compile-time enum. V1 ships three kinds: `systemctl_restart`,
  `docker_restart`, `noop`. Adding a kind is a code change plus an
  ADR amendment, not a config change.
- Per-kind handlers are individual functions with strict param
  schemas. Each handler:
  1. Validates params against the kind-specific schema.
  2. Looks up the resolved binary path against a compile-time
     allowlist (e.g. `/usr/bin/systemctl`, `/usr/bin/docker`). The
     allowlist is a `map[Kind]string` constant; no env-derived paths.
  3. Builds an argv vector — never a shell string.
  4. Executes under `exec.CommandContext` with the agent's tick
     deadline.
  5. Captures stdout/stderr (truncated to 4 KiB each) and the exit
     code.
- No chaining: one approved proposal → one executor invocation → one
  execution record. Multi-step workflows are V2.
- No remote targets: actions execute *as the agent's user, on the
  agent's host*. Pi-side actions, SSH-to-other-VPS, and
  HTTP-callouts-to-config-management are out of V1 scope.
- The agent process runs as the unprivileged `aceso` system user.
  Handlers that need root (`systemctl restart`) invoke through a
  fixed sudoers entry of the shape
  `aceso ALL=(root) NOPASSWD: /usr/bin/systemctl restart <allowed-units>`.
  The allowed-units list is operator-curated; the agent enforces it
  client-side too (defense in depth — sudoers wildcard accidents
  are real).

### Proposal lifecycle

The state machine is intentionally small:

```
created ──► pending ──► approved ──► executing ──► executed
                  │
                  ├──► rejected
                  └──► expired
```

- **created** is implicit: the moment an incident is appended to
  `/data/incidents.json` with a non-null `proposed_action`, the
  proposal exists. There is no separate "create proposal" call.
- **pending** is the same line as **created**; the distinction is
  only that "pending" describes the proposal *until* an approval line
  is written. From the operator's point of view, "pending" is the
  queue depth on the review surface.
- **approved / rejected / expired** are recorded by appending a new
  NDJSON line with the same `incident_id` and a populated `approval`
  object (per [incidents-schema.md](../incidents-schema.md) v1).
- **executing** is the window between `execution.started_at` and
  `execution.finished_at`. If the agent dies in this window, the
  next tick reconciles by appending an `execution` line with
  `exit_code = -2` and `stderr_excerpt = "execution lost (agent
  restart or crash)"`. Actions are *not* automatically re-tried; the
  operator decides what to do next.
- **executed** is terminal. Approval timeouts (default 1 h,
  `APPROVAL_TIMEOUT_SECONDS`) are enforced by a sweeper in the
  agent's tick loop, not by the approval surface. An expired
  proposal is recorded with `approval.decision = "expired"` and
  never executes.

### Storage model

- One file: `/data/incidents.json`. No sidecar SQLite, no separate
  proposal store.
- NDJSON, append-only. Lifecycle transitions are emitted as **new
  lines** keyed by `incident_id` (already specified in
  [incidents-schema.md](../incidents-schema.md) v1).
- Readers fold by `incident_id`, last-write-wins (the same contract
  V2 audit tooling will read).
- No compaction. File rotation is operator policy (`logrotate`,
  manual archival).
- Why not SQLite: adds CGO to the static-binary story, adds a backup
  target, and the fold-by-id pattern is sufficient for V1's expected
  throughput (≤1 incident/second, dominated by Ollama latency at
  15–20 s/diagnose).
- Why not a sidecar daemon: doubles the deploy surface, splits the
  audit trail across two log streams, and the agent already has the
  alert context it needs to author proposals.

### Auth boundary

Two distinct trust boundaries, deliberately separated:

1. **Operator → approval surface.** The surface (whatever it is)
   authenticates the operator. The mechanism is surface-specific and
   lives in the PRD. The ADR fixes only the invariant: *the surface
   must be reachable only by the operator, and its decisions must be
   cryptographically attributable to a single per-incident approval
   token issued by the agent.*
2. **Approval surface → agent.** The agent does **not** trust the
   surface to decide what actions are legal. The agent's executor
   enforces:
   - Per-kind allowlist (compile-time).
   - Per-kind param schema (compile-time).
   - Per-kind binary path (compile-time).
   - Sudoers allowlist (deploy-time).
   - Per-approval token match (request-time).

   A compromised approval surface cannot expand the action
   vocabulary, escalate privilege, or replay approvals across
   incidents.

The data trust boundary from ADR-001 is unchanged: production logs
and prompt content stay in `/data/incidents.json` on the CX23.
Approval surfaces that send anything to a third party (e.g. ntfy
actions) must send metadata only — same discipline as the v0
escalation push.

### Failure modes

| Failure | Recovery |
|---------|----------|
| LLM produces malformed `proposed_action` | Persisted with `proposed_action: null`, partial failure recorded in `backend_errors`. Operator sees diagnosis without action. |
| Approval surface unreachable | Proposals expire after `APPROVAL_TIMEOUT_SECONDS`. Sweeper runs in the agent's tick loop, not surface-dependent. |
| Approval arrives but kind is not in allowlist | Executor rejects; appends an `execution` line with a sentinel exit code and `stderr_excerpt` explaining the reject. No host side effect. |
| Action runs, exits non-zero | Recorded with real exit code plus truncated stderr. No automatic retry. |
| Agent crashes mid-execution | On restart, the next tick appends an `execution` line with `exit_code = -2`, `stderr_excerpt = "execution lost"`. Operator decides the next step. |
| Operator approves a hallucinated action | Bounded by kind enum, param schema, and sudoers allowlist. Worst-case is an in-allowlist action against an in-allowlist target — same risk profile as any sysadmin tool with a confused operator. |
| Two operators try to approve | First valid approval wins; subsequent approvals are recorded but no-op. V1 assumes one operator. |
| Action targets the agent itself (`systemctl restart aceso`) | The agent's own unit name is on a per-kind *denylist*; the handler refuses before invoking sudo. |
| ntfy down during expiry of many proposals | Expiry is silent (no notification path). Operator sees the backlog on next review-surface visit. |

### Rollback story

Two senses, both load-bearing.

**A. Per-action rollback.** V1 actions are not reversible by the
agent. `systemctl restart` has no defined inverse beyond "wait and
observe." If an approved action makes things worse, the operator
intervenes manually on the host; the agent does not try to be
clever. This is a deliberate scope cap: per-action rollback requires
a runbook engine, which is V2.

**B. Milestone rollback (V1 → V0).** Image roll: pin `ACESO_IMAGE`
to a V0 SHA in `.env`, then `docker compose up -d`. The V0 binary
tolerates v1 NDJSON lines as "incidents with unknown additive
fields" per the reader contract in
[incidents-schema.md](../incidents-schema.md). Approval surface: stop
the daemon if any. No file migration needed; v1 lines remain on disk
and are read by future v1 binaries when the operator rolls forward
again. **One-way concerns:** an *executed* action's host effect is
not undone by rolling back the binary — same as any deploy with side
effects.

## Consequences

### Positive

- V0's three load-bearing properties survive: local-only inference
  (ADR-001), human escalation on chain failure (ADR-002), and
  append-only NDJSON audit trail. V1 is additive, not a rewrite.
- The new write surface is tiny: three kinds, three param schemas,
  three binary paths, one sudoers entry shape. The whole executor
  fits in one file the operator can read in five minutes.
- All policy is in the agent. Swapping the approval surface (CLI →
  web → ntfy actions → something else) is a contained change that
  does not re-open the action vocabulary.
- Schema cutover is already documented; v1's NDJSON shape is not
  invented here.
- Append-only NDJSON keeps forensics easy: every state transition is
  a line; nothing is overwritten.

### Negative

- V0's "no host writes" compile-time invariant is gone permanently.
  After V1 ships, `agent/executor.go` exists, and there is no way to
  assert at the type level that an action handler cannot be called.
- LLM hallucination becomes a real-world risk in a new way: a
  fabricated `proposed_action` that happens to fall in the kind enum
  and pass the param schema is approvable. The operator's review
  discipline is the load-bearing mitigation.
- The agent gains state-machine complexity (expiry sweeper, crash
  recovery) that V0 did not have. The "stateless except for
  incidents.json" simplification is weakened.
- The NDJSON file grows faster (≥3 lines per actioned incident:
  created, approved, executed). Rotation policy becomes
  operationally relevant.
- The approval surface is now a piece of infrastructure the operator
  must keep running. V0 had one box to think about (CX23); V1 has
  CX23 + Pi + approval-surface-host (probably the same CX23, but
  conceptually distinct).

### Neutral

- The choice of approval surface (CLI / web / ntfy actions / mix) is
  deferred to the PRD and to first V1 implementation iteration. This
  ADR fixes the invariants any surface must satisfy.
- Runbook registry format (YAML vs. directory of scripts vs.
  something else) is deferred. V1 ships with the kind enum
  hard-coded; the registry is the operator's per-deployment
  customisation surface, not a binary contract.
- The kind enum will grow in V1.1 / V1.2. Each addition is a code
  change plus an ADR amendment; the ADR-005 status moves from
  `proposed` to `accepted` when V1 ships, and remains the reference
  for "what is the V1 action vocabulary."

## Alternatives considered

| Option | Rejected because |
|--------|------------------|
| Full autonomy in V1 (V2 features) | CLAUDE.md rule 10 forbids write paths without HITL. The roadmap explicitly stages V1 → V2 to give the operator time to *see what the agent proposes* before promoting any of those proposals to autonomous execution. |
| Sidecar SQLite for approval state | Adds CGO, breaks the static-binary story, doubles the durability target. The NDJSON fold has been good enough for similar audit-heavy systems. Revisit if `incidents.json` exceeds ~10 GB or if fold-by-id becomes a hot path. |
| Separate control-plane daemon | Doubles deploy surface, splits the audit trail, and the agent already has the alert context to author proposals. The approval *surface* can be a separate process; the proposal *authoring* and *execution* belong in the agent. |
| Generalised RPC action API (`POST /api/execute`) | Externalises policy enforcement. A compromised surface or a misconfigured firewall could call it with arbitrary commands. The fixed enum plus per-kind handler keeps the policy server-side. |
| Filesystem-based approval (`touch /var/lib/aceso/approve/<id>`) | Clever but inscrutable; the approval credential becomes "write access to a directory," which does not compose well with auth. The agent's executor needs a verifiable approval, not a sentinel file. |
| Inverse "rollback" runbooks in V1 | Requires a runbook engine and a defined inverse for each action. That is V2's whole point; bolting half of it onto V1 would muddy the milestone. |

## Implementation (planned)

This ADR is a draft; no code lands until V0's soak completes and the
V1 PRD is signed off. Forward-looking pointers so the next person
knows where to look:

- `agent/executor.go` (new) — `Executor` interface, per-kind
  handlers, sudoers-aware exec.
- `agent/proposal.go` (new) — proposal lifecycle state machine,
  expiry sweeper.
- `agent/approval.go` (new) — surface-agnostic approval-ingestion
  contract, per-incident token issuance and verification.
- `agent/brain.go` (modified) — incident-creation path emits
  `proposed_action` when LLM output includes one; expiry sweeper
  called from `Tick`.
- `agent/config.go` (modified) — new env vars: `APPROVAL_SURFACE`,
  `APPROVAL_TIMEOUT_SECONDS`, `EXECUTOR_KINDS_ALLOWED` (subset of
  compiled-in kinds, deploy-time hardening).
- `agent/main.go` (modified) — sweeper goroutine plus crash-recovery
  reconciliation on startup.
- `agent/executor_test.go`, `agent/proposal_test.go`,
  `agent/approval_test.go` (new) — unit + integration coverage at
  the 80 % floor before V1 ships.
- `docs/incidents-schema.md` — v1 shape already documented; no
  schema-doc changes required for this ADR.
- Sudoers fragment (deploy artefact): `/etc/sudoers.d/aceso-v1`,
  shipped via `scripts/cx23-setup.sh` (or successor). Audited diff
  before each kind-enum expansion.
