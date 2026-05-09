# Architecture Decision Records

Decisions that shape Aceso's surface area and trust model. Each ADR is
a one-time write-up — they are not living docs and should not be
edited after `accepted` except to mark them `superseded` and link
forward.

If a decision is small enough that it lives in `CLAUDE.md` rules,
`docs/status.md` rows, or a code comment, it does not need an ADR.
Reach for an ADR when:

- The decision rules out a category of future implementation
  (e.g. "no cloud LLM backends ever").
- The decision will be questioned again in 3–6 months by a
  reasonable engineer (including future-you) who didn't see the
  trade-offs the first time.
- The decision affects more than one file in the repo and the *why*
  isn't visible from any one of them.

## Format

Each ADR follows a tight template: status, context, decision,
consequences (positive / negative / neutral), and an implementation
section pointing at the actual files and commits. ADRs are numbered
monotonically and kept in this directory; numbers are never reused.

## Current ADRs

| # | Title | Status |
|---|-------|--------|
| [0001](0001-local-only-inference.md) | Inference is local-only | accepted |
| [0002](0002-human-escalation-over-cloud-fallback.md) | Human escalation, not cloud fallback, when local inference is unreachable | accepted |
| [0003](0003-plain-wireguard-over-tailscale.md) | Plain WireGuard for the Pi inference plane (no Tailscale) | accepted |
| [0004](0004-ghcr-image-publishing.md) | Publish the agent image to GHCR; CX23 pulls instead of builds | accepted |
