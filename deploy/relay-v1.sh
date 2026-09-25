#!/usr/bin/env bash
# GameNoLag relay installer.
#
#   sudo ./relay-v1.sh --key GNL-XXXX-XXXX-XXXX-XXXX --control https://cp.example.com
#
# What this does to your machine:
#   - installs wireguard-tools, ipset and iptables if missing
#   - creates a WireGuard interface (default wg0) and a private key at
#     /etc/gnl/relay.key, mode 0600, which NEVER leaves this machine
#   - sets net.ipv4.ip_forward=1 and rp_filter=2
#   - adds five iptables rules and one ipset
#   - installs /usr/local/bin/gnl-agent and a systemd unit
#
# Everything it installs is removable with --uninstall.
# Read it before running it. That is why it is short and why it is published.
set -euo pipefail

KEY=""
CONTROL=""
IFACE="wg0"
PORT="51820"
SETNAME="gnl-games"
RATE_LIMIT="64kb/s"   # per session, per direction; kb here is 1024 bytes
RATE_BURST="256kb"    # four seconds at the sustained rate
REGION=""
UNINSTALL=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --key)       KEY="$2";     shift 2 ;;
    --control)   CONTROL="$2"; shift 2 ;;
    --iface)     IFACE="$2";   shift 2 ;;
    --port)      PORT="$2";    shift 2 ;;
    --region)    REGION="$2";  shift 2 ;;
    --endpoint)  ENDPOINT_IP="$2"; shift 2 ;;
    --rate)      RATE_LIMIT="$2"; shift 2 ;;
    --uninstall) UNINSTALL=1;  shift ;;
    -h|--help)   sed -n '2,20p' "$0"; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

# Argument checking needs no privilege, so it runs first. A typo then reports
# itself instead of being masked by "must run as root", and every guard below can
# be exercised by an unprivileged test.
if [[ $UNINSTALL -eq 0 ]]; then
  if [[ -z "$KEY" || -z "$CONTROL" ]]; then
    echo "Both --key and --control are required." >&2
    echo "Get a contributor key from the project, then:" >&2
    echo "  sudo $0 --key GNL-XXXX-XXXX-XXXX-XXXX --control https://cp.example.com" >&2
    exit 2
  fi

fi

# Root is required from here down, where the machine actually gets modified.
if [[ $EUID -ne 0 ]]; then
  echo "Must run as root: sudo $0 ..." >&2
  exit 1
fi

# ---------------------------------------------------------------- uninstall
if [[ $UNINSTALL -eq 1 ]]; then
  # Undo exactly what was installed. The old version deleted a FORWARD rule whose
  # spec did not match the one it had added, so iptables refused it, the failure
  # was swallowed, ipset destroy then failed because the set was still referenced,
  # that was swallowed too - and the script printed "Removed." and exited 0. A
  # contributor who ran the documented removal command was left with a DROP
  # forwarding policy on their own machine, permanently, with nothing to trace it to.
  if [[ -r /etc/gnl/relay.state ]]; then
    # shellcheck source=/dev/null
    . /etc/gnl/relay.state
  else
    echo "!! /etc/gnl/relay.state is missing, so the exact rules this host installed" >&2
    echo "   are unknown. Removing what can be identified; check by hand afterwards:" >&2
    echo "     iptables -S FORWARD; iptables -t nat -S POSTROUTING; ipset list" >&2
  fi

  systemctl disable --now gnl-agent.service 2>/dev/null || true
  systemctl disable --now gnl-wg.service 2>/dev/null || true
  rm -f /etc/systemd/system/gnl-agent.service /etc/systemd/system/gnl-wg.service
  rm -f /usr/local/bin/gnl-wg-up
  systemctl daemon-reload 2>/dev/null || true
  ip link del "$IFACE" 2>/dev/null || true

  if [[ -n "${INNER_SUBNET:-}" ]]; then
    iptables -D FORWARD -s "$INNER_SUBNET" -m hashlimit \
      --hashlimit-above "${RATE_LIMIT:-64kb/s}" --hashlimit-burst "${RATE_BURST:-256kb}" \
      --hashlimit-mode srcip --hashlimit-name gnl-up \
      --hashlimit-htable-expire 60000 -j DROP 2>/dev/null || true
    iptables -D FORWARD -d "$INNER_SUBNET" -m hashlimit \
      --hashlimit-above "${RATE_LIMIT:-64kb/s}" --hashlimit-burst "${RATE_BURST:-256kb}" \
      --hashlimit-mode dstip --hashlimit-name gnl-down \
      --hashlimit-htable-expire 60000 -j DROP 2>/dev/null || true
    iptables -D FORWARD -s "$INNER_SUBNET" -m set --match-set "$SETNAME" dst -j ACCEPT 2>/dev/null || true
    iptables -D FORWARD -d "$INNER_SUBNET" -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT 2>/dev/null || true
    iptables -t nat -D POSTROUTING -s "$INNER_SUBNET" -o "$WAN" -j MASQUERADE 2>/dev/null || true
  fi
  iptables -t mangle -D FORWARD -p tcp --tcp-flags SYN,RST SYN -j TCPMSS --clamp-mss-to-pmtu 2>/dev/null || true
  iptables -D INPUT -p udp --dport "${PORT:-51820}" -j ACCEPT 2>/dev/null || true

  # Put the forwarding policy back. Leaving DROP behind breaks Docker, libvirt, a
  # VPN or any NAT the owner runs themselves, with no sign of what did it.
  iptables -P FORWARD "${PREV_FORWARD_POLICY:-ACCEPT}" 2>/dev/null || true
  command -v ip6tables >/dev/null 2>&1 && { ip6tables -P FORWARD ACCEPT 2>/dev/null || true; }

  ipset destroy "$SETNAME" 2>/dev/null || true

  rm -f /usr/local/bin/gnl-agent /etc/sysctl.d/99-gnl-relay.conf
  # Re-read sysctls, or the loosened rp_filter stays live until a reboot.
  sysctl -q --system 2>/dev/null || true

  if command -v netfilter-persistent >/dev/null 2>&1; then
    netfilter-persistent save >/dev/null 2>&1 || true
  elif [[ -d /etc/sysconfig ]]; then
    iptables-save > /etc/sysconfig/iptables 2>/dev/null || true
  fi

  echo "==> Removed: service, interface, firewall rules, sysctls."
  echo "    FORWARD policy restored to ${PREV_FORWARD_POLICY:-ACCEPT}."
  echo "    Verify:  iptables -S FORWARD; iptables -t nat -S POSTROUTING; ipset list"
  echo "    /etc/gnl still holds this relay's private key; delete it by hand if you"
  echo "    are done:  rm -rf /etc/gnl"
  exit 0
fi

# ---------------------------------------------------------------- preflight
# Every check runs BEFORE anything is changed, and each names the real cause.
echo "==> Preflight"

if [[ ! -e /dev/net/tun ]]; then
  echo "!! /dev/net/tun is missing." >&2
  echo "   This VPS is almost certainly OpenVZ or LXC, which cannot create TUN" >&2
  echo "   devices at all. A KVM-based VPS is required. Nothing has been changed." >&2
  exit 1
fi
echo "    /dev/net/tun present"

KVER="$(uname -r)"
KMAJ="${KVER%%.*}"
KMIN="$(echo "$KVER" | cut -d. -f2 | sed 's/[^0-9].*//')"
if (( KMAJ < 5 || (KMAJ == 5 && KMIN < 6) )); then
  echo "!! Kernel $KVER is older than 5.6, which is where WireGuard entered the" >&2
  echo "   mainline kernel. Upgrade the kernel, or install wireguard-dkms." >&2
  exit 1
fi
echo "    kernel $KVER"

# The per-session cap uses xt_hashlimit. Check it here rather than discovering it
# when the rule is added, half way through configuring the firewall.
if ! modprobe xt_hashlimit 2>/dev/null && ! lsmod | grep -q '^xt_hashlimit'; then
  echo "!! The xt_hashlimit kernel module will not load." >&2
  echo "   It enforces the per-session bandwidth cap that keeps one player from" >&2
  echo "   using up your monthly quota. Install your distro's extra netfilter" >&2
  echo "   modules (Debian/Ubuntu: linux-modules-extra-\$(uname -r)) and re-run." >&2
  exit 1
fi
echo "    xt_hashlimit available"

if ! modprobe wireguard 2>/dev/null && ! lsmod | grep -q '^wireguard'; then
  echo "!! The wireguard kernel module will not load." >&2
  echo "   Try: apt-get install -y wireguard-dkms   (or the equivalent for this distro)" >&2
  exit 1
fi
echo "    wireguard module loaded"

WAN="$(ip -4 route show default | awk '/default/ {print $5; exit}')"
if [[ -z "$WAN" ]]; then
  echo "!! Could not detect the internet-facing interface from the default route." >&2
  exit 1
fi
echo "    WAN interface $WAN"

PUBIP="${ENDPOINT_IP:-$(ip -4 -o addr show dev "$WAN" | awk '{print $4}' | cut -d/ -f1 | head -1)}"
if [[ -z "$PUBIP" ]]; then
  echo "!! No IPv4 address found on $WAN. Pass the public address explicitly:" >&2
  echo "   --endpoint <public-ip>" >&2
  exit 1
fi
# A relay registers the address players will dial. Some providers hand out a
# private address on the interface and NAT it, so the address that is on the
# card is not the address that works - and a relay registered on 10.x is simply
# unreachable, with nothing in the logs to say why.
case "$PUBIP" in
  10.*|127.*|169.254.*|192.168.*|172.1[6-9].*|172.2[0-9].*|172.3[01].*)
    if [[ -z "${ENDPOINT_IP:-}" ]]; then
      echo "!! $WAN carries $PUBIP, which is private or loopback. Players cannot" >&2
      echo "   reach that. If this VPS is behind provider NAT, pass the public" >&2
      echo "   address explicitly:  --endpoint <public-ip>" >&2
      exit 2
    fi
    ;;
esac
echo "    endpoint address: $PUBIP"

# ------------------------------------------------------------- dependencies
echo "==> Installing dependencies"
if command -v apt-get >/dev/null 2>&1; then
  apt-get update -qq
  apt-get install -y -qq wireguard-tools ipset iptables curl >/dev/null
elif command -v dnf >/dev/null 2>&1; then
  dnf install -y -q wireguard-tools ipset iptables curl >/dev/null
elif command -v yum >/dev/null 2>&1; then
  yum install -y -q wireguard-tools ipset iptables curl >/dev/null
else
  for c in wg ipset iptables curl; do
    command -v "$c" >/dev/null 2>&1 || { echo "!! $c is missing and no known package manager was found" >&2; exit 1; }
  done
fi

# --------------------------------------------------------------- relay key
# Generated here and never sent anywhere. The control plane receives only the
# public half, which is all it needs and all it should ever hold.
mkdir -p /etc/gnl
chmod 0700 /etc/gnl
if [[ ! -f /etc/gnl/relay.key ]]; then
  umask 077
  wg genkey > /etc/gnl/relay.key
  echo "==> Generated a new relay private key"
else
  echo "==> Reusing the existing relay private key"
fi
chmod 0600 /etc/gnl/relay.key
PUBKEY="$(wg pubkey < /etc/gnl/relay.key)"
echo "    public key: $PUBKEY"

# ---------------------------------------------------------------- register
echo "==> Registering with $CONTROL"
REG="$(curl -fsS -X POST "$CONTROL/v1/relay/register" \
  -H 'Content-Type: application/json' \
  -d "{\"contributor_key\":\"$KEY\",\"public_key\":\"$PUBKEY\",\"endpoint\":\"$PUBIP:$PORT\",\"region\":\"$REGION\",\"hostname\":\"$(hostname)\"}")" || {
    echo "!! Registration failed. Check --key and that $CONTROL is reachable from here." >&2
    exit 1
  }

jsonfield() { echo "$REG" | sed -n "s/.*\"$1\":\"\([^\"]*\)\".*/\1/p"; }
RELAY_ID="$(jsonfield relay_id)"
RELAY_TOKEN="$(jsonfield relay_token)"
INNER_IP="$(jsonfield inner_ip)"
INNER_SUBNET="$(jsonfield inner_subnet)"

if [[ -z "$RELAY_TOKEN" || -z "$INNER_IP" ]]; then
  echo "!! The control plane's reply was not what was expected:" >&2
  echo "$REG" >&2
  exit 1
fi
echo "    relay $RELAY_ID, inner address $INNER_IP"

umask 077
printf '%s' "$RELAY_TOKEN" > /etc/gnl/relay.token
chmod 0600 /etc/gnl/relay.token

# ------------------------------------------------------------------ sysctl
echo "==> Kernel settings"
cat > /etc/sysctl.d/99-gnl-relay.conf <<'SYSCTL'
net.ipv4.ip_forward = 1
# rp_filter must be 2 (loose), not 1 (strict). The path in and the path out are
# not symmetric across a TUN device, and strict mode silently drops the return
# traffic: the tunnel comes up, packets reach the relay, and nothing comes back.
net.ipv4.conf.all.rp_filter = 2
net.ipv4.conf.default.rp_filter = 2
net.core.rmem_max = 8388608
net.core.wmem_max = 8388608
SYSCTL
sysctl -q --system

# -------------------------------------------------------------- wireguard
echo "==> WireGuard interface $IFACE"
# The interface is brought up by a script rather than inline, because it has to
# happen again on every boot. A WireGuard interface is not persistent: after a
# reboot wg0, its address, its listen port and its key binding are all gone. The
# old version set it up here only, so a rebooted relay came back dead - the agent
# exited on the missing interface, systemd gave up after five restarts, and the
# control plane went on reporting the relay as up. Nobody found out until a
# player did.
cat > /usr/local/bin/gnl-wg-up <<'WGUPEOF'
#!/usr/bin/env bash
# Bring up this relay's WireGuard interface from the state the installer
# recorded. Idempotent: safe to run at every boot and by hand.
set -euo pipefail
[[ -r /etc/gnl/relay.state ]] || { echo "missing /etc/gnl/relay.state; re-run the installer" >&2; exit 1; }
# shellcheck source=/dev/null
. /etc/gnl/relay.state
[[ -r /etc/gnl/relay.key ]] || { echo "missing /etc/gnl/relay.key; re-run the installer" >&2; exit 1; }

ip link show "$IFACE" >/dev/null 2>&1 || ip link add "$IFACE" type wireguard
wg set "$IFACE" listen-port "$PORT" private-key /etc/gnl/relay.key
ip -4 addr show dev "$IFACE" | grep -q "${INNER_IP%%/*}" \
  || ip -4 addr add "$INNER_IP" dev "$IFACE"
ip link set "$IFACE" up
echo "$IFACE up: UDP $PORT, address $INNER_IP"
WGUPEOF
chmod 0755 /usr/local/bin/gnl-wg-up

# --------------------------------------------------------------- firewall
# Deny by default, then allow exactly the game CIDRs. An allowlist is fewer
# rules than a denylist and has no edge cases to forget; the agent fills the
# ipset from the profile the control plane publishes.
echo "==> Firewall"
ipset create "$SETNAME" hash:net family inet -exist

add_rule() { # add_rule <table> <chain> <rule...>
  local table="$1" chain="$2"; shift 2
  iptables -t "$table" -C "$chain" "$@" 2>/dev/null || iptables -t "$table" -I "$chain" 1 "$@"
}

# Remember the policy being replaced, so --uninstall can put it back rather than
# leaving a machine that also runs Docker, libvirt or a VPN unable to forward its
# owner's own traffic.
PREV_FORWARD_POLICY="$(iptables -S FORWARD 2>/dev/null | awk '/^-P FORWARD/{print $3; exit}')"
PREV_FORWARD_POLICY="${PREV_FORWARD_POLICY:-ACCEPT}"
iptables -P FORWARD DROP
# Per-session bandwidth cap, inserted BEFORE the ACCEPTs so excess is dropped
# rather than forwarded. Spec 6.5 promises contributors that one player cannot
# burn their monthly quota; this is that promise.
#
# hashlimit rather than tc classes: it hashes on the inner address itself, so one
# rule covers every peer and there is no per-peer state for the agent to keep in
# step. A real game session runs about 10 KB/s, so the cap sits roughly six times
# above normal use and only bites on abuse.
#
# Note what this is NOT: a rate cap is not a quota cap. Sustained flat-out use
# still moves real volume over a month. What actually bounds the damage is this
# cap together with the game-CIDR allowlist, which means only game traffic can
# flow at all.
add_rule filter FORWARD -s "$INNER_SUBNET" -m hashlimit \
  --hashlimit-above "$RATE_LIMIT" --hashlimit-burst "$RATE_BURST" \
  --hashlimit-mode srcip --hashlimit-name gnl-up \
  --hashlimit-htable-expire 60000 -j DROP
add_rule filter FORWARD -d "$INNER_SUBNET" -m hashlimit \
  --hashlimit-above "$RATE_LIMIT" --hashlimit-burst "$RATE_BURST" \
  --hashlimit-mode dstip --hashlimit-name gnl-down \
  --hashlimit-htable-expire 60000 -j DROP

add_rule filter FORWARD -s "$INNER_SUBNET" -m set --match-set "$SETNAME" dst -j ACCEPT
add_rule filter FORWARD -d "$INNER_SUBNET" -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
add_rule nat POSTROUTING -s "$INNER_SUBNET" -o "$WAN" -j MASQUERADE
# Without MSS clamping, TCP through the tunnel stalls on large packets. Gameplay
# is UDP, but HTTPS shares the same address ranges and is TCP.
add_rule mangle FORWARD -p tcp --tcp-flags SYN,RST SYN -j TCPMSS --clamp-mss-to-pmtu
add_rule filter INPUT -p udp --dport "$PORT" -j ACCEPT

# Record what this install actually applied. Without it --uninstall cannot
# reconstruct the rules it needs to delete: iptables -D requires the exact
# rule-spec, and INNER_SUBNET is only ever known from the registration response.
cat > /etc/gnl/relay.state <<STATEEOF
INNER_SUBNET=$INNER_SUBNET
WAN=$WAN
PORT=$PORT
IFACE=$IFACE
INNER_IP=$INNER_IP
SETNAME=$SETNAME
RATE_LIMIT=$RATE_LIMIT
RATE_BURST=$RATE_BURST
PREV_FORWARD_POLICY=$PREV_FORWARD_POLICY
STATEEOF
chmod 0600 /etc/gnl/relay.state

# The tunnel is IPv4 only. iptables -P FORWARD DROP above governs IPv4 alone, so
# without this the box would forward IPv6 wherever it was asked to - straight
# past the game-CIDR allowlist that is the whole point of the egress policy.
if command -v ip6tables >/dev/null 2>&1; then
  ip6tables -P FORWARD DROP 2>/dev/null || true
  echo "==> IPv6 forwarding disabled (the tunnel is IPv4 only)"
else
  echo "WARNING: ip6tables not found. If this host has IPv6 enabled, traffic can" >&2
  echo "         be forwarded over it without passing the game-CIDR allowlist." >&2
fi

# Persistence that fails is reported, never assumed. Without it a reboot brings
# the box back with ip_forward=1 (which IS persisted below) and FORWARD back at
# its default, usually ACCEPT - an open forwarder on somebody else's address.
if command -v netfilter-persistent >/dev/null 2>&1 && netfilter-persistent save >/dev/null 2>&1; then
  echo "==> firewall rules persisted via netfilter-persistent"
elif [[ -d /etc/sysconfig ]] && iptables-save > /etc/sysconfig/iptables 2>/dev/null; then
  command -v ip6tables-save >/dev/null 2>&1 && ip6tables-save > /etc/sysconfig/ip6tables 2>/dev/null || true
  echo "==> firewall rules persisted to /etc/sysconfig/iptables"
else
  echo "!! Could not persist the firewall rules on this distro." >&2
  echo "   After a reboot this host comes back with IP forwarding ON and the" >&2
  echo "   FORWARD policy back at its default, which on most systems is ACCEPT:" >&2
  echo "   an open forwarder on your IP address. Install iptables-persistent" >&2
  echo "   (apt-get install -y iptables-persistent) and re-run this script, or" >&2
  echo "   save the rules the way your distro expects before rebooting." >&2
fi

# ------------------------------------------------------------------- agent
echo "==> Agent"
if [[ ! -f ./gnl-agent ]]; then
  echo "!! ./gnl-agent is not in this directory." >&2
  echo "   Download it from the release alongside this script, verify its" >&2
  echo "   checksum, and run this again." >&2
  exit 1
fi
install -m 0755 ./gnl-agent /usr/local/bin/gnl-agent

# The interface is owned by its own oneshot unit so it is recreated at every
# boot, before the agent starts. RemainAfterExit keeps it "active" once done, so
# the agent's Requires= is satisfied for the life of the boot.
cat > /etc/systemd/system/gnl-wg.service <<WGUNIT
[Unit]
Description=GameNoLag relay WireGuard interface
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/usr/local/bin/gnl-wg-up
ExecStop=/sbin/ip link del $IFACE

[Install]
WantedBy=multi-user.target
WGUNIT

cat > /etc/systemd/system/gnl-agent.service <<UNIT
[Unit]
Description=GameNoLag relay agent
# The agent cannot do anything without the interface, and the interface does not
# survive a reboot. Requires= makes a failure to create it fail the agent too,
# instead of leaving the agent crash-looping against something that is never
# coming back.
Requires=gnl-wg.service
After=gnl-wg.service network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/gnl-agent -control $CONTROL -iface $IFACE -ipset $SETNAME -state-file /etc/gnl/relay.state
Restart=always
RestartSec=5
# systemd gives up after 5 starts in 10s by default. This service is meant to
# keep trying for as long as the machine is up: a relay that stopped retrying
# because of a transient failure is a relay nobody notices is gone.
StartLimitIntervalSec=0
# Needs root for wgctrl, iptables and ipset; CAP_NET_ADMIN is the capability
# that actually matters. The rest is taken away.
NoNewPrivileges=yes
ProtectHome=yes
PrivateTmp=yes

[Install]
WantedBy=multi-user.target
UNIT

systemctl daemon-reload
systemctl enable gnl-wg.service
systemctl restart gnl-wg.service
systemctl enable gnl-agent.service
# restart, not enable --now: --now leaves an already-running unit alone, so a
# re-run with a fixed agent binary would report success while the old one kept
# running.
systemctl restart gnl-agent.service

echo
echo "==> Done. Relay $RELAY_ID is up."
echo
echo "    One thing this script CANNOT check: your VPS provider's own firewall."
echo "    Open UDP $PORT in the provider's security group or cloud firewall."
echo "    Until that is done the relay looks healthy from here and is"
echo "    unreachable from everywhere else."
echo
echo "    Logs:   journalctl -u gnl-agent -f"
echo "    Peers:  wg show $IFACE"
echo "    Remove: sudo $0 --uninstall"
