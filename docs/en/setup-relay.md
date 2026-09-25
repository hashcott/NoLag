# Setting up a relay

A relay is a VPS that carries players' game traffic to the game servers. It is
contributed by a community member (a *contributor*), who receives a contributor
key in return. Anything the relay forwards is limited to published game address
ranges and rate-capped per player, so it cannot be used as a general proxy.

## Requirements

| | |
|---|---|
| Virtualisation | **KVM**. OpenVZ and LXC cannot create TUN devices; the installer stops if `/dev/net/tun` is missing |
| Kernel | 5.6 or newer (WireGuard is in-kernel from 5.6) |
| OS | A systemd Linux with `apt-get`, `dnf` or `yum` (Debian, Ubuntu, Fedora, RHEL family) |
| Location | Near the game servers: Singapore first, Tokyo second |
| Size | The smallest instance is enough. A game session uses about 10 KB/s |
| Network | A public IPv4 address, and UDP 51820 open in the provider's firewall |
| From the operator | A contributor key `GNL-XXXX-XXXX-XXXX-XXXX` and the control-plane URL |

## 1. Get the installer and the agent

Put `relay-v1.sh` and `gnl-agent` side by side. They come from the repository
(`deploy/relay-v1.sh`) and the CI artifact `gamenolag-linux-amd64`, or from
whatever the operator publishes with `.sha256` files:

```bash
sha256sum -c relay-v1.sh.sha256 gnl-agent.sha256
less relay-v1.sh          # it runs as root: read it first
chmod +x relay-v1.sh gnl-agent
```

To build `gnl-agent` yourself:
`GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o gnl-agent ./cmd/gnl-agent`.

## 2. Install

```bash
sudo ./relay-v1.sh --key GNL-XXXX-XXXX-XXXX-XXXX \
                   --control https://cp.example.com \
                   --region sgp
```

| Option | Default | Meaning |
|---|---|---|
| `--key` | — | Contributor key. Required |
| `--control` | — | Control-plane URL, `https` only. Required |
| `--region` | — | Free-text label shown to operators, e.g. `sgp`, `tyo` |
| `--endpoint IP` | the WAN address | The public address players dial. Needed when the provider NATs the VPS |
| `--port N` | `51820` | WireGuard UDP port |
| `--iface NAME` | `wg0` | WireGuard interface name |
| `--rate RATE` | `64kb/s` | Per-session cap in each direction |
| `--uninstall` | — | Remove everything the script installed |

What it does to the machine:

- Installs `wireguard-tools`, `ipset` and `iptables` if they are missing.
- Creates the WireGuard interface and a private key at `/etc/gnl/relay.key`
  (mode `0600`). The key never leaves the machine.
- Sets `net.ipv4.ip_forward=1` and `rp_filter=2`.
- Adds five iptables rules and one ipset (`gnl-games`).
- Installs `/usr/local/bin/gnl-agent` and two systemd units:
  - `gnl-wg.service` recreates the interface at every boot.
  - `gnl-agent.service` syncs with the control plane every 10 s.

If it stops because the WAN interface has a private address, the provider is
NATing the VPS. Re-run with `--endpoint <public-ip>`. A relay registered with a
private address never receives a handshake, and nothing in its own logs says
why.

## 3. Open the UDP port at the provider

In the provider's control panel, open **UDP 51820** (or your `--port`) in the
security group or firewall for the VPS. The installer cannot check this from
inside the machine, and it is the most common reason a relay looks healthy
locally but is unreachable from everywhere else.

## 4. Check it locally

```bash
systemctl status gnl-wg gnl-agent
wg show                          # interface, port, peers
journalctl -u gnl-agent -n 20    # a sync line every 10 s
iptables -L FORWARD -n -v --line-numbers | head
ipset list gnl-games             # empty until a game profile is published
```

The two `hashlimit` DROP rules must sit **above** the ACCEPT rules in
`FORWARD`; below them they never match. The FORWARD policy must be `DROP`.

Reboot once and check both units again. A WireGuard interface does not survive
a reboot on its own; `gnl-wg` recreates it.

## 5. Verify from outside

From **another machine**, not the relay itself (needs root for the temporary
interface):

```bash
sudo gnl-relaycheck -endpoint <relay-ip>:51820 \
                    -pubkey "$(ssh relay 'sudo wg pubkey < /etc/gnl/relay.key')" \
                    -control https://cp.example.com -key GNL-XXXX-XXXX-XXXX-XXXX
```

- `REACHABLE`: the relay is marked `up`.
- `NOT REACHABLE`: prints the likely causes, the provider's firewall first.

`-control` and `-key` are what record the verdict. Without them you see the
answer, the control plane never learns it, and the relay stays `pending`. The
key proves the report comes from the relay's owner, so nobody can knock a
stranger's relay out of service.

A newly verified relay is offered to players only after its observation window
(48 hours from registration). Re-run the check after anything that could change
the path in: a reboot, a firewall change, a new IP address.

## What protects the contributor

- **Egress allowlist.** Forwarded traffic may only go to the published game
  ranges in `gnl-games`. Everything else is dropped.
- **Rate cap.** 64 KB/s per session per direction, about six times normal
  play. It is a rate cap, not a monthly quota; together with the allowlist it
  keeps total use small.
- **No inbound services.** Only the WireGuard UDP port is used.
- **Revocation is immediate.** Removing a peer on the control plane removes it
  from the kernel within one sync.

## Uninstall

```bash
sudo ./relay-v1.sh --uninstall
```

It removes the units, the interface, the binary, the firewall rules, the ipset
and the sysctl file, and restores the previous FORWARD policy. It deliberately
**keeps `/etc/gnl`**, which holds the relay's private key. Delete it by hand
when you are done (`sudo rm -rf /etc/gnl`), and ask the operator to delete the
relay record.

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| Installer: `/dev/net/tun is missing` | OpenVZ or LXC | Use a KVM VPS |
| Installer: kernel older than 5.6 | Old image | Upgrade the kernel or choose a newer image |
| Installer refuses a private WAN address | Provider NAT | `--endpoint <public-ip>` |
| `gnl-relaycheck`: NOT REACHABLE | Provider firewall closed | Open UDP 51820 at the provider |
| Relay stays `pending` | Check never reported | Re-run `gnl-relaycheck` with `-control` and `-key` |
| Relay goes `down` | No sync for 5 minutes | `systemctl status gnl-agent`, `journalctl -u gnl-agent` |
| `gnl-wg` failed after reboot | `/etc/gnl/relay.state` or `relay.key` missing | Run the installer again |

For the full reasoning behind each step, see the [P1 runbook](../p1-runbook.md).
