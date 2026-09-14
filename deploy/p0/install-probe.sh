#!/usr/bin/env bash
# Installs gnl-probe on a measurement host and, for landmark hosts, locks the
# echo port down to the addresses that are allowed to measure against it.
#
#   sudo ./install-probe.sh --mode server --allow 203.0.113.5,198.51.100.7
#   sudo ./install-probe.sh --mode client
#
# Idempotent: safe to re-run.
set -euo pipefail

MODE=""
ALLOW=""
PORT="51830"
BIN="/usr/local/bin/gnl-probe"
SRCS=()   # --allow, parsed and validated up front; used by the chain build

need() { # need <flag> <value>
  [[ -n "${2:-}" ]] || { echo "$1 requires a value" >&2; exit 2; }
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --mode)  need --mode  "${2:-}"; MODE="$2";  shift 2 ;;
    --allow) need --allow "${2:-}"; ALLOW="$2"; shift 2 ;;
    --port)  need --port  "${2:-}"; PORT="$2";  shift 2 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

if [[ "$MODE" != "server" && "$MODE" != "client" ]]; then
  echo "--mode must be server or client" >&2
  exit 2
fi
if ! [[ "$PORT" =~ ^[0-9]+$ ]] || (( PORT < 1024 || PORT > 65535 )); then
  # DynamicUser=yes means no CAP_NET_BIND_SERVICE, so a privileged port cannot be
  # bound at all. Refusing here beats a service that fails to start later for a
  # reason this script never mentions.
  echo "--port must be between 1024 and 65535 (got: $PORT)" >&2
  exit 2
fi

# Everything server mode needs, checked before a single byte is written. The
# binary install below is a mutation, and a script that modifies the machine and
# then refuses is not the "safe to re-run" this file claims to be at the top.
if [[ "$MODE" == "server" ]]; then
  if [[ -z "$ALLOW" ]]; then
    echo "--allow is required in server mode: a comma-separated list of the public" >&2
    echo "IP addresses permitted to probe this host." >&2
    exit 2
  fi
  IFS=',' read -ra RAW <<< "$ALLOW"
  for s in "${RAW[@]}"; do
    s="$(echo "$s" | tr -d '[:space:]')"
    [[ -n "$s" ]] && SRCS+=("$s")
  done
  if [[ ${#SRCS[@]} -eq 0 ]]; then
    # A value like "," is non-empty but names no address. Caught here rather than
    # after the ACCEPT loop, because by then the binary is installed and the chain
    # is built - and a DROP-only chain silently refuses every probe for the whole
    # campaign.
    echo "--allow contained no usable address: $ALLOW" >&2
    exit 2
  fi
  if ! command -v iptables >/dev/null 2>&1; then
    echo "iptables not found; install it first" >&2
    exit 1
  fi
  if ! command -v systemctl >/dev/null 2>&1; then
    echo "systemctl not found. This script installs a systemd unit and cannot" >&2
    echo "configure a host without systemd." >&2
    exit 1
  fi
fi
if [[ ! -x "./gnl-probe" ]]; then
  echo "Build the binary first, then run this from the directory holding it:" >&2
  echo "  GOOS=linux GOARCH=amd64 go build -o gnl-probe ./cmd/gnl-probe" >&2
  exit 1
fi

# Root is needed from here down, where the machine actually gets modified.
# Everything above is argument checking, which needs no privilege - so a typo
# reports itself instead of being masked by "must run as root".
if [[ $EUID -ne 0 ]]; then
  echo "Must run as root: sudo $0 ..." >&2
  exit 1
fi

install -m 0755 ./gnl-probe "$BIN"
echo "==> installed $BIN"

if [[ "$MODE" == "client" ]]; then
  echo "==> client mode: nothing else to configure."
  echo "    Add cron entries with the campaign's target list; see docs/p0-runbook.md."
  exit 0
fi

# --- server mode ------------------------------------------------------------
#
# An open UDP echo server is a reflection vector even at a 1:1 ratio: an attacker
# spoofing a victim's source address turns this host into a packet source aimed
# at that victim. The magic-prefix check in the server drops unrelated payloads
# but cannot see a forged source address. So the port is closed by default and
# opened only to the measurement hosts.

# Rebuild a dedicated chain from scratch so re-running never stacks duplicates.
iptables -N GNL_PROBE 2>/dev/null || iptables -F GNL_PROBE
# Re-running with a different --port leaves the previous port's INPUT jump rule
# behind: changing port across runs is not a documented workflow.
iptables -C INPUT -p udp --dport "$PORT" -j GNL_PROBE 2>/dev/null \
  || iptables -I INPUT 1 -p udp --dport "$PORT" -j GNL_PROBE

# SRCS was parsed and validated in the upfront server-mode block: non-empty is
# guaranteed here.
for src in "${SRCS[@]}"; do
  iptables -A GNL_PROBE -s "$src" -j ACCEPT
  echo "==> allowed $src"
done
iptables -A GNL_PROBE -j DROP
echo "==> everything else to UDP $PORT is dropped"

cat > /etc/systemd/system/gnl-probe.service <<UNIT
[Unit]
Description=GameNoLag P0 measurement echo server
Wants=network-online.target
After=network-online.target

[Service]
ExecStart=$BIN server -listen :$PORT
Restart=always
RestartSec=2
DynamicUser=yes
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes

[Install]
WantedBy=multi-user.target
UNIT

systemctl daemon-reload
systemctl enable --now gnl-probe.service
echo "==> gnl-probe.service is running on UDP $PORT"
systemctl --no-pager --lines=5 status gnl-probe.service || true
