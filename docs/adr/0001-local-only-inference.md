# ADR-0001: Inference is local-only

- **Status:** accepted
- **Date:** 2026-05-07
- **Deciders:** Emil Østergaard
- **Supersedes:** —
- **Superseded by:** —

## Context

Aceso ships production logs into an LLM prompt to get back a
diagnosis. The prompt routinely contains hostnames, user IDs, request
paths, stack traces, and whatever else the alerting target happened to
emit in the last query window. That data is operator-owned and must
not leave the operator's infrastructure.

Earlier drafts of the agent included optional cloud LLM backends
(DeepSeek, Gemini) gated behind environment-variable feature flags.
The intent was "graceful degradation" — if the local Ollama instance
was unreachable, the agent would fall back to a cloud provider rather
than skip the alert. Two problems with that:

1. **Flags rot, configuration drifts.** A backend that *exists in the
   binary* but is "off by default" is a backend that can be turned on
   accidentally — by an operator who didn't read the docs, by a stale
   `.env` file copied between hosts, by a misconfigured CI step. The
   blast radius of a wrong flag is "production logs leaked to a
   third-party endpoint." That is not a recoverable mistake.

2. **There is a better fallback than a cloud LLM.** When the local
   model is unreachable, the right thing for a self-healing agent to
   do is alert the human and wait — not invent a diagnosis using a
   different vendor's model with different reliability characteristics
   and different prompt-handling behaviour. See ADR-0002.

## Decision

The Aceso binary contains **no code paths** to any third-party LLM
API. This is not a configuration toggle: cloud-LLM client
implementations do not exist in the package, are not vendored, and are
not optionally compiled in.

Concretely:

- The `Backend` interface has exactly one production implementation,
  `OllamaBackend`, talking to a local-or-LAN Ollama instance.
- `buildBackendChain` rejects unknown backend names at startup. An
  operator setting `BACKEND_ORDER=deepseek,ollama` gets a startup
  error, not a silent fallback.
- A regression test (`TestBuildBackendChainRejectsCloudBackends`)
  pins this behaviour so a future PR cannot quietly add a
  cloud-shaped backend.
- `CLAUDE.md` rule 11 codifies the rule for AI agents working in
  the repo: "Inference is local-only. No exceptions."

When every backend in the chain fails, the agent escalates to a human
via a structured log line and an optional ntfy.sh push (see ADR-0002),
and persists the incident with `escalated: true`. It does **not**
silently route around the outage.

## Consequences

### Positive

- The supply-chain audit surface is the Go stdlib plus the operator's
  own Ollama instance. There is nothing else to attest, vendor, or
  rotate keys for.
- Production logs cannot leak to a third-party endpoint via Aceso,
  because there is no code path to a third-party endpoint.
- Operator trust model is simple: "the box that runs Aceso talks to
  the box that runs Ollama, and that's it." Both boxes are
  operator-owned.

### Negative

- When Ollama is down, alerts pile up in the escalation channel until
  the operator brings it back. There is no automated diagnosis during
  an Ollama outage. This is acceptable: in an operability tool, "I
  don't know, look at this" is a correct answer; "I confidently
  hallucinated a remediation" is not.
- The Pi inference plane (see ADR-0003) becomes a hard dependency for
  the V0 production deployment. This is mitigated by the Pi being
  cheap, well-understood, and runnable on hardware the operator
  already owns.

### Neutral

- A future ADR could revisit this if Aceso ever needs to operate in
  contexts where the operator does not own a viable LLM-runtime host.
  Until then, "add a cloud backend for reliability" should be rejected
  in favour of extending the escalation layer (ADR-0002).

## Implementation

- `agent/backends.go` — single backend, hostile name resolver
- `agent/backends_test.go::TestBuildBackendChainRejectsCloudBackends`
- `agent/escalate.go` — human-escalation path on chain failure
- `CLAUDE.md` rule 11 — agent-facing version of this decision
- Commit `fab6b3c` (refactor: remove cloud LLM backends from binary)
