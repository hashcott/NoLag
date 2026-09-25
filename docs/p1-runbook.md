# P1 Runbook: bringing up the control plane and the first relay

Prerequisite: `gnl-analyze` from P0 has returned GO, and you know which provider
won. See `docs/superpowers/plans/2026-09-14-p0-route-measurement.md`.

## 1. Control plane

Needs Postgres, a public HTTPS endpoint, and nothing else. It is not on the data
path, so its latency to Vietnam does not matter.

```bash
createdb gamenolag
export GNL_DSN='postgres://user:pass@localhost:5432/gamenolag'
gnl-control -listen 127.0.0.1:8080
```

The schema is applied on start; there is no separate migration step.

Put TLS in front of it — a reverse proxy is fine. **Relay tokens are bearer
tokens.** Over plain HTTP, anyone on the path can take one and impersonate that
relay to the control plane.

## 2. Mint a contributor key

```bash
gnl-control -mint-key
```

Prints the key once. Only its hash is stored, so it cannot be recovered. Give it
to the contributor over a channel you trust.

## 3. The contributor installs the relay

They need a **KVM** VPS with kernel 5.6 or newer. Not OpenVZ and not LXC: those
cannot create TUN devices at all, and the installer stops with that message
rather than failing later somewhere confusing.

```bash
wget https://.../relay-v1.sh https://.../relay-v1.sh.sha256 https://.../gnl-agent
sha256sum -c relay-v1.sh.sha256
less relay-v1.sh                       # 200 lines; read it, it runs as root
sudo ./relay-v1.sh --key GNL-XXXX-XXXX-XXXX-XXXX \
                   --control https://cp.example.com \
                   --region sgp
```

If the installer refuses because the WAN interface carries a private address,
the provider is NATing this VPS and the address on the card is not the address
players dial. Pass the public one explicitly and re-run:

```bash
sudo ./relay-v1.sh --key GNL-XXXX-XXXX-XXXX-XXXX \
                   --control https://cp.example.com \
                   --region sgp \
                   --endpoint <public-ip>
```

Registering a private address is not an error the relay can detect later: it
simply never receives a handshake, and nothing in its own logs says why.

Then, in the VPS provider's control panel, **open UDP 51820 in the security
group**. The installer says this at the end because it is the one thing it
cannot check from inside the machine, and it is the most common reason a relay
looks healthy locally and is unreachable from everywhere else.

The installer leaves two units behind. `gnl-wg.service` recreates the WireGuard
interface and is a oneshot that runs at every boot; `gnl-agent.service` requires
it and does the reconciling. A WireGuard interface does not survive a reboot, so
without the first one a rebooted relay comes back with no interface at all.

Check both after a reboot, because this is the failure that used to go unnoticed
for as long as nobody happened to look:

```bash
systemctl status gnl-wg gnl-agent
wg show                       # the interface, its port and its peers
```

If `gnl-wg` failed, `/etc/gnl/relay.state` or `/etc/gnl/relay.key` is missing and
the relay needs the installer run again. The control plane marks a relay `down`
after five minutes without a sync, so a relay in this state stops being handed to
players rather than silently swallowing their traffic.

### What protects your bandwidth

Each session is capped at **64 KB/s per direction**, enforced on the relay by a
per-address `hashlimit` rule. A real game session uses about 10 KB/s, so the cap
sits roughly six times above normal play and only bites on abuse. Change it with
`--rate` at install time if you want a different ceiling.

Be clear about what this is: a **rate** cap, not a quota. It bounds how fast any
one player can move data, not how much they move in a month. What actually keeps
the total small is the cap together with the egress allowlist — only traffic to
the game address ranges is forwarded at all, so there is nothing else to use the
relay for.

Check the cap is live and see what it has dropped:

```bash
iptables -L FORWARD -n -v --line-numbers | head
```

The two `hashlimit` DROP rules must appear **above** the ACCEPT rules. Below
them they would never match, and the cap would be present but inert.

## 4. Verify from outside

From a different machine — not the relay:

```bash
sudo gnl-relaycheck -endpoint <relay-ip>:51820 -pubkey <relay public key> \
                    -control https://cp.example.com --key GNL-XXXX-XXXX-XXXX-XXXX
```

The public key is printed by the installer, and on the relay is:

```bash
wg pubkey < /etc/gnl/relay.key
```

`REACHABLE` means the relay is genuinely serviceable. `NOT REACHABLE` prints the
causes in order of likelihood, starting with the provider's security group.

**`-control` and `--key` are what make the result count.** Without them the check
tells you the answer and the control plane never learns it — and a relay it has
not verified stays `pending` and is never offered to players, no matter how
faithfully its agent syncs.

That is deliberate. A sync only proves the relay can reach the control plane. It
proves nothing about whether a player can reach the relay, because the provider's
security group sits in front of the UDP port and is invisible from inside the
machine — which is exactly the most common reason a relay looks healthy locally
and is unreachable from everywhere else. Only this check settles it.

The key authenticates the report as the contributor who owns that relay, so
nobody can mark a stranger's relay unreachable and take it out of service.

A relay's status therefore reads:

| status | meaning |
|---|---|
| `pending` | registered, never verified from outside. Not offered to players |
| `up` | verified reachable, and still syncing |
| `unreachable` | an external check failed; the reason is stored alongside it |
| `down` | stopped syncing for five minutes |

Re-run this check after anything that could change the network path — a reboot,
a provider firewall change, a new IP address.


## 5. Bind a test device and watch it arrive

There is no client yet — that is P2 — so bind a peer by hand to prove the loop
works end to end. On the control plane's database:

```sql
INSERT INTO device (id, key_hash, wg_pubkey)
VALUES ('test-device-1', '<hash of the contributor key>', '<a WireGuard public key>');

INSERT INTO peer_binding (device_id, relay_id, inner_ip)
VALUES ('test-device-1', '<relay id>', '10.77.0.5/32');
```

Generate that public key anywhere with `wg genkey | wg pubkey`.

Within one poll interval — ten seconds — the peer appears on the relay:

```bash
wg show wg0
journalctl -u gnl-agent -n 20
```

The log line reads `agent: peers +1 ~0 -0 (now 1)`.

Delete the `peer_binding` row and it disappears just as fast. That is revocation:
no token expiry to wait out, no key rotation, the peer is simply gone from the
kernel.

## 6. Publish a game profile

Until a profile exists, the allowlist is empty and the relay forwards nothing.
That is the correct state, not a bug — a relay with no profile should carry no
traffic.

```sql
INSERT INTO game_profile (version, cidrs)
VALUES (1, ARRAY['20.24.48.0/20', '52.139.208.0/20']);
```

On the relay, within one poll:

```bash
ipset list gnl-games
```

The real profile comes from P4. These two ranges are illustrative and are enough
to prove the plumbing.

## What is deliberately not here

- **Client.** P2. Until then, peers are bound by hand as in step 5.
- **Automatic relay verification.** `gnl-relaycheck` is run by a person. Wiring
  it into the control plane on a schedule is worth doing once there are more
  relays than one person wants to check.
- **Contributor dashboard.** P6.
- **`trusted_after` enforcement.** The column is populated at registration, but
  nothing reads it yet because there is no client fetching a relay list. That
  lands with `/v1/session` in P3.
