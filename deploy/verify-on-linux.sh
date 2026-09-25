#!/usr/bin/env bash
# Verify, on a throwaway Linux VM, everything about the relay installer that has
# never actually been executed.
#
#   sudo ./verify-on-linux.sh --control https://cp.example.com --key GNL-XXXX-XXXX-XXXX-XXXX
#
# Every component below was written and unit-tested on macOS, where none of it
# can run: iptables, ipset, systemd and the WireGuard kernel module are all
# absent there. The Go tests cover the logic; they cannot cover whether the
# syntax is accepted by a real kernel. This script closes that gap in one run.
#
# RUN THIS ON A MACHINE YOU ARE WILLING TO DESTROY. It installs a relay, asserts
# against live kernel state, then uninstalls and asserts again. It is not a dry
# run: it genuinely changes the firewall, the routing configuration and systemd.
set -uo pipefail

CONTROL=""
KEY=""
IFACE="wg0"
PORT="51820"
SETNAME="gnl-games"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --control) CONTROL="$2"; shift 2 ;;
    --key)     KEY="$2";     shift 2 ;;
    --iface)   IFACE="$2";   shift 2 ;;
    --port)    PORT="$2";    shift 2 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

if [[ $EUID -ne 0 ]]; then echo "Must run as root." >&2; exit 1; fi
if [[ -z "$CONTROL" || -z "$KEY" ]]; then
  echo "Both --control and --key are required." >&2
  exit 2
fi
if [[ ! -x ./relay-v1.sh ]]; then
  echo "Run this from the directory holding relay-v1.sh and gnl-agent." >&2
  exit 1
fi

PASS=0; FAIL=0
ok()   { echo "  PASS  $1"; PASS=$((PASS+1)); }
bad()  { echo "  FAIL  $1"; FAIL=$((FAIL+1)); }
check(){ # check <description> <command...>
  local d="$1"; shift
  if "$@" >/dev/null 2>&1; then ok "$d"; else bad "$d"; fi
}

echo "=============================================================="
echo " 1. Install"
echo "=============================================================="
if ! ./relay-v1.sh --key "$KEY" --control "$CONTROL" --iface "$IFACE" --port "$PORT"; then
  echo "!! The installer itself failed. Everything below is moot." >&2
  exit 1
fi

echo
echo "=============================================================="
echo " 2. Kernel state the unit tests could never reach"
echo "=============================================================="
check "wireguard interface exists"            ip link show "$IFACE"
check "interface is up"                       bash -c "ip link show $IFACE | grep -q 'state UNKNOWN\|UP'"
check "wg reports the listen port"            bash -c "wg show $IFACE | grep -q 'listening port: $PORT'"
check "ipset $SETNAME exists"                 ipset list "$SETNAME"
check "FORWARD policy is DROP"                bash -c "iptables -S FORWARD | grep -q '^-P FORWARD DROP'"
check "ip_forward is on"                      bash -c "[[ \$(cat /proc/sys/net/ipv4/ip_forward) == 1 ]]"
check "rp_filter is loose (2)"                bash -c "[[ \$(cat /proc/sys/net/ipv4/conf/all/rp_filter) == 2 ]]"
check "state file written"                    test -r /etc/gnl/relay.state
check "private key is 0600"                   bash -c "[[ \$(stat -c %a /etc/gnl/relay.key) == 600 ]]"

echo
echo "--- the per-session cap: syntax the kernel has never been asked to accept ---"
check "hashlimit upload rule present"         bash -c "iptables -S FORWARD | grep -q 'hashlimit-name gnl-up'"
check "hashlimit download rule present"       bash -c "iptables -S FORWARD | grep -q 'hashlimit-name gnl-down'"
# Ordering is the part that silently does nothing when wrong: a DROP below an
# ACCEPT never matches.
UP_POS=$(iptables -S FORWARD | grep -n 'gnl-up' | cut -d: -f1 | head -1)
ACC_POS=$(iptables -S FORWARD | grep -n 'match-set .* dst -j ACCEPT' | cut -d: -f1 | head -1)
if [[ -n "$UP_POS" && -n "$ACC_POS" && "$UP_POS" -lt "$ACC_POS" ]]; then
  ok "caps sit above the ACCEPT rules"
else
  bad "caps sit above the ACCEPT rules (cap=$UP_POS accept=$ACC_POS) - a cap below an ACCEPT never matches"
fi

echo
echo "--- systemd: two units that have never been parsed by a real systemd ---"
check "gnl-wg.service is active"              systemctl is-active --quiet gnl-wg.service
check "gnl-agent.service is active"           systemctl is-active --quiet gnl-agent.service
check "agent unit requires the wg unit"       bash -c "systemctl show gnl-agent.service -p Requires | grep -q gnl-wg"
check "agent has no start limit"              bash -c "systemctl show gnl-agent.service -p StartLimitIntervalUSec | grep -q '=0\$'"

echo
echo "=============================================================="
echo " 3. Idempotence: re-running must not stack duplicates"
echo "=============================================================="
BEFORE=$(iptables -S | wc -l)
./relay-v1.sh --key "$KEY" --control "$CONTROL" --iface "$IFACE" --port "$PORT" >/dev/null 2>&1
AFTER=$(iptables -S | wc -l)
if [[ "$BEFORE" == "$AFTER" ]]; then
  ok "rule count unchanged after a second install ($BEFORE)"
else
  bad "rule count went $BEFORE -> $AFTER; the -C/-I guard is not matching"
fi

echo
echo "=============================================================="
echo " 4. Reboot survival, without rebooting"
echo "=============================================================="
# A reboot destroys the interface. Simulate exactly that and let systemd recover.
ip link del "$IFACE" 2>/dev/null
systemctl restart gnl-wg.service >/dev/null 2>&1
sleep 2
check "interface recreated by gnl-wg.service"  ip link show "$IFACE"
check "agent still running afterwards"         systemctl is-active --quiet gnl-agent.service

echo
echo "=============================================================="
echo " 5. Uninstall must leave the machine as it was"
echo "=============================================================="
./relay-v1.sh --uninstall --iface "$IFACE" >/dev/null 2>&1
check "interface gone"                        bash -c "! ip link show $IFACE 2>/dev/null"
check "ipset gone"                            bash -c "! ipset list $SETNAME 2>/dev/null"
check "no gnl FORWARD rules left"             bash -c "! iptables -S FORWARD | grep -q 'gnl-\|match-set $SETNAME'"
check "no MASQUERADE left"                    bash -c "! iptables -t nat -S POSTROUTING | grep -q MASQUERADE"
check "FORWARD policy restored (not DROP)"    bash -c "! iptables -S FORWARD | grep -q '^-P FORWARD DROP'"
check "units removed"                         bash -c "! systemctl list-unit-files | grep -q gnl-"
check "binary removed"                        bash -c "! test -e /usr/local/bin/gnl-agent"
check "rp_filter restored"                    bash -c "[[ \$(cat /proc/sys/net/ipv4/conf/all/rp_filter) != 2 ]]"

echo
echo "=============================================================="
printf " %d passed, %d failed\n" "$PASS" "$FAIL"
echo "=============================================================="
if [[ $FAIL -gt 0 ]]; then
  echo
  echo "Do not roll this out to contributors until these are green. Each failure"
  echo "above is something no test on the development machine could have caught,"
  echo "which is exactly why this script exists."
  exit 1
fi
echo
echo "Everything that could only be verified on Linux is verified."
