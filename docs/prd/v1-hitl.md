# V1 PRD — Human-in-the-loop action proposals

- **Status:** draft
- **Date:** 2026-05-14
- **Author:** Emil Østergaard
- **Trigger to start implementation:** V0 soak completes (2026-05-20) with no escalation-rate issues.
- **Related:** [`../roadmap.md`](../roadmap.md), [`../adr/005-hitl-action-proposals.md`](../adr/005-hitl-action-proposals.md), [`../incidents-schema.md`](../incidents-schema.md)

## The problem

V0 reads alerts and writes diagnoses. Today the operator's workflow
during an incident is:

1. Phone buzzes (ntfy escalation, or Prometheus Alertmanager separately).
2. SSH into the CX23.
3. `tail -F /data/incidents.json | jq` to see what Aceso thought.
4. Decide what to do.
5. Run the command by hand (`sudo systemctl restart worker`,
   `docker restart api`, etc.).

Steps 1 and 2 are unavoidable until V2. Step 3 already pays off: when
the LLM diagnosis is right, the operator skips the "stare at logs"
phase. **Step 5 is the part that hurts**: the operator already knows
what they want done — Aceso usually wrote the command in
`suggested_action` — and they are now typing it into a phone-tethered
SSH session at 2 AM. Half the time they are paraphrasing exactly what
is on screen.

V1's job is to close that loop without crossing the autonomy line.
The operator should be able to *approve* the action Aceso already
proposed, and have Aceso run it, without typing the command themselves.

Concretely:

- The diagnosis envelope grows a structured `proposed_action`
  alongside the free-text `suggested_action`.
- The operator gets a notification with the proposal (alert + cause
  + the specific command that will run).
- One tap / one keystroke approves it; one tap / one keystroke
  rejects it.
- Aceso executes the approved action and records the outcome on the
  incident.
- No action runs without explicit operator intent.

## What "approval" looks like in practice

The ADR fixes the invariants any approval surface must satisfy
([ADR-005](../adr/005-hitl-action-proposals.md)). This section
describes the operator-facing experience and outlines the surfaces
under consideration. The actual choice is downstream of soak data:
which alerts fire, how often, where the operator is when they fire.

### Operator-facing requirements (surface-independent)

1. **Reachable from a phone.** Most pages happen away from the
   laptop. Anything that requires a desktop browser or an active SSH
   session by default is wrong.
2. **One-tap decide.** Approve / reject / (long-press)
   reject-with-reason. No "fill out a form" flow.
3. **Shows the operator everything they need to decide.** Alert
   name, severity, instance, the LLM's cause, the LLM's suggested
   action, the *specific command* that will run if approved, and a
   pointer to where the full log context lives (`incidents.json`,
   last N log lines).
4. **No surprise execution.** Approval timeouts are visible. An old
   proposal that expired must not run if the operator approves it
   after the window.
5. **Audit-trail-visible to the operator.** The operator can see
   what they approved, when, what ran, and what it returned. Not
   just "Aceso ran something three days ago" — full context.

### Surface candidates

Two surfaces, evaluated against the requirements above. Choice — and
whether we ship one or both — is deferred to first-iteration
implementation work and may be informed by V0 soak data (e.g., do
operators actually get pages on their phone, or is everything
desktop-first?).

#### A. CLI (`aceso review` on the CX23)

- **Operator flow:** SSH into the CX23, run `aceso review`. Interactive
  TUI lists pending proposals; arrow keys + Enter to drill in; `a` /
  `r` / `q` to act.
- **Pros:** No new HTTP surface. Authentication is "SSH access to the
  CX23," which is already the operator's strongest auth channel. Zero
  new attack surface beyond the existing SSH path. Implementation is
  one Go binary; the same image already ships.
- **Cons:** Requires the operator to be at a real keyboard. Phone SSH
  works but is friction. Does not compose with ntfy.sh notifications
  (operator gets the push, has to switch to terminal app to act).
- **Reasonable shape for V1.0:** ship this as the *baseline* — every
  Aceso deploy has it, regardless of whether other surfaces are added.

#### B. Web (small self-hosted UI on the CX23)

- **Operator flow:** Phone push from ntfy.sh contains a link to
  `https://aceso.<operator-domain>/<incident-id>`. Tap → minimal HTML
  page shows the proposal context plus approve/reject buttons. Action
  records the decision and triggers execution.
- **Pros:** Phone-first. Composes naturally with the existing ntfy
  push (add a "Click" header pointing at the UI URL). One tap from
  page to decision.
- **Cons:** New HTTP surface to expose. TLS termination needs to
  land. Auth model has to be designed (shared secret? client cert?
  operator-device pair flow?). Requires a public hostname (or
  operator's existing VPN reach to the CX23).
- **Reasonable shape for V1.1+:** layer on top of the CLI baseline
  once one cycle of real-world V1 use has clarified the auth model
  the operator actually wants.

### What we are *not* shipping as a surface in V1

- **ntfy.sh action buttons.** ntfy supports inline action buttons
  that POST to a URL. Tempting because it removes the "switch to
  browser" step entirely. Rejected for V1 because the auth model is
  hard: the action URL is whatever ntfy POSTs, and the only thing the
  agent can authenticate is "the request came from ntfy.sh." That
  gives ntfy.sh (or anyone who learns the topic name and the URL
  shape) the ability to approve actions. ADR-005 mandates that
  approvals carry a per-incident token issued by the agent;
  threading that through ntfy's action button format is possible but
  the result is fragile. Revisit in V1.x if the auth story tightens.
- **Slack / Discord bots.** Same auth concerns as ntfy actions, plus
  a third-party trust path that ADR-001 was specifically designed to
  avoid for the inference plane. Could plausibly fit if the bot
  lives inside operator-controlled infra (self-hosted Mattermost?),
  but that is a V2-shaped question.
- **Email approval.** Inbound email is the worst auth surface of all
  the candidates.

## Success criteria

V1 ships when all of these are true on the production CX23 + Pi
deployment:

1. **One approved action executes end-to-end without the operator
   typing the command.** The operator gets a notification, decides
   via the chosen surface, and Aceso runs the action. The incident
   records the full lifecycle. The action type is
   `systemctl_restart` against an operator-curated unit allowlist
   (the smallest meaningful action).
2. **One rejected proposal exits cleanly.** Rejection is recorded;
   nothing runs. The operator can see *why* they rejected it
   (free-text reason, optional).
3. **One expired proposal exits cleanly.** A proposal sitting
   unhandled past `APPROVAL_TIMEOUT_SECONDS` is recorded with
   `approval.decision = "expired"`. No action runs. The operator can
   see the backlog on the next review surface visit.
4. **Crash recovery exercised.** The operator (or
   `docker compose restart aceso`) kills the agent mid-execution;
   the next tick reconciles the in-flight action with the documented
   sentinel exit code. Tested at least once against a real action.
5. **Audit trail readable by `jq`.** `jq` queries against
   `incidents.json` answer: "show me all incidents from last week
   where Aceso proposed an action," "show me what I approved and what
   ran," "show me which alerts I always reject." No tooling beyond
   `jq` required for first-iteration ops.
6. **V0 invariants intact.** Local-only inference, append-only
   NDJSON, ntfy escalation on chain failure, plain-WG Pi inference
   plane — all still operational after V1 ship. Verified by
   re-running the V0 smoke test against a V1 binary.
7. **Schema-v1 NDJSON readable by V0 binary.** Rollback verified:
   pin `ACESO_IMAGE` to a V0 SHA, restart, observe the agent reads
   v1 lines without crashing (unknown fields ignored per reader
   contract).
8. **Test coverage at the 80 % floor.** `go test -race -cover ./...`
   on the V1 branch passes the same gate V0 must.

## Explicitly out of scope

These appear plausible adjacent to V1 and are not in this milestone.
Most are recorded with more detail in
[`../roadmap.md`](../roadmap.md); listed here so the V1 implementer
does not accidentally pull them in.

- **Autonomous execution.** Approval timeouts produce *no* action,
  not "auto-approve for low severity." That is V2's whole point.
  ADR-005 makes this a hard line.
- **Multi-step workflows / runbooks.** A proposal is one command.
  Sequences, conditionals, and retries-with-backoff are V2.
- **Cross-incident correlation.** "The same alert fired 50x in 10
  min — only propose once" is dedup work. V1 ships proposals
  one-per-incident; dedup is V1.x at earliest.
- **Multi-operator approval.** V1 assumes one operator. Quorum,
  role-based approval, on-call rotation routing, and approval
  delegation are V2+ if they happen at all.
- **Web dashboard for browsing incident history.** The "review
  surface" in V1 is for *acting on pending proposals*. Querying
  historical incidents stays in `jq` + `tail` territory. A dashboard
  is a separate product question.
- **Remote execution.** V1 actions run on the host the agent runs on
  (the CX23). Pi-side actions are theoretically possible — same
  `Backend`-style abstraction, different endpoint — but are out of
  scope. So is any action that targets a third VPS the agent has
  SSH access to.
- **Pre-LLM classifier fast path.** Listed in V0 out-of-scope;
  trigger to revisit is "3 months of `incidents.json` to train
  against," which V1 produces but does not consume.
- **Action vocabulary expansion beyond V1.0.** V1.0 ships
  `systemctl_restart`, `docker_restart`, `noop`. New kinds are
  deliberate additions in V1.x with their own ADR amendment.
- **Approval surface TLS / HTTP wiring choice.** If we ship the web
  surface, those decisions are first-iteration implementation work,
  not PRD scope. The PRD fixes the operator experience; the
  implementation chooses the wiring.

## Open questions for the implementer

Listed so they do not get lost. Resolution belongs in implementation
work or in a follow-up ADR amendment, not in this PRD.

- **Default approval timeout.** The roadmap says 1 hour. Soak data
  may suggest different ranges per severity. PRD lean: ship one
  global default in V1.0; per-severity timeouts in V1.1 if there is
  signal.
- **Per-incident token shape.** ADR-005 mandates a token; the format
  (HMAC, ULID-with-secret, random opaque) is implementation work.
- **"Approve all matching" affordance.** Operator-side ergonomics:
  if the same alert fires three times during a deploy, can the
  operator approve once and have it apply to subsequent proposals of
  the same shape? This is half-way to dedup; lean *no* in V1.0 to
  keep the audit trail clean.
- **`jq` recipes.** Ship a `docs/v1-jq-cookbook.md` with three or
  four worked queries (pending proposals, approval rate, action exit
  codes). Not a PRD requirement, but a cheap operator-experience win.

## What this doc is not

- Not a UI mockup. The surface candidates above describe the
  *flow*, not the pixel layout. If a web surface ships, its HTML/CSS
  is implementation work and lives in the relevant code-review pass.
- Not a deployment plan. That lives in `docs/pi-deploy.md` style
  runbooks once the V1 implementation is on a branch.
- Not a model-prompting spec. Whether and how the LLM gets prompted
  to emit `proposed_action` is V1 implementation work; this PRD
  specifies that *the field exists*, not how to fill it.
