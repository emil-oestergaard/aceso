# Architecture Decision Records

Architecture Decision Records (ADRs) are short text files that capture a significant
architectural decision made during a project, along with its context and consequences.
This directory serves as the "living history" of Aceso's evolution. Each ADR is
a one-time write-up — they are not living docs and should not be edited after `accepted`
except to mark them `superseded` and link forward.

If a decision is small enough that it lives in `CLAUDE.md` rules,
`docs/status.md` rows, or a code comment, it does not need an ADR.

## Format

Each ADR follows a tight template: status, context, decision,
consequences (positive / negative / neutral), and an implementation
section pointing at the actual files and commits.

## Current ADRs

| # | Title | Status |
|---|-------|--------|
| [001](001-local-only-inference.md) | Inference is local-only | accepted |
| [002](002-human-escalation-over-cloud-fallback.md) | Human escalation, not cloud fallback, when local inference is unreachable | accepted |
| [003](003-plain-wireguard-over-tailscale.md) | Plain WireGuard for the Pi inference plane (no Tailscale) | accepted |
| [004](004-ghcr-image-publishing.md) | Publish the agent image to GHCR; CX23 pulls instead of builds | accepted |
