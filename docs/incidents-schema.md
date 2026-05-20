# docs/incidents-schema.md — `/data/incidents.json` line format

> Last updated: 2026-05-09
>
> `/data/incidents.json` is a contract with future tooling (V1
> approval UI, V2 audit, post-incident review). This file is the
> authoritative description of the line format. Source of truth for
> the wire shape is `agent/brain.go:Incident` and its referenced
> structs; this doc explains the *why* and the *evolution rules*
> alongside the shape.

## Format invariants

These hold across every schema version.

- **NDJSON.** One incident per line. Lines are append-only. Readers
  may safely `tail -F` the file and parse one line at a time.
- **No reordering.** New incidents append to the end. Lines are
  never edited in place — see the "updates" rule below for V1.
- **UTF-8.** Strings are UTF-8. Embedded log lines are best-effort
  decoded; invalid byte sequences are replaced before write.
- **No comments, no blank lines.** Every byte until `\n` is part of
  exactly one JSON object.
- **Timestamps are RFC 3339 with timezone.** Go's `time.Time` JSON
  marshalling produces `"2026-04-29T22:07:00Z"` for UTC, with
  nanoseconds preserved when present
  (`"2026-04-29T22:07:00.123456789Z"`). Readers parse with
  `time.RFC3339Nano`.

## Schema-v0 (current)

The shape live in production today. No `schema_version` field —
absence of the field implies v0.

```json
{
  "timestamp": "2026-04-29T22:07:00Z",
  "alert": {
    "labels": {
      "alertname": "HighCPU",
      "severity": "warning",
      "instance": "vps01"
    },
    "annotations": {
      "summary": "CPU above threshold"
    },
    "state": "firing",
    "activeAt": "2026-04-29T22:05:30Z",
    "value": "85.5"
  },
  "log_lines": [
    {
      "timestamp": "2026-04-29T22:06:55Z",
      "line": "oom-killer: process 1234 (worker)",
      "stream": { "job": "node", "host": "vps01" }
    }
  ],
  "diagnosis": {
    "cause": "Worker OOM after recent deploy.",
    "suggested_action": "Restart the worker; roll back if it recurs."
  }
}
```

### Optional v0 fields

These are emitted only when populated; readers must treat absence as
the documented default.

| Field | Type | Default when absent | Meaning |
|-------|------|---------------------|---------|
| `error` | string | `""` (no partial failure) | Single concatenated string capturing partial failures during the diagnose chain. Format is informal (`"loki: …; backend: …"`). **Do not parse**; treat as opaque human-readable text. Migrating to a structured form in v1 — see below. |
| `escalated` | bool | `false` | `true` when the entire backend chain failed and the incident was escalated to a human. When `true`, `diagnosis` is the zero value (`{"cause":"", "suggested_action":""}`) and `error` carries the chain failure detail. |

### Field-by-field

| Field | Type | Required | Source |
|-------|------|----------|--------|
| `timestamp` | RFC 3339 string | yes | `agent/brain.go` — when the incident was *recorded*, not when the alert started. |
| `alert.labels` | map<string,string> | yes | Prometheus alert labels, verbatim. |
| `alert.annotations` | map<string,string> | yes | Prometheus alert annotations, verbatim. |
| `alert.state` | string | yes | One of `"firing"`, `"pending"`, `"inactive"`. V0 only persists `"firing"`. |
| `alert.activeAt` | RFC 3339 string | yes | When Prometheus first marked the alert active. |
| `alert.value` | string | yes | Prometheus's string-encoded numeric value (Prometheus emits this as a string, we don't reinterpret). |
| `log_lines[].timestamp` | RFC 3339 string | yes | Log line timestamp from Loki. |
| `log_lines[].line` | string | yes | Raw log line. May contain operator-owned data (hostnames, request paths, stack traces) — see [ADR-0001](adr/0001-local-only-inference.md). |
| `log_lines[].stream` | map<string,string> | yes | Loki stream labels for the matched line set. |
| `diagnosis.cause` | string | yes (may be empty when `escalated=true`) | LLM output. |
| `diagnosis.suggested_action` | string | yes (may be empty when `escalated=true`) | LLM output. Free text — nothing parses this in v0. |

### v0 rules of evolution

1. New optional fields may be added without bumping schema version,
   but must use `omitempty` so existing readers see byte-identical
   lines on the success path.
2. Existing fields cannot change type or semantics without a version
   bump.
3. The `error` field is **frozen as unstructured**. Do not add more
   informal text into it. When V1 needs structured failure history,
   the field migrates wholesale to v1's `backend_errors` array.

## Schema-v1 (planned, lands with V1 milestone)

V1 introduces action proposals, human-in-the-loop approval, and
execution audit. This requires the first non-additive schema
evolution; v1 carries an explicit `schema_version: 1` field and
restructures the `error` field.

### Cutover semantics

- Existing v0 lines remain valid forever. Readers identify them by
  the absence of `schema_version` and apply v0 defaults.
- New incidents written by a v1-or-later agent always carry
  `schema_version: 1`. There is no `schema_version: 0` value; v0
  is the implicit fallback.
- Incidents are **mutable in v1** — proposals get approved, actions
  get executed, outcomes are recorded. NDJSON stays append-only;
  updates are emitted as **new lines** keyed by `incident_id` with
  the latest state. Readers fold by `incident_id` and take the
  last-write-wins line. (This is simpler than rewriting the file
  and keeps the audit trail intact.)

### v1 shape (planned)

```json
{
  "schema_version": 1,
  "incident_id": "01J5K7P2Q3R4S5T6V7W8X9Y0Z1",
  "timestamp": "2026-08-12T14:30:00Z",
  "alert": { "...": "as in v0" },
  "log_lines": [ { "...": "as in v0" } ],
  "diagnosis": {
    "cause": "Worker OOM after recent deploy.",
    "suggested_action": "Restart the worker; roll back if it recurs."
  },
  "proposed_action": {
    "kind": "systemctl_restart",
    "params": { "unit": "worker.service" },
    "runbook_ref": "runbooks/worker-oom.yaml"
  },
  "approval": {
    "by": "emil",
    "at": "2026-08-12T14:32:11Z",
    "decision": "approved",
    "reason": null
  },
  "execution": {
    "started_at": "2026-08-12T14:32:12Z",
    "finished_at": "2026-08-12T14:32:14Z",
    "exit_code": 0,
    "stdout_excerpt": "(empty)",
    "stderr_excerpt": ""
  },
  "backend_errors": [
    { "name": "ollama", "error": "context deadline exceeded", "at": "2026-08-12T14:30:45Z" }
  ]
}
```

### v1 new fields

| Field | Type | Required | Notes |
|-------|------|----------|-------|
| `schema_version` | int | yes (in v1+) | Always `1` for v1 lines. Readers use this to dispatch on shape. |
| `incident_id` | ULID string | yes | Stable across update lines. ULID chosen over UUID for monotonic-ish ordering and shorter representation. |
| `proposed_action` | object \| null | optional | Structured action the LLM (or runbook lookup) suggests. `null` when no action is appropriate (e.g. info-only alert). |
| `proposed_action.kind` | enum string | yes if `proposed_action != null` | V1 enum: `"systemctl_restart"`, `"docker_restart"`, `"noop"`. Future kinds require a code change, not a schema change. |
| `proposed_action.params` | object | yes if `proposed_action != null` | Kind-specific parameters. Validated against per-kind schemas in `agent/executor.go`. |
| `proposed_action.runbook_ref` | string \| null | optional | Path into the operator-curated YAML runbook registry. `null` when the action is direct (no runbook). |
| `approval` | object \| null | optional | Human decision. `null` until the operator acts or the proposal expires. |
| `approval.by` | string | yes when present | Operator identifier (V1: single operator, free string). |
| `approval.at` | RFC 3339 | yes when present | When the decision was recorded. |
| `approval.decision` | enum string | yes when present | `"approved"`, `"rejected"`, `"expired"`. |
| `approval.reason` | string \| null | optional | Free text. `null` for `approved` and `expired`; usually populated for `rejected`. |
| `execution` | object \| null | optional | Action outcome. `null` until the action runs (or never, if rejected/expired). |
| `execution.started_at` | RFC 3339 string | yes when present | When the executor invoked the command. Distinct from the incident `timestamp` (recording time, not action-start time). |
| `execution.finished_at` | RFC 3339 string | yes when present | When the command returned. Absent on the `exit_code = -2` crash-recovery path (the agent died mid-run; the real finish time is unknown). |
| `execution.exit_code` | int | yes when present | OS exit code. `0` is success. Sentinel `-2` means the execution record was reconstructed after an agent crash and the command's actual outcome is unknown (per [ADR-005](adr/005-hitl-action-proposals.md)). |
| `execution.stdout_excerpt` | string | yes when present | Truncated to 4 KiB; full output lives in journalctl. |
| `execution.stderr_excerpt` | string | yes when present | Same truncation. |
| `backend_errors` | array of object | optional | Migrated from v0's `error` string. Each entry is `{name, error, at}`. Empty array on full success; non-empty when one or more backends in the chain failed. The chain-success line and the chain-failure line are distinguished by `escalated`, not by this array's emptiness. |

### v1 fields removed / changed

| Field | Change | Migration |
|-------|--------|-----------|
| `error` | Removed in v1 | Replaced by `backend_errors`. v1 readers do not populate `error`. v0 readers seeing a v1 line will see no `error` field and should treat absence as "no partial failure" — which is wrong for escalated v1 lines, but tolerable since v0 readers are not expected to see v1 lines (the version bump is the signal to upgrade). |
| `escalated` | Unchanged | Same semantics: `true` when the chain fully failed and the incident was surfaced to a human. |
| `timestamp` | Unchanged | Recording time, not alert start time. |

## Update lines (v1 only)

When the operator approves a proposal or an action executes, the
agent emits a *new line* with the same `incident_id` and the
updated state.

Readers fold by `incident_id` and prefer the line with the latest
`timestamp` for any field that changed. The original "first sighting"
line remains in the file as the audit trail. Tooling that wants the
current state of an incident reads the *last* line for each id.

There is no compaction in v1. The file grows linearly; rotation is
the operator's choice (logrotate, manual archival, etc.).

## Reader contract

A well-behaved reader:

1. Parses each line as JSON, dispatches on `schema_version`
   (absent ⇒ v0).
2. Treats unknown fields as informational. Never errors on a field
   it doesn't recognise.
3. For v0: applies documented defaults for absent optional fields.
4. For v1: folds by `incident_id`, last-write-wins.
5. Validates timestamps with `time.RFC3339Nano`. Rejects lines with
   malformed timestamps but logs the offset and continues.
6. Tolerates trailing whitespace before `\n`.

## Writer contract

The agent (the only writer in V0):

1. Holds a single open append handle. Writes are line-oriented and
   `O_APPEND` so concurrent writers (which do not exist in V0 but
   may in V2) cannot interleave bytes within a line.
2. Calls `fsync` after each line on hosts where the operator has
   set `INCIDENTS_FSYNC=1`. (Default off; the cost is real and the
   benefit is recovery from a power loss mid-write.)
3. Does not emit fields it cannot populate. No `null` placeholders
   for optional fields in v0; v1 uses explicit `null` only where the
   schema documents it (`proposed_action`, `approval`, `execution`).
4. Never edits a previously-written line.

## Open questions for v1

These are listed here so they don't get lost; resolution belongs in
ADRs (see [`roadmap.md`](roadmap.md) — ADR-0004 / 0005 / 0006).

- Whether `incident_id` is part of the agent's API surface or only
  internal. (Lean: surfaced — operators will reference it.)
- Whether `backend_errors` should also include success entries
  (`{name, ok: true, at}`) for full-chain visibility, or stay
  failure-only. (Lean: failure-only; success is implicit.)
- Whether `approval.by` is a free string or an enum at v1.
  (Lean: free string until V2 introduces multi-operator semantics.)

## See also

- [`adr/0001-local-only-inference.md`](adr/0001-local-only-inference.md)
  for why `log_lines[].line` content cannot leave operator
  infrastructure.
- [`adr/0002-human-escalation-over-cloud-fallback.md`](adr/0002-human-escalation-over-cloud-fallback.md)
  for the `escalated:true` semantics.
- [`roadmap.md`](roadmap.md) for the V1 milestone shipping
  definition that anchors v1's cutover.
- `agent/brain.go:Incident` — the canonical struct.
