# ADR-0002: Human escalation, not cloud fallback, when local inference is unreachable

- **Status:** accepted
- **Date:** 2026-05-07
- **Deciders:** Emil Østergaard
- **Supersedes:** —
- **Superseded by:** —
- **Related:** [ADR-0001](0001-local-only-inference.md)

## Context

ADR-0001 removes cloud LLM backends from the binary. That decision
forces a follow-up question: when the local inference path is
unreachable (Pi off, WireGuard tunnel down, model OOM, Ollama
crashed), what should the agent do with the alerts it can't diagnose?

The wrong answers, in increasing order of harm:

1. **Drop the alert silently.** History gets a hole; the operator
   never finds out.
2. **Invent a diagnosis from a built-in heuristic.** Looks fine in
   demos, fails the first time it's load-bearing.
3. **Fall back to a cloud LLM.** Defeats ADR-0001.

Aceso is observability infrastructure. The cost of "I don't know"
delivered loudly is much lower than the cost of "I think I know"
delivered confidently and wrongly. We need a path that records the
failure, surfaces it to a human, and waits.

## Decision

When every backend in the chain returns an error, the agent escalates
the alert through a dedicated `Escalator` interface (`agent/escalate.go`)
and persists the incident with `escalated: true`. There is no
automated retry-with-different-vendor path.

The default `NtfyEscalator` does three things:

1. **Always:** emit a structured log line of the form
   `[escalate] alert="..." severity="..." state="..." backend_error="..."`.
   This is free, lands in whatever shipper is already capturing aceso's
   stdout (Promtail, journald), and is trivially queryable in LogQL.
2. **If `ESCALATE_NTFY_URL` is set:** POST a metadata-only summary
   to ntfy.sh (or a self-hosted equivalent). The body contains alert
   name, severity, and state plus a fixed "see logs" pointer. **It
   does not contain the wrapped backend error**, because that error
   can transitively include truncated model output (see
   `agent/ollama.go` `raw=...` decode-failure path), which is
   downstream of production log content. Sending that to a
   third-party notification service would re-create the
   exfiltration risk we just removed. The full error stays in the
   local log line and the on-disk incident.
3. **Always:** persist an incident to `/data/incidents.json` with the
   additive `escalated: true` field, the failed-chain error string,
   and a zero-valued diagnosis. The incident log is the audit trail
   even when the diagnosis is missing.

Failure to deliver the ntfy push is logged but does not propagate as a
fatal error: an unreachable ntfy server should not itself become a
paging incident. The structured log line is the source of truth.

## Consequences

### Positive

- Operator gets a push, instantly, when the inference path breaks.
  The "wait for the human" semantics from ADR-0001 are real, not
  aspirational.
- The on-disk incident log captures *what the agent could not see at
  decision time*, which is exactly the information needed for a
  post-incident review.
- ntfy is dependency-light: a single POST, no SDK, no auth flow, no
  service account to rotate. Self-hosting is a one-line nginx config
  change.
- The Escalator is an interface, so swapping in (e.g.) a Slack or
  PagerDuty path later is a contained change with the same
  metadata-only body discipline.

### Negative

- An ntfy outage during a local-Ollama outage means the operator only
  has the structured log line to find the alert. This is acceptable:
  the log line is always emitted, and the operator has direct access
  to incidents.json. The ntfy push is a convenience, not a
  correctness layer.
- Operators who don't read the runbook may set `ESCALATE_NTFY_URL` to
  a guessable topic and get nuisance traffic from random ntfy users
  to the same topic. The `.env.example` and pi-deploy runbook both
  call this out; the topic should be unguessable
  (e.g. `aceso-emil-7f3a9b`).

### Neutral

- The escalation rate is unbounded. If the LLM path fails for a while,
  every alert in that window produces a structured log line and a
  push. V0 punts on rate limiting; an open V0-out-of-scope item in
  `docs/status.md` tracks this for V1.

## Alternatives considered

| Option | Rejected because |
|--------|------------------|
| Cloud LLM fallback | Defeats ADR-0001. |
| Built-in heuristic / template diagnosis | Confidently-wrong is worse than "I don't know." |
| Retry-with-backoff and silently drop after N | Hides the underlying outage from the operator. |
| Email | Higher latency, more moving parts (SMTP, deliverability), and the operator already has a phone-shaped notification surface via ntfy. |
| Slack/PagerDuty as default | Requires SDK, auth, and credential rotation. The Escalator interface keeps these as easy future implementations without making them the default. |

## Implementation

- `agent/escalate.go` — `Escalator` interface + `NtfyEscalator`
- `agent/escalate_test.go` — body redaction + best-effort delivery
- `agent/brain.go` — `Brain.escalateAlert` routes chain failures and
  writes the `escalated: true` incident
- `agent/config.go` — `EscalateNtfyURL` env field, no default
- `.env.example` — operator guidance + topic unguessability note
- Commit `e24ebbc` (feat: add human-escalation layer on backend-chain failure)
