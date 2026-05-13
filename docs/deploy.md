# docs/deploy.md — V0 production deploy walkthrough

> Operator runbook. End-to-end path from "Pi is racked, CX23 is
> provisioned" to "Aceso is writing real `incidents.json` lines from
> real Prometheus alerts." Pairs with
> [`pi-deploy.md`](pi-deploy.md) (Pi-side detail) and
> [`dev-stack.md`](dev-stack.md) (synthetic soak alert).

## What V0 deploy means

Two hosts, two roles:

- **Hetzner CX23** — control plane. Runs the Aceso container (pulled
  from GHCR), reaches Prometheus + Loki on the existing `monitoring`
  Docker network, and reaches the Pi over WireGuard for inference.
- **Raspberry Pi 5 / 16 GB** — compute plane. Runs Ollama with a
  pinned model on the WireGuard IP. No public exposure; the only
  inbound interface is `wg0`.

The Aceso binary is `read-only` (V0 rule 10). It can `GET` Prometheus
and Loki, `POST` to local Ollama, and optionally `POST` to ntfy.sh on
escalation. Nothing else.

## Preconditions

Before you start, confirm each of these. The deploy assumes them and
will not paper over a missing one.

| Item | How to check |
|------|--------------|
| Pi physically up, SSHable over LAN | `ssh pi 'uname -a'` |
| CX23 reachable, Docker installed | `ssh cx23 'docker --version'` |
| Monitoring stack on the CX23 (Prometheus + Loki + Promtail + node-exporter) | `ssh cx23 'docker ps --filter network=monitoring'` should list all four. If the CX23 is fresh and the stack isn't up yet, follow [`monitoring-stack.md`](monitoring-stack.md) first — it creates the `monitoring` Docker network and brings up the four observability services. |
| Hetzner Cloud firewall allows inbound UDP 51820 on the CX23 | Cloud console → CX23 → Firewalls. Source: `0.0.0.0/0`. (WG auth is cryptographic; IP-allowlisting on top is brittle when the Pi's home IP rotates.) |
| WG keypairs generated on operator laptop | `ls ~/aceso-keys/{pi,cx23}.{key,pub}` |
| GHCR image published | `ghcr.io/emil-oestergaard/aceso:latest` — built automatically on push to `main` by `.github/workflows/build.yml`. Confirm at https://github.com/emil-oestergaard/aceso/pkgs/container/aceso |

If any row fails, stop here. None of them have a graceful workaround
inside the deploy path.

## Phase order

| Phase | Where | What | Time |
|-------|-------|------|------|
| A. Pi setup | Pi (LAN SSH) | Hardening + WG + pinned Ollama + benchmark gate | 30-60 min |
| B. CX23 tunnel + agent | CX23 (SSH) | WG up, `.env` written, `docker compose up -d` | 15 min |
| C. First-tick verification | CX23 | Watch the first poll → diagnose → persist cycle | 5 min |
| D. 1-week soak | both | Synthetic alerts; watch for tunnel/memory/model drift | 7 days |

Phases A-C are the same evening's work. Phase D is the patience test
before pointing Aceso at your real Prometheus rules (if you aren't
already; see "Synthetic vs real alerts" below).

## Phase A — Pi setup

Follow [`pi-deploy.md`](pi-deploy.md) Phases 0-2 verbatim. The short
version:

```sh
# On the Pi:
git clone https://github.com/emil-oestergaard/aceso.git
cd aceso
sudo cp scripts/templates/pi-setup.conf.example /etc/aceso-pi.conf
sudo chmod 0600 /etc/aceso-pi.conf
sudo $EDITOR /etc/aceso-pi.conf   # WG_PEER_PUBKEY (= cx23.pub),
                                  # WG_PEER_ENDPOINT (= CX23 public IP:51820),
                                  # OLLAMA_SHA256 (from the Ollama release page)
sudo ./scripts/pi-setup.sh /etc/aceso-pi.conf
```

Exit condition: `/etc/aceso/pi-ready` exists on the Pi and the script
ended on a green benchmark gate. If the gate failed with "warm runs
took >60s", edit `/etc/aceso-pi.conf` to set
`OLLAMA_MODEL=qwen2.5:3b-instruct-q4_K_M` and re-run. The 7B → 3B
fallback is the explicit V0 escape hatch.

**Do not proceed to Phase B until `pi-ready` exists.** A Pi that
booted but did not pass the benchmark gate is a Pi that will time out
every alert in production.

## Phase B — CX23 tunnel + agent

### B.1 — Bring up the tunnel

On the CX23:

```sh
git clone https://github.com/emil-oestergaard/aceso.git /opt/aceso
cd /opt/aceso
sudo cp scripts/templates/cx23-setup.conf.example /etc/aceso-cx23.conf
sudo chmod 0600 /etc/aceso-cx23.conf
sudo $EDITOR /etc/aceso-cx23.conf   # WG_PEER_PUBKEY (= pi.pub)
sudo ./scripts/cx23-setup.sh /etc/aceso-cx23.conf
```

The script ends with a cross-tunnel diagnose smoke test — a real
`POST /api/generate` over WG that the script decodes as `{cause,
suggested_action}`. If you see anything other than
`tunnel verified end-to-end`, **stop**. The deploy will not heal a
broken tunnel; downstream symptoms (every alert escalates) will just
make it harder to diagnose.

After the script: `wg show wg0` should list the Pi peer with a recent
`latest handshake`, and `ping -c3 10.10.0.2` should succeed.

### B.2 — Ensure the monitoring stack is up

If you followed the preconditions, this should already be done. To
double-check:

```sh
docker network inspect monitoring > /dev/null 2>&1 && echo "network: OK" || echo "network: MISSING"
docker ps --filter network=monitoring --format 'table {{.Names}}\t{{.Status}}'
```

You should see the `monitoring` network plus four containers
(`prometheus`, `loki`, `promtail`, `node-exporter`) all `Up`. If
anything is missing, run through [`monitoring-stack.md`](monitoring-stack.md)
before continuing — Aceso needs Prometheus and Loki reachable by
service name on this network.

### B.3 — Write `.env`

```sh
cd /opt/aceso
cp .env.example .env
chmod 0600 .env
$EDITOR .env
```

The minimum diff vs. `.env.example` for production:

```dotenv
# Point at the Pi over WG. This is the whole reason Phase A exists.
OLLAMA_URL=http://10.10.0.2:11434

# Match the model installed in Phase A.
# 7B is the default; switch to 3B only if the Pi benchmark gate failed.
OLLAMA_MODEL=qwen2.5:7b-instruct-q4_K_M

# Your actual Prometheus/Loki service names on the `monitoring` network.
# If they're literally named `prometheus` and `loki`, the defaults work.
PROMETHEUS_URL=http://prometheus:9090
LOKI_URL=http://loki:3100

# Optional but recommended: ntfy push on escalation.
# Pick an unguessable topic suffix — ntfy.sh topics are public.
ESCALATE_NTFY_URL=https://ntfy.sh/aceso-<your-unguessable-suffix>

# 7B model + cold load can be slow. 120 s is conservative.
HTTP_TIMEOUT_SECONDS=120
```

If you want to pin a specific build instead of rolling `:latest`, add:

```dotenv
ACESO_IMAGE=ghcr.io/emil-oestergaard/aceso:sha-<short-sha>
```

The short SHA is the first 7 chars of any commit on `main` — find it
under https://github.com/emil-oestergaard/aceso/pkgs/container/aceso.
Pinning is recommended once you've soaked one build; rolling `:latest`
is fine during initial deploy.

### B.4 — Pull and start

```sh
docker compose pull        # fetches ghcr.io/emil-oestergaard/aceso:latest
docker compose up -d
docker compose ps          # aceso should be "Up"
```

`pull_policy: always` is set in `docker-compose.yml`, so subsequent
`docker compose up -d` calls will fetch newer `:latest` builds without
a manual pull step. The first invocation still benefits from an
explicit `pull` so any auth/network issue surfaces immediately
instead of getting swallowed by `up`.

## Phase C — First-tick verification

The first tick fires within `POLL_INTERVAL_SECONDS` (default 30 s) of
container start. You're verifying three things in order: agent
started, agent reached Prometheus, agent reached the Pi.

### C.1 — Agent started

```sh
docker compose logs --tail 50 aceso
```

Look for, in order:

1. Startup config print (URLs, model, backend chain `[ollama]`).
2. Either a tick log line (`tick: 1 firing alert(s)`) or
   `tick: no firing alerts`.

If you see a Go panic, a `config error`, or
`fallback: all 0 backend(s)`: the .env is wrong. Fix and
`docker compose up -d` again.

### C.2 — Prometheus reachable

If your CX23 has any firing alert right now, the agent will log it.
If not, force one — either by stopping one of the services Prometheus
already alerts on, or by adding the synthetic alert from
[`dev-stack.md`](dev-stack.md) (`config/test_alert.yml`, labelled
`job=aceso-self-test`) to your real Prometheus's rule files
temporarily.

You should see (within 30 s):

```
tick: 1 firing alert(s)
loki: queried for {job="aceso-self-test"} → 0 lines
ollama: POST /api/generate → 200 in 8.4s
persisted: incidents.json +1
```

### C.3 — Pi reachable, diagnosis landed

```sh
docker compose exec aceso cat /data/incidents.json | tail -1 | python3 -m json.tool
```

A success-path incident looks like (field names from
[`incidents-schema.md`](incidents-schema.md)):

```json
{
  "ts": "2026-05-12T22:14:31Z",
  "alert": { "name": "AlwaysFiring", "severity": "warning", "labels": {} },
  "diagnosis": {
    "cause": "synthetic self-test alert; no underlying issue",
    "suggested_action": "remove the test rule when verification is complete"
  }
}
```

A failure-path (escalated) incident looks like:

```json
{
  "ts": "2026-05-12T22:14:31Z",
  "alert": { "name": "AlwaysFiring", "severity": "warning", "labels": {} },
  "escalated": true,
  "error": "fallback: all 1 backend(s) failed: ollama: ..."
}
```

If you see `escalated: true` in the first incident:
- The agent reached Prometheus (good).
- The agent could not reach the Pi over WG, OR Ollama on the Pi
  returned an error.
- Cross-check from the CX23: `curl -m 5 http://10.10.0.2:11434/api/tags`
  should return a JSON tag list. If it times out, the tunnel is
  broken. If it returns an error, the Ollama service is down on the
  Pi — `ssh pi 'sudo systemctl status ollama'`.

Drop the synthetic alert from Prometheus once C.3 is green.

## Phase D — Soak

[`pi-deploy.md`](pi-deploy.md) §"Phase 5: soak" is the authority. The
short version: leave the deploy running for one week against synthetic
alerts before pointing it at real prod alerting rules. Watch the four
soak risks (Pi memory leak, tunnel staleness, model drift, SD-card
write pressure). 24 h is not enough.

When you're ready to flip status.md's "First production deploy" and
"1-week soak" rows from `not started` to `shipped`, that's the same
commit that lands the row update.

## Synthetic vs real alerts

You have two choices during the soak week:

- **Synthetic only**: temporarily mirror `config/test_alert.yml` into
  the real Prometheus's rules dir. The agent diagnoses one always-
  firing alert every 30 s. Use this if your real alerting surface is
  noisy and you don't want the soak signal drowned out.
- **Real prod**: just let the agent see whatever fires naturally. Use
  this if your prod alert volume is low (handful per day) and you'd
  rather measure quality on real signal.

Either is defensible. The doc tracks "1-week soak" generically; what
you fed it during that week goes in your own deploy notes.

## Updating Aceso after deploy

The CX23 pulls a pre-built image, so updates are:

```sh
cd /opt/aceso
git pull                  # picks up any compose / env-example changes
docker compose pull       # fetches the newer :latest from GHCR
docker compose up -d      # recreates the container if the image changed
docker compose logs -f aceso
```

If you pinned `ACESO_IMAGE` to a specific SHA, bump the value in
`.env` first, then `docker compose up -d`. There's no rollback magic;
to roll back, bump it to an older `sha-` tag.

The escape hatch when GHCR / GitHub Actions is unreachable (per
[`adr/004-ghcr-image-publishing.md`](adr/004-ghcr-image-publishing.md)):

```sh
# On the operator's laptop:
docker buildx build --platform linux/amd64 -t aceso:hand ./agent
docker save aceso:hand | ssh cx23 'docker load'

# On the CX23:
sed -i 's|^ACESO_IMAGE=.*|ACESO_IMAGE=aceso:hand|' .env
docker compose up -d
```

This is for a real GHCR outage, not a "I want to skip CI today"
shortcut.

## Where the incident log lives

`/data/incidents.json` inside the container, backed by the
`aceso-data` named Docker volume on the CX23 host. To read from the
host without going through the container:

```sh
docker volume inspect aceso-data --format '{{ .Mountpoint }}'
# typically /var/lib/docker/volumes/aceso_aceso-data/_data
sudo cat /var/lib/docker/volumes/aceso_aceso-data/_data/incidents.json
```

The volume survives `docker compose down`. It does **not** survive
`docker volume rm aceso_aceso-data` — don't run that without a backup.

## Troubleshooting

| Symptom | First thing to check |
|---------|----------------------|
| `aceso` container exits immediately | `docker compose logs aceso` — usually a missing required env var. |
| Logs show `dial tcp prometheus:9090: lookup prometheus on ... no such host` | The container isn't on the `monitoring` network with Prometheus. `docker network inspect monitoring` should list both. |
| Every tick produces `escalated: true` | The Pi or the tunnel. Run `wg show wg0` (CX23 side), then `curl -m 5 http://10.10.0.2:11434/api/tags`. |
| ntfy push not arriving | The ntfy log line is always emitted; check `docker compose logs aceso \| grep '\[escalate\]'` first. If the log shows the push but the phone doesn't, your ntfy topic or app subscription is wrong. |
| Diagnoses look generic / hallucinated | Model + prompt issue, not deploy issue. Soak still counts as green if the agent doesn't crash; quality work is V1. |
| `latest handshake` on `wg show` is > 5 min stale | `PersistentKeepalive=25` should keep this fresh; either the Pi's WG service is down or its outbound UDP 51820 is blocked. SSH the Pi: `sudo systemctl status wg-quick@wg0`. |

## See also

- [`pi-deploy.md`](pi-deploy.md) — Pi-side detail, key rotation,
  rollback semantics, emergency mute.
- [`dev-stack.md`](dev-stack.md) — local stack used for synthetic
  soak alerts.
- [`status.md`](status.md) — flips "First production deploy" and
  "1-week soak" rows when this runbook is exercised end-to-end.
- [`adr/003-plain-wireguard-over-tailscale.md`](adr/003-plain-wireguard-over-tailscale.md) — why plain WG, not Tailscale.
- [`adr/004-ghcr-image-publishing.md`](adr/004-ghcr-image-publishing.md) — why GHCR, and the laptop-build escape hatch.
