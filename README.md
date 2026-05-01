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
3. Builds a prompt describing the alert + logs and asks the configured
   LLM backend chain for a diagnosis. The default chain is
   `ollama → deepseek → gemini`: Ollama is tried first (typically a
   Tailscale-reachable Pi in prod, or local in dev); on failure the
   chain falls through to the free-tier DeepSeek and Gemini APIs.
   Backends without credentials are skipped at startup with a log line.
4. Logs the diagnosis to stdout and appends the full incident as a JSON
   line to `/data/incidents.json`.

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
│   ├── backends.go         # Backend interface + Ollama/DeepSeek/Gemini impls
│   ├── fallback.go         # FallbackChain (tries each backend in order)
│   ├── brain.go            # prompt builder + incident persistence
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
| `OLLAMA_URL`            | yes      | —                          | Base URL, e.g. `http://ollama:11434` or a Tailscale IP for an off-VPS Pi.        |
| `OLLAMA_MODEL`          | no       | `gemma2:2b`                | Any locally-pulled Ollama model.                                                 |
| `BACKEND_ORDER`         | no       | `ollama,deepseek,gemini`   | Comma-separated chain. Backends without creds are skipped at startup.            |
| `DEEPSEEK_API_KEY`      | no       | —                          | Enables DeepSeek (`deepseek-chat`) as a fallback. Free tier is plenty for V0.    |
| `GEMINI_API_KEY`        | no       | —                          | Enables Google AI Studio (`gemini-1.5-flash`) as a fallback.                     |
| `INCIDENTS_PATH`        | no       | `/data/incidents.json`     | NDJSON, one incident per line.                                                   |
| `POLL_INTERVAL_SECONDS` | no       | `30`                       | Cadence of the observe loop.                                                     |
| `HTTP_TIMEOUT_SECONDS`  | no       | `120`                      | Per-call timeout (Ollama can be slow on first generation).                       |

## Running locally with Docker

Aceso expects Prometheus, Loki, and Ollama to already be reachable on a
shared external Docker network named `monitoring`.

```sh
# One-time: create the shared network
docker network create monitoring

# Build and start Aceso
docker compose up --build -d

# Tail diagnoses
docker compose logs -f aceso

# Inspect persisted incidents
docker compose exec aceso cat /data/incidents.json
```

If your monitoring services live elsewhere, override the URLs:

```sh
PROMETHEUS_URL=http://prom.lan:9090 \
LOKI_URL=http://loki.lan:3100 \
OLLAMA_URL=http://ollama.lan:11434 \
docker compose up --build -d
```

## Running without Docker

```sh
cd agent
export PROMETHEUS_URL=http://localhost:9090
export LOKI_URL=http://localhost:3100
export OLLAMA_URL=http://localhost:11434
go run .
```

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
