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

while [[ $# -gt 0 ]]; do
  case "$1" in
    --mode)  MODE="$2";  shift 2 ;;
    --allow) ALLOW="$2"; shift 2 ;;
    --port)  PORT="$2";  shift 2 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

if [[ $EUID -ne 0 ]]; then
  echo "Must run as root: sudo $0 ..." >&2
  exit 1
fi
if [[ "$MODE" != "server" && "$MODE" != "client" ]]; then
  echo "--mode must be server or client" >&2
  exit 2
fi
if [[ ! -x "./gnl-probe" ]]; then
  echo "Build the binary first, then run this from the directory holding it:" >&2
  echo "  GOOS=linux GOARCH=amd64 go build -o gnl-probe ./cmd/gnl-probe" >&2
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
if [[ -z "$ALLOW" ]]; then
  echo "--allow is required in server mode: a comma-separated list of the public" >&2
  echo "IP addresses permitted to probe this host." >&2
  exit 2
fi

if ! command -v iptables >/dev/null 2>&1; then
  echo "iptables not found; install it first" >&2
  exit 1
fi

# Rebuild a dedicated chain from scratch so re-running never stacks duplicates.
iptables -N GNL_PROBE 2>/dev/null || iptables -F GNL_PROBE
iptables -C INPUT -p udp --dport "$PORT" -j GNL_PROBE 2>/dev/null \
  || iptables -I INPUT 1 -p udp --dport "$PORT" -j GNL_PROBE

IFS=',' read -ra SRCS <<< "$ALLOW"
for src in "${SRCS[@]}"; do
  src="$(echo "$src" | tr -d '[:space:]')"
  [[ -z "$src" ]] && continue
  iptables -A GNL_PROBE -s "$src" -j ACCEPT
  echo "==> allowed $src"
done
iptables -A GNL_PROBE -j DROP
echo "==> everything else to UDP $PORT is dropped"

cat > /etc/systemd/system/gnl-probe.service <<UNIT
[Unit]
Description=GameNoLag P0 measurement echo server
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
