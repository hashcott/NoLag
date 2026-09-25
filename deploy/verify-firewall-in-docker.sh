#!/usr/bin/env bash
# Run the relay's firewall and allowlist code against a real Linux kernel.
#
#   ./deploy/verify-firewall-in-docker.sh
#
# Everything in internal/agent and internal/ipsetsync was written on macOS, where
# iptables and ipset do not exist. The ordinary tests prove the rules are the
# ones intended; they cannot prove a kernel accepts them, and the failure that
# matters most is invisible from macOS: iptables -C compares the whole spec, so a
# rule the kernel stores differently from how it was given is never found by the
# check and the agent inserts a duplicate on every poll.
#
# This is not the full relay verification. systemd, the WireGuard kernel module
# and a live control plane are not here, so deploy/verify-on-linux.sh on a
# throwaway VM is still the gate before anyone's traffic goes near this. This
# closes the part that can be closed without one.
#
# The container is privileged and its firewall is wrecked by the run. That is
# what it is for; nothing touches the host.
set -euo pipefail

cd "$(dirname "$0")/.."

IMAGE="${IMAGE:-golang:1.25-bookworm}"

exec docker run --rm --privileged \
  -v "$PWD":/src -w /src \
  -e GNL_FIREWALL_TESTS=1 \
  -e GOFLAGS=-buildvcs=false \
  "$IMAGE" sh -euc '
    apt-get update -qq >/dev/null
    apt-get install -y -qq iptables ipset >/dev/null
    iptables --version
    ipset --version
    go test -tags linuxroot -count=1 -v ./internal/agent/ ./internal/ipsetsync/
  '
