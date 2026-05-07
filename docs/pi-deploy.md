# docs/pi-deploy.md — Pi inference plane deployment

> Operator runbook. Pairs with `scripts/pi-setup.sh` and
> `scripts/cx23-setup.sh`. The scripts handle the mechanics; this doc
> covers the decisions, the order of operations, and what's outside the
> scripts (NVMe boot, key rotation, rollback semantics).

## What this deploys

A WireGuard tunnel between a Hetzner CX23 (control plane) and a
Raspberry Pi 5 / 16 GB on a home network (compute plane). The Pi runs
Ollama with a single pinned model. The Aceso agent on the CX23 reaches
the Pi over the tunnel by setting `OLLAMA_URL` to the Pi's tunnel IP.

There is no new backend type. The existing `OllamaBackend` in
`agent/backends.go` works against any reachable Ollama endpoint. Putting
the Pi on the WG IP is purely a deploy-time configuration change.

## Hardware: prefer NVMe, accept SD-card

The default Pi 5 install is fine on a microSD card for V0, but **NVMe
boot via the Pi 5 PCIe HAT is the recommended sustained-service
configuration**. Reasons:

- microSD has poor sustained-write endurance. Ollama logs to
  `/var/log/journal/`; model files (~4 GB) live under `/var/lib/ollama`;
  apt updates churn the rootfs. A 24/7 deployment puts steady write
  pressure on the card and many cards reach degraded write speed within
  6–12 months.
- NVMe gives ~3–10x faster cold-load for the model on Ollama restart.
- An NVMe failure is recoverable (ribbon swap, reformat, re-run
  `pi-setup.sh`); a corrupted SD card can leave the Pi in a state where
  even SSH refuses connections.

`pi-setup.sh` does NOT enforce or check for NVMe boot — the script
works on either. The recommendation is purely operational: if the Pi
will run more than a few months, swap to NVMe early.

**Hardware sanity check:** the script does a *loose* substring match
against `/proc/device-tree/model` for the literal `Raspberry Pi`. Any
Pi model passes (Pi 3, Pi 4, Pi 5, Pi Zero) — only the *type of host*
is checked, not the *generation*. The 7B Q4 model needs ~6 GB resident,
so the operator is responsible for picking a Pi with enough RAM
(recommended: Pi 5 / 16 GB; minimum: Pi 5 / 8 GB with the 3B fallback
model).

## Order of operations

| Phase | Where | Script | Output |
|-------|-------|--------|--------|
| 0. Preflight | operator's laptop | n/a | WG keypairs (`pi.key`/`pi.pub`, `cx23.key`/`cx23.pub`); SSH access to Pi; Hetzner Cloud firewall opens UDP 51820 |
| 1. Pi base | Pi (over LAN SSH) | `pi-setup.sh` Phases 1-2 | hardened SSH (key-only), ufw, `aceso` user, WireGuard `wg0` up |
| 2. Ollama on Pi | Pi | `pi-setup.sh` Phase 3 | `/usr/local/bin/ollama` (SHA256-verified), model pre-pulled, **benchmark gate passed**, `/etc/aceso/pi-ready` stamped |
| 3. CX23 tunnel | CX23 | `cx23-setup.sh` | WireGuard `wg0` up, cross-tunnel smoke test green |
| 4. Agent flip | CX23 | manual edit + restart | `OLLAMA_URL` in `.env` points at Pi tunnel IP; `docker compose restart aceso` |
| 5. Soak | both | observation | one week of synthetic alerts (see "Soak" below) before flipping to real prod |

Phases 0-3 should complete in one evening. Phase 4 is a 5-minute
operation. Phase 5 is the 1-week patience test.

## Phase 0: key generation and Hetzner firewall

On the operator's laptop:

```sh
mkdir -p ~/aceso-keys && cd ~/aceso-keys
wg genkey | tee pi.key   | wg pubkey > pi.pub
wg genkey | tee cx23.key | wg pubkey > cx23.pub
chmod 0600 pi.key cx23.key
```

Transfer the private keys to their hosts (and only their hosts):

```sh
scp pi.key   pi:/root/aceso-keys/pi.key      # over LAN
scp cx23.key cx23:/root/aceso-keys/cx23.key  # over existing SSH
ssh pi   'chmod 0600 /root/aceso-keys/pi.key'
ssh cx23 'chmod 0600 /root/aceso-keys/cx23.key'
```

Public keys can be copied freely (they go into the conf files of the
opposite host).

In the Hetzner Cloud console, add an inbound rule to the CX23's
firewall: **UDP 51820 from any source**. WireGuard's authentication is
cryptographic (peer pubkey + handshake); IP allowlisting on top is
brittle because the Pi's home IP rotates. See the V0 deploy decisions
in `docs/status.md`.

## Phase 1-2: Pi setup

On the Pi:

```sh
git clone https://github.com/<your-fork>/aceso.git
cd aceso
sudo cp scripts/templates/pi-setup.conf.example /etc/aceso-pi.conf
sudo chmod 0600 /etc/aceso-pi.conf
sudo $EDITOR /etc/aceso-pi.conf   # fill in WG_PEER_PUBKEY, WG_PEER_ENDPOINT, OLLAMA_SHA256
sudo ./scripts/pi-setup.sh /etc/aceso-pi.conf
```

`OLLAMA_SHA256` is required and has no default. Get it from
`https://github.com/ollama/ollama/releases/tag/v${OLLAMA_VERSION}` —
look for `ollama-linux-arm64` in `sha256sums.txt`. The script aborts if
the downloaded binary doesn't match.

If the script aborts at the **benchmark gate** ("warm runs took >60s"),
edit `/etc/aceso-pi.conf` to set `OLLAMA_MODEL=qwen2.5:3b-instruct-q4_K_M`
(also update `OLLAMA_SHA256` if you also bumped `OLLAMA_VERSION`) and
re-run. The 7B → 3B fallback is the explicit V0 escape hatch.

After the script finishes, `/etc/aceso/pi-ready` exists and contains
the deployment receipt:

```
ready_at=2026-05-07T22:00:00Z
ollama_version=0.5.7
ollama_model=qwen2.5:7b-instruct-q4_K_M
warm_max_seconds=42
kernel=6.6.31+rpt-rpi-2712
```

## Phase 3: CX23 tunnel + smoke test

On the CX23:

```sh
cd /path/to/aceso  # whatever directory holds the compose file
sudo cp scripts/templates/cx23-setup.conf.example /etc/aceso-cx23.conf
sudo chmod 0600 /etc/aceso-cx23.conf
sudo $EDITOR /etc/aceso-cx23.conf   # fill in WG_PEER_PUBKEY (the Pi's pubkey)
sudo ./scripts/cx23-setup.sh /etc/aceso-cx23.conf
```

The script ends with a green cross-tunnel diagnose. If you see anything
other than "tunnel verified end-to-end", do NOT proceed to Phase 4.

## Phase 4: flip the agent over

```sh
sudo $EDITOR /path/to/aceso/.env
# change: OLLAMA_URL=http://10.10.0.2:11434
docker compose restart aceso
docker compose logs -f aceso
```

The first tick will hit the Pi via the tunnel. Watch for one full
incident landing in `/data/incidents.json` from a synthetic alert
(`docker-compose.dev.yml`'s always-firing test alert is convenient).

### Phase 4: rollback

**There is no graceful rollback path.** This is intentional, not an
oversight.

If the Pi misbehaves after the flip:

- **The agent escalates every failed-diagnose alert** until the Pi is
  fixed. Per CLAUDE.md rule 11, there is no localhost-fallback model on
  the CX23 — silently falling back to a different model would re-create
  the same trust problem the cloud-backend removal was meant to fix.
- The escalation channel (structured `[escalate]` log line + optional
  ntfy.sh push) is what tells you something is wrong.
- The "rollback" is **fix the Pi**, not "point the agent at a different
  inference path."
- If the Pi is genuinely unrecoverable, the operator's only options are
  (a) provision a replacement and re-run `pi-setup.sh`, or (b) accept
  that V0 has no diagnoses until then and rely on the existing alerting
  pipeline below the agent.

If you need a faster recovery path than this, that's a V1 capability
discussion — not a hot-fix decision.

### Phase 4: emergency mute (when "fix the Pi" will take a while)

"Fix the Pi" is the *design* rollback, but during a prolonged outage
(parts ordered, Pi unbootable, you're on a flight) the escalation
volume can itself become a problem — `incidents.json` keeps growing,
ntfy keeps pushing, and the on-call channel turns into noise.

The emergency mute is to stop the agent entirely on the CX23:

```sh
docker compose stop aceso       # silences the agent immediately
docker compose logs --tail 5 aceso   # confirms it's stopped
```

Important properties:

- **Prometheus and AlertManager keep running.** The underlying alert
  pipeline below the agent is unaffected; whatever notifies you about
  alerts today still notifies you. You're only silencing the
  diagnose-and-escalate layer.
- **`incidents.json` stops growing.** The file is preserved; restart
  resumes appending.
- **It's loud silence.** The CX23's process supervisor will not
  re-launch the container. `docker compose ps` shows `Exit 0`. There
  is no way to "accidentally have aceso muted" — anyone running
  `docker compose ps` sees it immediately.

To resume after the Pi is fixed:

```sh
docker compose start aceso
docker compose logs -f aceso    # watch the first tick
```

Do **not** add a flag, env var, or config option that mutes the agent
without stopping it. Same reasoning as the cloud-backend removal:
mute-toggles rot, and a misconfigured mute that survives a restart
would silently disarm the entire observability layer. Use
`docker compose stop` — visible at the supervisor level.

## Phase 5: soak

Before pointing the agent at production Prometheus/Loki, run for **one
week** against the dev stack (`docker-compose.dev.yml` + always-firing
test alert). Watch for:

- **Slow memory leaks on the Pi.** `journalctl -u ollama` should not
  grow into the gigabytes; resident set on the model process should
  stay flat. A leak here would cause the Pi to OOM weekly in prod.
- **Tunnel edge cases.** ISP NAT timeouts, daily route changes,
  WireGuard handshake renegotiations. `wg show wg0 latest-handshakes`
  should never go more than a few minutes stale. If you see hour-long
  gaps, the keepalive isn't working.
- **Model drift.** The same prompt should produce roughly the same
  diagnosis quality across the week. Use `incidents.json` for this —
  pick five recurring synthetic alerts and skim their `cause` fields
  daily.
- **Disk write pressure on SD card** (if not on NVMe). `iostat -x 1`
  should not show sustained MB/s writes — Ollama logs are small but
  apt cron jobs and journald rotation can be surprising.

A clean soak week with no agent crashes, no Pi OOMs, no tunnel staleness
events, and a stable incident-quality profile is the bar for
production. 24h is not enough — slow leaks and ISP-related tunnel
flakiness need time to surface.

## Key rotation

WG keys do not expire by themselves; rotate when:

- A device leaves your control (laptop loss, Pi resale).
- Six months have passed since last rotation (recommend).
- You suspect compromise.

Rotation is symmetric and disruptive (~5 minutes of downtime). On the
operator's laptop:

```sh
cd ~/aceso-keys
mv pi.key pi.key.old; mv pi.pub pi.pub.old
wg genkey | tee pi.key | wg pubkey > pi.pub
chmod 0600 pi.key
```

Then push the new private key to the Pi, update
`/etc/aceso-cx23.conf`'s `WG_PEER_PUBKEY` on the CX23 with the new
`pi.pub`, and re-run both setup scripts. The agent will escalate every
alert that fires during the rotation window — that's the expected
loud-failure behaviour.

## What's committed vs. what isn't

| In the repo | Not in the repo (operator-managed) |
|-------------|------------------------------------|
| `scripts/pi-setup.sh`, `scripts/cx23-setup.sh` | `pi.key`, `cx23.key` (private WG keys) |
| `scripts/templates/wg0-pi.conf.tmpl`, `wg0-cx23.conf.tmpl` | `/etc/wireguard/wg0.conf` (materialised, contains private key) |
| `scripts/templates/ollama.service` | `/etc/aceso-pi.conf`, `/etc/aceso-cx23.conf` (operator config) |
| `scripts/templates/pi-setup.conf.example`, `cx23-setup.conf.example` | The CX23's public IP / endpoint string |
| `docs/pi-deploy.md`, `docs/status.md` (Pi rows) | The agent's `.env` (already gitignored) |

`scripts/*.conf` is gitignored to catch accidental commits of real
operator configs. Only `scripts/templates/*.example` is committed.

## See also

- [`status.md`](status.md) — Pi inference plane capability matrix and
  the V0 escalation contract this deploy depends on.
- [`dev-stack.md`](dev-stack.md) — local Prometheus/Loki/Promtail/Ollama
  stack used for the Phase 5 soak test.
- [`../CLAUDE.md`](../CLAUDE.md) rule 11 — the local-only constraint
  that motivates the no-rollback design.
