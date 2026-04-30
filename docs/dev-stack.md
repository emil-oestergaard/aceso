# docs/dev-stack.md — local end-to-end smoke test

> Last updated: 2026-04-30

This file documents `docker-compose.dev.yml` and the four configs under
`config/`. It is the shortest path from a clean clone to "Aceso wrote
its first real `incidents.json` line on my machine."

## What it brings up

| Service | Image | Purpose |
|---------|-------|---------|
| `prometheus` | `prom/prometheus:latest` | Hosts the always-firing `AlwaysFiring` test alert from `config/test_alert.yml`. |
| `loki` | `grafana/loki:latest` | Receives logs from Promtail; queried by Aceso. |
| `promtail` | `grafana/promtail:latest` | Tails `/var/log/*.log` and Docker container stdout/stderr; pushes to Loki. |
| `ollama` | `ollama/ollama:latest` | Local LLM runtime. Default model `gemma2:2b` (~1.6 GB). 2 GiB memory cap. |
| `aceso` | built from `./agent` | Polls Prometheus every 15 s, queries Loki per firing alert, asks Ollama for a diagnosis, appends NDJSON to `/data/incidents.json`. |

All five services join a private bridge network named
`aceso-dev-monitoring`. **No ports are published to the host** —
everything is internal-only. Add `ports:` per service if you need
local UI access.

## Bring it up

```bash
# 1. Build aceso and start the stack.
docker compose -f docker-compose.dev.yml up --build -d

# 2. Pull the LLM into the persisted ollama-data volume (one-off).
docker compose -f docker-compose.dev.yml exec ollama ollama pull gemma2:2b

# 3. Watch Aceso work.
docker compose -f docker-compose.dev.yml logs -f aceso

# 4. Inspect persisted incidents.
docker compose -f docker-compose.dev.yml exec aceso cat /data/incidents.json
```

## What you should see

Within ~15 s of step 1 finishing:

- Prometheus has loaded `test_alert.yml` and `AlwaysFiring` is firing.
- Aceso's first poll picks up the alert, builds a LogQL selector from
  the `job=aceso-self-test` label, queries Loki (which has no logs
  for that job — that is fine), and sends a prompt to Ollama.
- Ollama returns a `{cause, suggested_action}` JSON.
- A new NDJSON line appears in `/data/incidents.json`.

If the model is still loading on the first request, expect a longer
initial latency. The 30 s `HTTP_TIMEOUT_SECONDS` is conservative for
small models; larger models may need a higher value.

## Why the test alert has a `job` label

The user spec for `config/test_alert.yml` requires only `severity` and
`summary`. The committed file also sets `job: aceso-self-test`. This is
intentional: Aceso's LogQL builder (`agent/loki.go`) prefers labels in
the order `{job, instance, container, namespace, pod, service, app}`,
and skips Loki entirely if none are present (the empty-selector skip,
marked `shipped` in `status.md`). Without `job`, the smoke test would
exercise Aceso → Prometheus → Ollama only, leaving the Loki path
untested. With `job`, all four hops are exercised even though no logs
exist for that selector — Loki's empty-result response is the realistic
case the agent will hit in production for any new alert.

## Tear down

```bash
# Stop, keep volumes (fast restart).
docker compose -f docker-compose.dev.yml down

# Stop, nuke all four named volumes (fresh state).
docker compose -f docker-compose.dev.yml down -v
```

Volumes are named `prometheus-data`, `loki-data`, `ollama-data`,
`aceso-data`. The Ollama model cache survives `down` but not `down -v`,
so re-pulling `gemma2:2b` is the price of a clean reset.

## Troubleshooting

| Symptom | Likely cause | Fix |
|---------|--------------|-----|
| `aceso` logs `prometheus: request failed: dial tcp ... connection refused` | Prometheus container not yet healthy. | Wait 5–10 s; it self-heals on the next 15 s tick. |
| `aceso` logs `ollama: response not marked done` | Model still streaming-loading on first request. | Bump `HTTP_TIMEOUT_SECONDS` or pull the model first (step 2 above). |
| `incidents.json` empty after a few minutes | Ollama OOM (2 GiB cap is tight for some models). | Either raise the `memory: 2g` limit in `docker-compose.dev.yml` or pick a smaller `OLLAMA_MODEL`. |
| Promtail logs `permission denied` on `/var/run/docker.sock` | Host Docker socket has restrictive permissions. | Add Promtail's container user to the docker group, or run the dev stack with elevated permissions on the socket bind. |

## Differences from production (`docker-compose.yml`)

- The dev compose **defines** the network; production assumes an
  existing external `monitoring` network created with `docker network
  create monitoring` and joined by services (Prometheus, Loki, Ollama)
  outside this repo.
- The dev compose **builds** Aceso from `./agent`; production tags it
  `aceso:latest` and is intended to be deployed via a registry.
- The dev compose tightens cadences (15 s poll, 30 s HTTP timeout)
  vs. production defaults (30 s poll, 120 s HTTP timeout) so the
  smoke-test loop is visible inside a minute, not five.
- The dev compose pins all log drivers to the same JSON-file rotation
  to keep `docker compose logs` readable.

## Status

Track real-world exercise of this stack in [`status.md`](status.md)
under **Deploy → Local dev stack**. Flip the row to `shipped` the
first time the four hops complete and a real `incidents.json` line is
produced.
