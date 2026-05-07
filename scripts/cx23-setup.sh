#!/usr/bin/env bash
# cx23-setup.sh — bring up the CX23 side of the Aceso WireGuard tunnel
# and verify the Pi's Ollama is reachable across it.
#
# Idempotent. Re-running on a CX23 with wg0 already up will rewrite the
# config file and restart wg-quick.
#
# Usage:
#   sudo ./scripts/cx23-setup.sh /etc/aceso-cx23.conf
#
# Run this ONLY after pi-setup.sh has stamped /etc/aceso/pi-ready on the
# Pi. Without that, the cross-tunnel smoke test will fail.

set -euo pipefail

log()  { printf '[cx23-setup] %s\n' "$*" >&2; }
die()  { printf '[cx23-setup] ERROR: %s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "missing required command: $1"; }

[[ "$(id -u)" -eq 0 ]] || die "must run as root (try: sudo $0 ...)"

CONF="${1:-}"
[[ -n "$CONF" && -f "$CONF" ]] || die "usage: $0 <config-file>  (see scripts/templates/cx23-setup.conf.example)"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TEMPLATES_DIR="$SCRIPT_DIR/templates"
[[ -d "$TEMPLATES_DIR" ]] || die "templates dir not found: $TEMPLATES_DIR"

# shellcheck source=/dev/null
source "$CONF"

for var in WG_PRIVKEY_FILE WG_PEER_PUBKEY WG_CX23_ADDRESS WG_CX23_PORT WG_PEER_ALLOWED \
           PI_TEST_URL PI_TEST_MODEL; do
    [[ -n "${!var:-}" ]] || die "config is missing required variable: $var"
done
[[ -r "$WG_PRIVKEY_FILE" ]] || die "WG_PRIVKEY_FILE not readable: $WG_PRIVKEY_FILE"

need apt-get
need systemctl
need install
need sed

# ----------------------------------------------------------------------------
# Install WireGuard tooling. ufw is intentionally NOT touched — the CX23
# already runs Aceso behind whatever firewalling the operator chose for
# the production deployment, and we don't want to surprise them.
# ----------------------------------------------------------------------------

log "installing wireguard tooling"
apt-get update -qq
DEBIAN_FRONTEND=noninteractive apt-get -y -qq install wireguard wireguard-tools curl jq

need curl
need jq

# ----------------------------------------------------------------------------
# Materialise wg0.conf
# ----------------------------------------------------------------------------

log "materialising /etc/wireguard/wg0.conf"

CX23_PRIVKEY="$(cat "$WG_PRIVKEY_FILE")"
[[ -n "$CX23_PRIVKEY" ]] || die "WG_PRIVKEY_FILE is empty: $WG_PRIVKEY_FILE"

WG_TMP="$(mktemp)"
trap 'rm -f "$WG_TMP"' EXIT
sed \
    -e "s|__CX23_PRIVKEY__|${CX23_PRIVKEY}|" \
    -e "s|__CX23_ADDRESS__|${WG_CX23_ADDRESS}|" \
    -e "s|__CX23_PORT__|${WG_CX23_PORT}|" \
    -e "s|__PEER_PUBKEY__|${WG_PEER_PUBKEY}|" \
    -e "s|__PEER_ALLOWED__|${WG_PEER_ALLOWED}|" \
    "$TEMPLATES_DIR/wg0-cx23.conf.tmpl" >"$WG_TMP"
install -m 0600 -o root -g root "$WG_TMP" /etc/wireguard/wg0.conf

# Restart unconditionally so config edits take effect on re-runs.
systemctl enable wg-quick@wg0 >/dev/null
systemctl restart wg-quick@wg0

# ----------------------------------------------------------------------------
# Tunnel verification
# ----------------------------------------------------------------------------

PEER_PING_TARGET="${WG_PEER_ALLOWED%/*}"
log "pinging peer at $PEER_PING_TARGET (10s)..."
if ! ping -c 3 -W 3 "$PEER_PING_TARGET" >/dev/null 2>&1; then
    die "tunnel verification failed: cannot reach $PEER_PING_TARGET — check Hetzner UDP firewall, peer pubkey, and Pi-side wg0 status"
fi
log "tunnel up: $(wg show wg0 latest-handshakes | head -n1)"

# ----------------------------------------------------------------------------
# Cross-tunnel smoke test
# ----------------------------------------------------------------------------
#
# Confirms the Pi's Ollama is reachable, has the expected model, and
# can produce a valid {cause, suggested_action} JSON. This is exactly
# what the agent will do — same prompt shape, same parse expectation —
# so a green run here is a strong signal Phase 4 will work.

log "smoke test: GET ${PI_TEST_URL}/api/tags"
if ! curl -fsS --max-time 10 "${PI_TEST_URL}/api/tags" \
        | jq -e --arg m "$PI_TEST_MODEL" '.models | map(.name) | index($m)' >/dev/null; then
    die "model $PI_TEST_MODEL not found on the Pi — re-run pi-setup.sh on the Pi or update PI_TEST_MODEL"
fi

log "smoke test: POST ${PI_TEST_URL}/api/generate (single diagnose)"
SMOKE_PROMPT='You are Aceso, a Site Reliability AI. Diagnose the alert below and suggest a single concrete remediation action. Respond ONLY with a JSON object of the form {"cause": string, "suggested_action": string}. Do not include any other text, markdown, or commentary.

ALERT
-----
name: HighCPU
severity: warning
state: firing

RECENT LOGS
-----------
[2026-04-29T22:01:00Z] kernel: oom-kill: process 1234 (nginx)

Return the JSON now.'

SMOKE_BODY="$(jq -n --arg model "$PI_TEST_MODEL" --arg prompt "$SMOKE_PROMPT" \
    '{model: $model, prompt: $prompt, stream: false, format: "json", options: {temperature: 0.2}}')"

if ! curl -fsS --max-time 120 -X POST \
        -H 'Content-Type: application/json' \
        -d "$SMOKE_BODY" \
        "${PI_TEST_URL}/api/generate" \
        | jq -e '.done == true and (.response | fromjson | has("cause") and has("suggested_action"))' \
        >/dev/null; then
    die "cross-tunnel smoke test failed — Pi did not return valid {cause, suggested_action} JSON within 120s"
fi

log "tunnel verified end-to-end."
log "Next: set OLLAMA_URL=${PI_TEST_URL} in the agent's .env and restart docker compose."
