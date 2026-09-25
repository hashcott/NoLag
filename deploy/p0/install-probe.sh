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

# valid_ipv4_or_cidr accepts 203.0.113.5 and 203.0.113.0/24 and nothing else.
# iptables -s also resolves hostnames, so an unvalidated typo that happens to
# resolve silently allowlists a stranger, and one that does not resolve fails the
# rule mid-build. Refusing here, before anything is touched, is the only place
# where the answer is still "nothing happened".
valid_ipv4_or_cidr() {
  local addr="$1" prefix
  if [[ "$addr" == */* ]]; then
    prefix="${addr#*/}"
    addr="${addr%%/*}"
    [[ "$prefix" =~ ^[0-9]{1,2}$ ]] || return 1
    (( 10#$prefix <= 32 )) || return 1
  fi
  [[ "$addr" =~ ^[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}$ ]] || return 1
  local IFS=. octet
  for octet in $addr; do
    (( 10#$octet <= 255 )) || return 1
  done
  return 0
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
  for s in "${SRCS[@]}"; do
    if ! valid_ipv4_or_cidr "$s"; then
      echo "--allow entry is not an IPv4 address or CIDR block: $s" >&2
      echo "Hostnames are not accepted: iptables would resolve one, and a typo that" >&2
      echo "happens to resolve allowlists a stranger for the whole campaign." >&2
      exit 2
    fi
  done
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

# The cron jobs write here. /var/lib is root-owned, so a crontab entry running as
# an ordinary user could not create it - and the failure would be one EACCES per
# tick into cron's local mail, which on most bare hosts goes nowhere. Create it
# here, owned by whoever invoked sudo, so both root's crontab and that user's
# crontab work.
RESULTS_DIR="/var/lib/gnl"
mkdir -p "$RESULTS_DIR"
if [[ -n "${SUDO_USER:-}" ]]; then
  chown "$SUDO_USER" "$RESULTS_DIR"
fi
chmod 0755 "$RESULTS_DIR"
echo "==> results directory $RESULTS_DIR ready"

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
# On a re-run the flush briefly empties an already-jumped-to chain; that window is
# one operator command long, unlike the failure modes below, which last the week.
iptables -N GNL_PROBE 2>/dev/null || iptables -F GNL_PROBE

# The terminating DROP goes in FIRST and the ACCEPTs are inserted above it. Built
# the other way round, an -A that fails partway leaves a chain with no terminator:
# it RETURNs, the port is open to the internet, and the only sign is one error
# message that reads like nothing happened.
iptables -A GNL_PROBE -j DROP

# SRCS was parsed and validated as IPv4 addresses or CIDR blocks in the upfront
# server-mode block: non-empty and well-formed are both guaranteed here.
for src in "${SRCS[@]}"; do
  iptables -I GNL_PROBE 1 -s "$src" -j ACCEPT
  echo "==> allowed $src"
done
echo "==> everything else to UDP $PORT is dropped"

# The jump goes in last, so INPUT never points at a half-built chain.
# Re-running with a different --port leaves the previous port's INPUT jump rule
# behind: changing port across runs is not a documented workflow.
iptables -C INPUT -p udp --dport "$PORT" -j GNL_PROBE 2>/dev/null \
  || iptables -I INPUT 1 -p udp --dport "$PORT" -j GNL_PROBE

# gnl-probe binds dual-stack, and everything above is IPv4 only: without this the
# allowlist is trivially bypassed by probing the host's IPv6 address. The probe is
# IPv4-only by design for this campaign, so a blanket DROP is the whole story.
if command -v ip6tables >/dev/null 2>&1; then
  ip6tables -C INPUT -p udp --dport "$PORT" -j DROP 2>/dev/null \
    || ip6tables -I INPUT 1 -p udp --dport "$PORT" -j DROP
  echo "==> IPv6 to UDP $PORT is dropped (the probe is IPv4-only by design)"
else
  echo "WARNING: ip6tables not found. If this host has IPv6, UDP $PORT is reachable" >&2
  echo "         over it and the IPv4 allowlist does not apply." >&2
fi

cat > /etc/systemd/system/gnl-probe.service <<UNIT
[Unit]
Description=GameNoLag P0 measurement echo server
Wants=network-online.target
After=network-online.target
# No start rate limit. The default of 5 starts in 10 seconds turns a short burst
# of restarts into a permanently failed unit, which means a dark landmark and a
# week of 100%-loss records for every client measuring against it.
StartLimitIntervalSec=0

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
# enable, then restart: "enable --now" only starts a unit that is not already
# running, so re-running this script with a fixed binary would leave the old one
# serving while the script reported success.
systemctl enable gnl-probe.service
systemctl restart gnl-probe.service
echo "==> gnl-probe.service is running on UDP $PORT"

# Without this the chain and the INPUT jump are gone after a reboot while the
# unit comes straight back up, leaving the echo server open to the internet -
# the exact outcome the comment above says must not happen. Persistence that
# fails is reported, never assumed, and never fails the install.
if command -v netfilter-persistent >/dev/null 2>&1 && netfilter-persistent save >/dev/null 2>&1; then
  echo "==> firewall rules persisted via netfilter-persistent"
elif [[ -d /etc/sysconfig ]] && iptables-save > /etc/sysconfig/iptables 2>/dev/null; then
  command -v ip6tables-save >/dev/null 2>&1 && ip6tables-save > /etc/sysconfig/ip6tables 2>/dev/null || true
  echo "==> firewall rules saved to /etc/sysconfig/iptables"
else
  echo "WARNING: could not persist the firewall rules on this host." >&2
  echo "         After a reboot the unit restarts but the rules do not, leaving UDP" >&2
  echo "         $PORT open to the internet. Persist them by hand before the campaign:" >&2
  echo "           apt-get install iptables-persistent   # then: netfilter-persistent save" >&2
  echo "         and re-run this script, or arrange your own save/restore." >&2
fi

systemctl --no-pager --lines=5 status gnl-probe.service || true
