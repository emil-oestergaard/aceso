# ADR-0003: Plain WireGuard for the Pi inference plane (no Tailscale)

- **Status:** accepted
- **Date:** 2026-05-07
- **Deciders:** Emil Østergaard
- **Supersedes:** —
- **Superseded by:** —
- **Related:** [ADR-0001](0001-local-only-inference.md), [ADR-0002](0002-human-escalation-over-cloud-fallback.md)

## Context

ADR-0001 commits Aceso to local-only inference. In V0 production that
means an Ollama instance on a 16 GB Raspberry Pi, reached from a
Hetzner CX23 over an encrypted tunnel. Two reasonable tunnels were on
the table:

1. **Tailscale.** Coordinated WireGuard with NAT traversal, ACLs,
   key rotation, MagicDNS, and a managed control plane. Would let the
   operator point `OLLAMA_URL=http://pi.tailscale-domain:11434` and
   ignore most of the network plumbing.
2. **Plain WireGuard.** Two `wg-quick` configs, two key pairs, one
   UDP port punched in the Hetzner Cloud firewall, an RFC1918 `/24`
   tying the two ends together.

Tailscale is more convenient. WireGuard is fewer dependencies and
fewer trust boundaries.

## Decision

Use plain WireGuard. Specifically:

- The Pi runs `wg-quick@wg0` initiated outbound to the CX23. The Pi is
  the WG client in the NAT sense (`PersistentKeepalive=25`); the CX23
  is the static endpoint.
- The CX23 runs `wg-quick@wg0` listening on UDP `51820`. The
  Hetzner Cloud firewall opens that single port to `0.0.0.0/0`. WG
  authenticates by key on every packet, and an unauthenticated UDP
  packet on a port that does not respond is indistinguishable from
  closed — opening it to the world is acceptable, and avoids the
  operational drag of an allowlist that breaks every time the Pi's
  ISP renumbers it.
- The Ollama systemd unit binds to `OLLAMA_HOST=10.10.0.2:11434` so
  Ollama is reachable *only* over the WG tunnel, never on a host LAN
  or `0.0.0.0`. Defense in depth: even a misconfigured ufw rule
  cannot expose Ollama to the public internet.
- Provisioning is two shell scripts (`scripts/pi-setup.sh`,
  `scripts/cx23-setup.sh`) plus templates. No Ansible, no Terraform.
  V0 is one Pi and one CX23; configuration management is over-spec'd
  for that fleet size and adds a tool the operator must learn before
  they can debug a tunnel.

## Why not Tailscale

Tailscale would have made the happy path easier. It loses on three
axes that matter for this deployment:

1. **Trust boundary.** Tailscale's coordination server holds the
   metadata about who can reach whom. ADR-0001 was about not handing
   production-log-adjacent traffic to a third party; routing the
   transport for that traffic through a third party's coordinator
   undoes part of that decision. Self-hosting Headscale is possible
   but reintroduces the operational cost we're trying to avoid.
2. **Failure modes.** Tailscale fails in ways that depend on the
   coordinator's availability and ACL state. Plain WG fails in ways
   that depend only on `wg-quick`, the kernel, and one UDP port. The
   second list is shorter, quieter, and easier to reason about
   during an incident.
3. **Stack-fit.** This repo's design discipline (`stdlib-only Go`,
   "no flag-gated cloud paths", "scripts not Ansible") is about
   keeping the surface area small. Adding a managed network layer
   contradicts that posture for a tunnel that serves exactly one
   port between exactly two hosts.

## Consequences

### Positive

- Two boxes, one UDP port, two key pairs. A WireGuard outage is a
  `wg show` away from a diagnosis.
- No third-party SLA in the inference data path.
- The Ollama bind address (`10.10.0.2:11434`) makes "Ollama is
  reachable only via WG" a property of the systemd unit, not of the
  firewall — which means the operator can't accidentally expose it
  by editing ufw rules.
- Provisioning is auditable: `cat scripts/pi-setup.sh` shows
  literally everything that will happen to the box. No state in a
  remote system, no inventory file, no "what does the role
  `ollama_pi` do today."

### Negative

- Manual key rotation. There is no coordination layer to redistribute
  keys; rotating a key requires editing both `wg0.conf` files and
  restarting `wg-quick` on both ends. V0 accepts this — the soak
  window is one week, not one year, and rotation cadence is operator
  choice.
- No NAT traversal magic. If the Pi moves behind a CGNAT-only
  upstream that blocks outbound UDP (rare; some hotel/conference
  networks), the tunnel won't come up. Acceptable for a stationary
  homelab Pi.
- Per-host scaling means O(N²) `[Peer]` blocks if the operator ever
  wants more than two boxes. V0 explicitly does not plan for that;
  if multi-host enters scope, this ADR gets superseded.

### Neutral

- Adding a second Pi later is one new key pair and a `[Peer]` block on
  each side. Manageable up to ~5 hosts; past that, revisit.
- Coral USB Accelerator (V1 stub in `docs/status.md`) does not change
  the network plane — it's a local pre-LLM classifier, still on the
  Pi, still reached over the same tunnel.

## Implementation

- `scripts/pi-setup.sh` — Pi-side: ufw, hardening, pinned Ollama,
  WG up, benchmark gate, `/etc/aceso/pi-ready` stamp
- `scripts/cx23-setup.sh` — CX23-side: WG up, cross-tunnel smoke test
  using the same prompt shape the agent uses
- `scripts/templates/wg0-pi.conf.tmpl` — Pi config (initiator,
  `PersistentKeepalive=25`)
- `scripts/templates/wg0-cx23.conf.tmpl` — CX23 config (listener,
  no `Endpoint` — learned from handshakes)
- `scripts/templates/ollama.service` — `OLLAMA_HOST=10.10.0.2:11434`,
  systemd hardening (`NoNewPrivileges`, `ProtectSystem=strict`, etc.)
- `docs/pi-deploy.md` — operator runbook
- Commit `e5d69e6` (feat: add Pi inference plane deploy scripts)
- Commit `9cf6701` (fix(pi-setup): address V0 audit)
