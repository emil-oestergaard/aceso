#!/usr/bin/env bash
# pi-setup.sh — provision a Raspberry Pi 5 as the Aceso inference node.
#
# Idempotent. Re-running on an already-provisioned Pi should be a no-op
# (or upgrade in place) rather than reverting to defaults.
#
# Usage:
#   sudo ./scripts/pi-setup.sh /etc/aceso-pi.conf
#
# See scripts/templates/pi-setup.conf.example for the config schema and
# docs/pi-deploy.md for the operator runbook (NVMe boot recommendation,
# rollback behaviour, key rotation, end-to-end test checklist).

set -euo pipefail

# ----------------------------------------------------------------------------
# Phase 0 — preflight
# ----------------------------------------------------------------------------

log()  { printf '[pi-setup] %s\n' "$*" >&2; }
die()  { printf '[pi-setup] ERROR: %s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "missing required command: $1"; }

[[ "$(id -u)" -eq 0 ]] || die "must run as root (try: sudo $0 ...)"

CONF="${1:-}"
[[ -n "$CONF" && -f "$CONF" ]] || die "usage: $0 <config-file>  (see scripts/templates/pi-setup.conf.example)"

# Refuse to run on a non-Pi host. /proc/device-tree/model is set on
# Raspberry Pi OS; absence or wrong content means we are not on the
# expected hardware and shouldn't be rewriting system config files.
MODEL_FILE=/proc/device-tree/model
if [[ ! -r "$MODEL_FILE" ]] || ! grep -q "Raspberry Pi" "$MODEL_FILE"; then
    die "this script is only safe on a Raspberry Pi (model file: $MODEL_FILE)"
fi
log "host: $(tr -d '\0' <"$MODEL_FILE")"

# Resolve the script's own directory so template paths work regardless
# of the operator's current working directory.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TEMPLATES_DIR="$SCRIPT_DIR/templates"
[[ -d "$TEMPLATES_DIR" ]] || die "templates dir not found: $TEMPLATES_DIR"

# shellcheck source=/dev/null
source "$CONF"

# Required vars from the conf — fail loudly on missing values.
for var in WG_PRIVKEY_FILE WG_PEER_PUBKEY WG_PEER_ENDPOINT WG_PI_ADDRESS WG_PEER_ALLOWED \
           OLLAMA_VERSION OLLAMA_SHA256 OLLAMA_MODEL; do
    [[ -n "${!var:-}" ]] || die "config is missing required variable: $var"
done
[[ -r "$WG_PRIVKEY_FILE" ]] || die "WG_PRIVKEY_FILE not readable: $WG_PRIVKEY_FILE"

need apt-get
need systemctl
need install
need sed
need sha256sum

# ----------------------------------------------------------------------------
# Phase 1 — base hardening
# ----------------------------------------------------------------------------

log "Phase 1: base hardening"

apt-get update -qq
DEBIAN_FRONTEND=noninteractive apt-get -y -qq full-upgrade
DEBIAN_FRONTEND=noninteractive apt-get -y -qq install \
    ufw unattended-upgrades wireguard wireguard-tools curl jq

# Unattended security upgrades. Auto-reboot in a quiet window so kernel
# updates don't wedge the model. Unattended-upgrades only applies the
# security pocket — major upgrades stay manual.
cat >/etc/apt/apt.conf.d/52aceso-unattended-upgrades <<'EOF'
APT::Periodic::Update-Package-Lists "1";
APT::Periodic::Unattended-Upgrade "1";
APT::Periodic::AutocleanInterval "7";
Unattended-Upgrade::Automatic-Reboot "true";
Unattended-Upgrade::Automatic-Reboot-Time "03:30";
EOF

# SSH key precondition (per operator instruction): refuse to disable
# password authentication unless at least one user has a non-empty
# authorized_keys. Lockout protection — without this the operator can
# trivially lock themselves out by re-running on a fresh image.
HAS_KEYS=0
for home in /root /home/*; do
    [[ -d "$home" ]] || continue
    keyfile="$home/.ssh/authorized_keys"
    if [[ -s "$keyfile" ]]; then
        HAS_KEYS=1
        log "  found authorized_keys: $keyfile"
    fi
done
[[ "$HAS_KEYS" -eq 1 ]] || die "no user has a non-empty ~/.ssh/authorized_keys; refusing to disable password auth (lockout risk)"

# Drop password auth via a dedicated dropin so the main sshd_config
# stays vendor-managed.
install -m 0644 /dev/stdin /etc/ssh/sshd_config.d/99-aceso.conf <<'EOF'
# Aceso hardening (managed by scripts/pi-setup.sh).
PasswordAuthentication no
PermitRootLogin prohibit-password
KbdInteractiveAuthentication no
EOF
systemctl reload ssh || systemctl reload sshd

# ufw: deny inbound by default, allow SSH from RFC1918 LAN ranges only,
# allow Ollama from the WG peer.
ufw --force reset >/dev/null
ufw default deny incoming
ufw default allow outgoing
# SSH only from local LAN — operator should be on the home network when
# administering the Pi. WireGuard handles remote access otherwise.
ufw allow from 10.0.0.0/8     to any port 22 proto tcp
ufw allow from 172.16.0.0/12  to any port 22 proto tcp
ufw allow from 192.168.0.0/16 to any port 22 proto tcp
# Ollama port, restricted to the WG peer's tunnel address.
PEER_ADDR="${WG_PEER_ALLOWED%/*}"
ufw allow from "$PEER_ADDR" to any port 11434 proto tcp
ufw --force enable

# Unprivileged service user for Ollama. No login shell, no home dir
# writes outside /var/lib/ollama.
if ! id aceso >/dev/null 2>&1; then
    useradd --system --home-dir /var/lib/ollama --shell /usr/sbin/nologin aceso
fi
install -d -o aceso -g aceso -m 0750 /var/lib/ollama
install -d -o aceso -g aceso -m 0750 /var/lib/ollama/models

# ----------------------------------------------------------------------------
# Phase 2 — WireGuard (Pi side)
# ----------------------------------------------------------------------------

log "Phase 2: WireGuard"

PI_PRIVKEY="$(cat "$WG_PRIVKEY_FILE")"
[[ -n "$PI_PRIVKEY" ]] || die "WG_PRIVKEY_FILE is empty: $WG_PRIVKEY_FILE"

WG_TMP="$(mktemp)"
trap 'rm -f "$WG_TMP"' EXIT
# Substitute placeholders. Using `|` as the sed delimiter because the
# values contain `/` (CIDRs) and `:` (endpoints).
sed \
    -e "s|__PI_PRIVKEY__|${PI_PRIVKEY}|" \
    -e "s|__PI_ADDRESS__|${WG_PI_ADDRESS}|" \
    -e "s|__PEER_PUBKEY__|${WG_PEER_PUBKEY}|" \
    -e "s|__PEER_ENDPOINT__|${WG_PEER_ENDPOINT}|" \
    -e "s|__PEER_ALLOWED__|${WG_PEER_ALLOWED}|" \
    "$TEMPLATES_DIR/wg0-pi.conf.tmpl" >"$WG_TMP"
install -m 0600 -o root -g root "$WG_TMP" /etc/wireguard/wg0.conf

systemctl enable --now wg-quick@wg0
# Verification: ping the peer inside the tunnel before declaring Phase 2
# done. If the tunnel is wrong the rest of the script is moot.
PEER_PING_TARGET="${WG_PEER_ALLOWED%/*}"
log "  pinging peer at $PEER_PING_TARGET (10s)..."
if ! ping -c 3 -W 3 "$PEER_PING_TARGET" >/dev/null 2>&1; then
    die "tunnel verification failed: cannot reach $PEER_PING_TARGET — check Hetzner UDP firewall, peer pubkey, and CX23 wg0 status"
fi
log "  tunnel up: $(wg show wg0 latest-handshakes | head -n1)"

# ----------------------------------------------------------------------------
# Phase 3 — Ollama install (pinned binary, checksum-verified)
# ----------------------------------------------------------------------------

log "Phase 3: Ollama install (pinned v${OLLAMA_VERSION})"

OLLAMA_BIN=/usr/local/bin/ollama
OLLAMA_URL="https://github.com/ollama/ollama/releases/download/v${OLLAMA_VERSION}/ollama-linux-arm64"
OLLAMA_TMP="$(mktemp)"
# Append cleanup to the existing trap so both temp files are removed on
# exit, regardless of which step exits.
trap 'rm -f "$WG_TMP" "$OLLAMA_TMP"' EXIT

# Skip the download if the installed version already matches.
INSTALLED_VERSION=""
if [[ -x "$OLLAMA_BIN" ]]; then
    INSTALLED_VERSION="$("$OLLAMA_BIN" --version 2>/dev/null | awk '{print $NF}' || true)"
fi
if [[ "$INSTALLED_VERSION" == "$OLLAMA_VERSION" ]]; then
    log "  ollama v${OLLAMA_VERSION} already installed"
else
    log "  downloading $OLLAMA_URL"
    curl -fsSL --retry 3 -o "$OLLAMA_TMP" "$OLLAMA_URL"
    ACTUAL_SHA="$(sha256sum "$OLLAMA_TMP" | awk '{print $1}')"
    if [[ "$ACTUAL_SHA" != "$OLLAMA_SHA256" ]]; then
        die "checksum mismatch! expected $OLLAMA_SHA256 got $ACTUAL_SHA — aborting before installing untrusted binary"
    fi
    log "  sha256 verified: $ACTUAL_SHA"
    install -m 0755 -o root -g root "$OLLAMA_TMP" "$OLLAMA_BIN"
fi

install -m 0644 -o root -g root "$TEMPLATES_DIR/ollama.service" /etc/systemd/system/ollama.service
systemctl daemon-reload
systemctl enable --now ollama

# Wait for Ollama to bind. /api/tags returns 200 once the server is up,
# even before any model is pulled.
log "  waiting for Ollama HTTP..."
ATTEMPTS=0
until curl -fsS --max-time 2 "http://${WG_PI_ADDRESS%/*}:11434/api/tags" >/dev/null 2>&1; do
    ATTEMPTS=$((ATTEMPTS + 1))
    [[ "$ATTEMPTS" -lt 30 ]] || die "Ollama did not respond within 60s — check 'journalctl -u ollama'"
    sleep 2
done

# Pre-pull the model so the first real diagnose isn't a 4 GB download.
# `ollama pull` is idempotent: it no-ops on an already-pulled tag.
log "  pulling model: $OLLAMA_MODEL"
sudo -u aceso OLLAMA_HOST="${WG_PI_ADDRESS%/*}:11434" \
    "$OLLAMA_BIN" pull "$OLLAMA_MODEL"

# Confirm the model is enumerated.
if ! curl -fsS "http://${WG_PI_ADDRESS%/*}:11434/api/tags" \
        | jq -e --arg m "$OLLAMA_MODEL" '.models | map(.name) | index($m)' >/dev/null; then
    die "model $OLLAMA_MODEL not found after pull"
fi

# ----------------------------------------------------------------------------
# Phase 3b — benchmark gate
# ----------------------------------------------------------------------------
#
# Run three diagnose-shaped prompts back-to-back. Discard the first
# (cold load). Of the remaining two, BOTH must complete in <= 60 s,
# otherwise the model is too slow for our 30 s poll cadence and we
# refuse to declare the Pi ready.

log "Phase 3b: benchmark gate (3 runs, max 60s after warmup)"

BENCH_PROMPT='You are Aceso, a Site Reliability AI. Diagnose the alert below and suggest a single concrete remediation action. Respond ONLY with a JSON object of the form {"cause": string, "suggested_action": string}. Do not include any other text, markdown, or commentary.

ALERT
-----
name: HighCPU
severity: warning
state: firing
current_value: 0.92

RECENT LOGS
-----------
[2026-04-29T22:01:00Z] kernel: oom-kill: process 1234 (nginx)
[2026-04-29T22:02:00Z] systemd: nginx.service: main process exited

Return the JSON now.'

BENCH_BODY="$(jq -n --arg model "$OLLAMA_MODEL" --arg prompt "$BENCH_PROMPT" \
    '{model: $model, prompt: $prompt, stream: false, format: "json", options: {temperature: 0.2}}')"

run_diag() {
    local start end
    start=$(date +%s)
    if ! curl -fsS --max-time 120 -X POST \
            -H 'Content-Type: application/json' \
            -d "$BENCH_BODY" \
            "http://${WG_PI_ADDRESS%/*}:11434/api/generate" \
            | jq -e '.done == true and (.response | fromjson | has("cause") and has("suggested_action"))' \
            >/dev/null; then
        return 1
    fi
    end=$(date +%s)
    echo $((end - start))
}

DURATIONS=()
for i in 1 2 3; do
    log "  run $i..."
    if d="$(run_diag)"; then
        log "    completed in ${d}s"
        DURATIONS+=("$d")
    else
        die "benchmark run $i failed — model did not return valid {cause, suggested_action} JSON"
    fi
done

# Discard the first (cold load). The remaining two must both be <= 60s.
WARM_MAX=0
for d in "${DURATIONS[@]:1}"; do
    [[ "$d" -gt "$WARM_MAX" ]] && WARM_MAX="$d"
done
if [[ "$WARM_MAX" -gt 60 ]]; then
    die "benchmark gate: warm runs took >60s (max ${WARM_MAX}s). Switch OLLAMA_MODEL to qwen2.5:3b-instruct-q4_K_M and re-run."
fi
log "  benchmark passed: warm max ${WARM_MAX}s"

# ----------------------------------------------------------------------------
# Pi-ready receipt
# ----------------------------------------------------------------------------

install -d -o root -g root -m 0755 /etc/aceso
cat >/etc/aceso/pi-ready <<EOF
# Aceso Pi-ready receipt (stamped by scripts/pi-setup.sh)
ready_at=$(date -u +%FT%TZ)
ollama_version=${OLLAMA_VERSION}
ollama_model=${OLLAMA_MODEL}
warm_max_seconds=${WARM_MAX}
kernel=$(uname -r)
EOF
chmod 0644 /etc/aceso/pi-ready

log "Pi is ready: $(tr '\n' ' ' </etc/aceso/pi-ready)"
log "Next: run scripts/cx23-setup.sh on the CX23, then point OLLAMA_URL there."
