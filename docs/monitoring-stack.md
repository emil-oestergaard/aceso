# docs/monitoring-stack.md — CX23 monitoring stack

> Operator runbook for the Prometheus + Loki + Promtail + node-exporter
> stack that runs on the Hetzner CX23. This is the observability surface
> Aceso reads from. **Stand it up *before* `docker compose up -d` in the
> repo root** — Aceso has nothing to do without it.

## What this stack is

Aceso is a diagnoser. It doesn't *generate* alerts and it doesn't
*collect* logs — it reacts to alerts somebody else has already fired and
queries logs somebody else has already archived. On the CX23, those
"somebodies" are the four containers this compose brings up:

| Service | What it does | Port (inside `monitoring` network) |
|---------|--------------|------------------------------------|
| `prometheus` | Polls metrics endpoints every 15 s; evaluates alert rules; exposes a list of *firing* alerts at `/api/v1/alerts`. Aceso reads from this list. | `9090` |
| `loki` | Stores log lines pushed by Promtail. Queryable via `/loki/api/v1/query_range`. Aceso reads from this when building the diagnosis prompt. | `3100` |
| `promtail` | Tails `/var/log/*.log` on the CX23 host *and* every Docker container's `stdout`/`stderr` via the host socket. Ships everything to Loki. | `9080` (internal only) |
| `node-exporter` | Exposes host metrics (CPU, RAM, disk, network, filesystem) so Prometheus has something real to scrape. | `9100` |

Plus one rule file (`test_alert.yml`) that defines a synthetic always-
firing alert so Aceso's first poll produces a deterministic incident —
useful for verification before any real alerts have been written.

## Why it's separate from `docker-compose.yml`

The repo has three composes now:

| Compose | Purpose | Brings up |
|---------|---------|-----------|
| `docker-compose.dev.yml` | Local dev smoke test on your laptop | Everything in one network, builds Aceso from source, includes Ollama. |
| `monitoring/docker-compose.yml` | **CX23 observability stack** | Prometheus + Loki + Promtail + node-exporter. *Creates* the shared `monitoring` Docker network. |
| `docker-compose.yml` | CX23 Aceso agent | Aceso alone, pulled from GHCR, joining the `monitoring` network as external. |

Separation buys you three things:

1. **Independent lifecycles**: you can `docker compose restart aceso` to
   bump the agent without bouncing Prometheus/Loki (and re-shipping every
   container's logs from scratch). And vice versa: you can restart the
   monitoring stack without taking Aceso down.
2. **Independent ownership**: the monitoring stack is operator infra
   that should outlive any one app. Aceso is one consumer of it; a
   future app on the same CX23 can join `monitoring` and be observed
   too, without touching this stack.
3. **Clean shutdown**: `cd monitoring && docker compose down` cleans up
   exactly the observability containers, not Aceso.

## Bring it up

On the CX23, **before** starting Aceso:

```sh
# Clone the repo if you haven't already.
cd /opt
sudo git clone https://github.com/emil-oestergaard/aceso.git
cd aceso/monitoring

# Bring up all four services + create the `monitoring` Docker network.
sudo docker compose up -d

# Sanity check: all four Up.
sudo docker compose ps

# Confirm the network was created with the bare name "monitoring"
# (not "monitoring_monitoring" or similar).
sudo docker network ls | grep monitoring
```

You should see exactly one network named `monitoring` and four
containers named `prometheus`, `loki`, `promtail`, `node-exporter`, all
in `Up` state.

## Verify the stack works (before adding Aceso)

These three checks confirm Prometheus and Loki are actually doing their
job. Run them from the CX23 host.

### 1. Prometheus is up and the synthetic alert is firing

```sh
sudo docker exec prometheus wget -qO- http://localhost:9090/api/v1/alerts \
  | python3 -m json.tool
```

Expected: a JSON object with `data.alerts` containing one entry,
`AlwaysFiring`, with `state: "firing"`. If `data.alerts` is empty,
Prometheus didn't load the rule file — check
`sudo docker compose logs prometheus | tail -50` for parse errors.

### 2. node-exporter metrics are being scraped

```sh
sudo docker exec prometheus wget -qO- \
  'http://localhost:9090/api/v1/query?query=up{job="node-exporter"}' \
  | python3 -m json.tool
```

Expected: a `data.result[0].value` array where the second element is
`"1"` — meaning Prometheus successfully scraped node-exporter on the
last tick. If it's `"0"` or `data.result` is empty, check
`sudo docker compose logs node-exporter`.

### 3. Loki has logs in it

Promtail will start pushing logs within ~10 s of bring-up. Confirm:

```sh
sudo docker exec loki wget -qO- \
  'http://localhost:3100/loki/api/v1/label/container/values' \
  | python3 -m json.tool
```

Expected: a JSON object with `data` listing several container names
(`prometheus`, `loki`, `promtail`, `node-exporter`). If `data` is empty,
Promtail isn't shipping — likely a permission issue on
`/var/run/docker.sock`; check `sudo docker compose logs promtail`.

All three green = monitoring stack is producing real data. Now proceed
to [`deploy.md`](deploy.md) Phase B.3 (write `.env`) and Phase B.4
(start Aceso).

## After first-tick verification: disable the synthetic alert

The synthetic alert in `test_alert.yml` is for verification only. In
steady-state operation, you don't want Aceso reacting to a permanently-
firing fake alert every 30 seconds — that burns prompt tokens on a
known-meaningless input.

Once `deploy.md` Phase C is green (you've seen at least one real
incident written to `/data/incidents.json`):

```sh
cd /opt/aceso/monitoring
sudo mv test_alert.yml test_alert.yml.disabled
sudo docker compose restart prometheus
```

After the restart, `/api/v1/alerts` should show zero firing alerts (or
only your real ones, if you've added any). Aceso's logs will switch to
`tick: no firing alerts` until something real breaks.

## Where data lives

Three named Docker volumes survive `docker compose down` and need
explicit `docker volume rm` to remove:

| Volume | Contains | Survives `down` | Survives `down -v` |
|--------|----------|-----------------|--------------------|
| `monitoring_prometheus-data` | TSDB blocks (30-day retention) | yes | no |
| `monitoring_loki-data` | Log chunks + index (30-day retention) | yes | no |
| `monitoring_promtail-positions` | `positions.yaml` (file offsets) | yes | no |

To inspect from the host:

```sh
sudo docker volume inspect monitoring_loki-data \
  --format '{{ .Mountpoint }}'
# typically: /var/lib/docker/volumes/monitoring_loki-data/_data
```

Don't run `docker volume rm` without a deliberate reason. Losing
`prometheus-data` means losing 30 days of metrics history; losing
`loki-data` means losing 30 days of logs.

## Tear down

```sh
# Stop, keep volumes (fast restart, history preserved).
cd /opt/aceso/monitoring
sudo docker compose down

# Stop, NUKE all volumes (fresh state — last resort).
sudo docker compose down -v
```

If Aceso is still running, `down` will leave it without anything to
poll; its incidents will then all `escalate: true` until you bring the
stack back up.

## Troubleshooting

| Symptom | First thing to check |
|---------|----------------------|
| `prometheus` exits immediately | `docker compose logs prometheus`. Usually a YAML parse error in `prometheus.yml` or `test_alert.yml`. |
| `loki` logs `failed to write chunk` | Disk full on CX23. `df -h /var/lib/docker`. |
| Promtail logs `permission denied` on `/var/run/docker.sock` | Host socket has restrictive permissions. Either run Promtail with elevated permissions on the socket bind or add the Promtail container user to the docker group. |
| `node-exporter` shows `up == 0` from Prometheus's perspective | Either node-exporter isn't running (`docker compose ps`) or there's a name resolution issue on the `monitoring` network (`docker network inspect monitoring`). |
| Aceso logs `dial tcp prometheus:9090: lookup prometheus on ... no such host` | Aceso container isn't on the `monitoring` network. Confirm with `docker network inspect monitoring` — Aceso should be in the `Containers` list. |
| Stack works but `incidents.json` is empty | Aceso can reach Prometheus, but Prometheus has no firing alerts. Either the synthetic alert was disabled too early, or there's a config error preventing rules from loading. |

## See also

- [`deploy.md`](deploy.md) — full V0 deploy walkthrough that this stack
  is a precondition for.
- [`dev-stack.md`](dev-stack.md) — local dev equivalent with everything
  in one compose.
- [`incidents-schema.md`](incidents-schema.md) — shape of the records
  Aceso writes once it has alerts to react to.
- [`status.md`](status.md) — flips "CX23 monitoring stack" to `shipped`
  when this stack runs in production for the first time.
