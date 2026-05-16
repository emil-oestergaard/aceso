# Aceso

A self-healing AI agent for VPS observability.

Aceso watches your monitoring stack and uses a local LLM to diagnose what
is going wrong. **V0 observes and diagnoses only.** It does not yet take
remediation actions.

## How it works

Every `POLL_INTERVAL_SECONDS` (default 30s) Aceso:

1. Pulls firing alerts from Prometheus (`GET /api/v1/alerts`).
2. For each alert, queries Loki for recent log lines from the affected
   target (`GET /loki/api/v1/query_range`), built from the alert's labels.
3. Builds a prompt describing the alert + logs and asks the local
   Ollama instance for a diagnosis. **Aceso is local-only by design** —
   the binary contains no code paths to third-party LLM APIs (see
   [ADR-001](docs/adr/001-local-only-inference.md)). In production
   the recommended topology is a 16 GB Raspberry Pi running Ollama,
   reached over a plain WireGuard tunnel from the VPS (see
   [ADR-003](docs/adr/003-plain-wireguard-over-tailscale.md) and
   the runbook in [`docs/pi-deploy.md`](docs/pi-deploy.md)); in dev,
   `docker-compose.dev.yml` ships a local Ollama container.
4. **On success:** logs the diagnosis to stdout and appends the full
   incident as a JSON line to `/data/incidents.json`.
5. **On failure (Ollama unreachable / errored):** does NOT invent a
   diagnosis. Instead, escalates to a human via a structured `[escalate]`
   log line and (optionally) an ntfy.sh push, and persists an incident
   with `"escalated": true` so the on-disk log shows what the agent
   could not see at decision time.

The agent is stateless except for `incidents.json` — restart it as often
as you like.

## Repository layout

```
aceso/
├── docker-compose.yml      # production: external "monitoring" network
├── docker-compose.dev.yml  # local stack (prom + loki + promtail + ollama + aceso)
├── agent/
│   ├── Dockerfile          # multi-stage build, static binary, non-root
│   ├── main.go             # entrypoint + polling loop + chain wiring
│   ├── config.go           # env-driven configuration
│   ├── prometheus.go       # /api/v1/alerts client
│   ├── loki.go             # /loki/api/v1/query_range client
│   ├── ollama.go           # /api/generate client
│   ├── backends.go         # Backend interface + OllamaBackend
│   ├── fallback.go         # FallbackChain (tries each backend in order)
│   ├── escalate.go         # Escalator (log line + ntfy.sh push on chain failure)
│   ├── brain.go            # prompt builder + incident persistence + escalation routing
│   └── go.mod
├── config/                 # dev-stack configs (prometheus, loki, promtail)
├── docs/                   # status, INDEX, dev-stack, etc.
└── README.md
```

The `agent/` subfolder isolates Go source from declarative infra
(Prometheus rules, Loki configs, dashboards) that will live alongside it
as the stack grows.

## Configuration

| Variable                | Required | Default                    | Notes                                                                            |
|-------------------------|----------|----------------------------|----------------------------------------------------------------------------------|
| `PROMETHEUS_URL`        | yes      | —                          | Base URL, e.g. `http://prometheus:9090`.                                         |
| `LOKI_URL`              | yes      | —                          | Base URL, e.g. `http://loki:3100`.                                               |
| `OLLAMA_URL`            | yes      | —                          | Base URL, e.g. `http://ollama:11434` or the Pi's WireGuard IP, e.g. `http://10.10.0.2:11434`. |
| `OLLAMA_MODEL`          | no       | `gemma2:2b`                | Any locally-pulled Ollama model.                                                 |
| `BACKEND_ORDER`         | no       | `ollama`                   | Comma-separated chain. V0 only supports `ollama`; unknown names are skipped.     |
| `ESCALATE_NTFY_URL`     | no       | —                          | ntfy.sh topic URL for push alerts when the LLM chain fails. Empty = log only.    |
| `INCIDENTS_PATH`        | no       | `/data/incidents.json`     | NDJSON, one incident per line.                                                   |
| `POLL_INTERVAL_SECONDS` | no       | `30`                       | Cadence of the observe loop.                                                     |
| `HTTP_TIMEOUT_SECONDS`  | no       | `120`                      | Per-call timeout (Ollama can be slow on first generation).                       |

## Running with Docker (production)

The production `docker-compose.yml` pulls a pre-built image from GHCR
(`ghcr.io/emil-oestergaard/aceso`). The image is rebuilt by GitHub
Actions on every push to `main` — see `.github/workflows/build.yml`.
Aceso expects Prometheus, Loki, and Ollama to already be reachable on
a shared external Docker network named `monitoring`.

```sh
# One-time: create the shared network
docker network create monitoring

# Pull and start
docker compose pull
docker compose up -d

# Tail diagnoses
docker compose logs -f aceso

# Inspect persisted incidents
docker compose exec aceso cat /data/incidents.json

# Update to the latest build
docker compose pull && docker compose up -d
```

For production, pin to a specific image SHA so an upstream rebuild
can't change what's running. Set in `.env`:

```sh
ACESO_IMAGE=ghcr.io/emil-oestergaard/aceso:sha-abc1234
```

Rollback is then `docker compose up -d` after editing `.env` back to
the previous SHA.

If your monitoring services live elsewhere, override the URLs in
`.env` (`PROMETHEUS_URL`, `LOKI_URL`, `OLLAMA_URL`).

## Running with Docker (local development)

Use `docker-compose.dev.yml` to bring up a self-contained stack
(Prometheus + Loki + Promtail + Ollama + Aceso) on a private bridge
network. This compose **builds the image from local source**, so
edits to `agent/` are picked up by `--build`:

```sh
docker compose -f docker-compose.dev.yml up --build -d
docker compose -f docker-compose.dev.yml logs -f aceso
```

See [`docs/dev-stack.md`](docs/dev-stack.md) for the full dev flow.

## Running without Docker

```sh
cd agent
export PROMETHEUS_URL=http://localhost:9090
export LOKI_URL=http://localhost:3100
export OLLAMA_URL=http://localhost:11434
go run .
```

## Production deployment (CX23 + Pi)

The V0 production topology is one Hetzner CX23 running Aceso plus a
16 GB Raspberry Pi running Ollama, joined by a plain WireGuard tunnel.
End-to-end provisioning is two scripts:

- `scripts/pi-setup.sh` — runs **on the Pi**, brings up `wg0`,
  installs the pinned Ollama binary, gates on a warm-generation
  benchmark, and stamps `/etc/aceso/pi-ready` on success.
- `scripts/cx23-setup.sh` — runs **on the CX23**, brings up the
  matching `wg0`, then runs a cross-tunnel smoke test that POSTs the
  exact prompt shape Aceso uses to confirm the Pi is reachable, has
  the expected model, and returns valid `{cause, suggested_action}`
  JSON.

Both scripts are designed to run locally on the target box: SSH in
with your operator account (e.g. `deploy@…`), then `sudo ./scripts/...`.
Neither script SSHes out to anywhere — there is no remote-credential
configuration to thread through them. Once the cross-tunnel smoke
test passes, set `OLLAMA_URL=http://10.10.0.2:11434` in the CX23's
`.env` and `docker compose up -d`.

The full runbook — key generation, Hetzner Cloud firewall rules,
soak window, NVMe boot recommendation, no-rollback semantics — lives
in [`docs/pi-deploy.md`](docs/pi-deploy.md). The architectural
rationale lives in [`docs/adr/`](docs/adr/README.md).

## Incident format

`incidents.json` is newline-delimited JSON. Each line is one incident:

```json
{
  "timestamp": "2026-04-29T22:07:00Z",
  "alert": {
    "labels": { "alertname": "HighCPU", "severity": "warning", "instance": "vps01" },
    "annotations": { "summary": "CPU above threshold" },
    "state": "firing",
    "activeAt": "2026-04-29T22:05:30Z",
    "value": "85.5"
  },
  "log_lines": [
    { "timestamp": "2026-04-29T22:06:55Z", "line": "oom-killer triggered", "stream": { "job": "node" } }
  ],
  "diagnosis": {
    "cause": "Worker process is consuming excessive CPU after recent deploy.",
    "suggested_action": "Restart the worker service and roll back the latest release if usage stays high."
  }
}
```

`tail -F /data/incidents.json | jq .` is a perfectly good live view.

## Roadmap

- **V0 (this)** — Observe and diagnose. Read-only.
- **V1** — Action proposals with human-in-the-loop approval.
- **V2** — Bounded autonomous remediation for whitelisted runbooks.

Shipping definitions, non-features, and open ADRs per milestone live
in [`docs/roadmap.md`](docs/roadmap.md).
