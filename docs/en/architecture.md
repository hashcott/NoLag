# Architecture

GameNoLag has three parts: a **client** on the player's Windows PC, **relays**
on VPSes contributed by the community, and one **control plane** that knows who
may use which relay. Game traffic goes client → relay → game server. The control
plane is never on that path.

```
 Player's PC (Windows)                     Contributed VPS                Game servers
┌─────────────────────────────┐          ┌──────────────────┐        ┌──────────────┐
│ gnl-ui  (tray + window,     │          │ WireGuard (wg0)  │        │ AWS / Azure  │
│          no privilege)      │          │ gnl-agent        │        │ Singapore,   │
│    │ named pipe, 4 verbs    │  UDP     │  - peers         │        │ Tokyo        │
│    ▼                        │ ═══════► │  - egress        │ ─────► │              │
│ gnl-service (LocalSystem)   │ WireGuard│    allowlist     │        │              │
│  - measures every relay     │          │  - rate limit    │        │              │
│  - routes only game ranges  │          └────────┬─────────┘        └──────────────┘
└────────────┬────────────────┘                   │ sync every 10 s
             │ HTTPS: session, profile            │
             ▼                                    ▼
        ┌──────────────────────────────────────────────┐
        │ gnl-control + Postgres                       │
        │ keys, devices, relays, published game ranges │
        └──────────────────────────────────────────────┘
```

## Actors

| Actor | Has | Does |
|---|---|---|
| **Operator** | The control plane and its database | Mints contributor keys, verifies relays, publishes game profiles |
| **Contributor** | A KVM VPS and a contributor key | Runs a relay; uses the key on up to three of their own PCs |
| **Player** | A Windows PC with the client | Plays; the client does the rest |

In the current phase, every player is a contributor: the key that registered a
relay is the key that activates devices.

## Components

| Binary | Runs on | Role |
|---|---|---|
| `gnl-control` | Linux, behind TLS | HTTP API and Postgres store: keys, devices, relays, peer bindings, game profiles |
| `gnl-agent` | Each relay | Every 10 s: reports status, receives peers and game ranges, reconciles WireGuard, ipset and iptables |
| `gnl-relaycheck` | Any machine outside the relay | Performs a real WireGuard handshake from the internet. The only thing that marks a relay `up` |
| `gnl-profile` | Operator's machine | Builds a game's CIDR list from contributor observations cross-checked against published cloud ranges |
| `gnl-service` | Windows, LocalSystem | Holds the tunnel: session, measurement, routes, game detection, failover |
| `gnl-ui` | Windows, as the user | Tray icon and window. Unprivileged |
| `gnl-probe`, `gnl-analyze` | Measurement hosts | The P0 campaign that decides whether any VPS path beats the ISP at peak |

## Control-plane API

All endpoints are JSON over HTTPS. Credentials are bearer tokens.

| Endpoint | Caller | Purpose |
|---|---|---|
| `POST /v1/relay/register` | Relay installer, with a contributor key | Registers a relay; returns its id, token and inner subnet |
| `POST /v1/relay/sync` | `gnl-agent`, with its relay token | Reports status; receives peers, game CIDRs and poll interval |
| `POST /v1/relay/reachability` | `gnl-relaycheck`, with the owner's key | Records whether the relay was reachable from outside |
| `POST /v1/activate` | Client, with a contributor key | Binds this device's public key to a slot |
| `GET /v1/session` | Client | Relays offered to this device: `up`, past `trusted_after` |
| `GET /v1/profile` | Client | The newest published game CIDR list |
| `DELETE /v1/devices/{id}` | Contributor | Frees a device slot |
| `POST /v1/observations` | Contributor tooling | Destination addresses seen carrying a game's traffic |
| `GET /healthz` | Monitoring | Liveness |

Key-consuming endpoints are rate-limited to 20 requests an hour per source
address and per key prefix.

## Relay lifecycle

```
register ──► pending ──(gnl-relaycheck: reachable)──► up ──(no sync for 5 min)──► down
                │                                     │                           │
                └──(gnl-relaycheck: not reachable)──► unreachable ◄───────────────┘
                                                      (a later check can move it back to up)
```

A relay is offered to players only when it is `up` **and** its `trusted_after`
time has passed. A sync proves the relay can reach the control plane. It does
not prove a player can reach the relay: the provider's security group sits in
front of the UDP port and cannot be seen from inside the machine. Only an
external handshake settles that.

## A connect, step by step

1. The client fetches the profile (game CIDRs). If the control plane is down
   but a profile is already held, it carries on with that one.
2. It fetches the session. A `403` means the device is not yet activated, so it
   activates and asks again.
3. It reads the current default route. This happens before the tunnel adapter
   exists, so the tunnel cannot be mistaken for the default route.
4. It resolves every relay endpoint to a literal IPv4 address, once.
5. It creates a Wintun adapter, adds every relay as a WireGuard peer with no
   allowed-ips, and handshakes each one. The handshake is the measurement,
   taken over the player's own path.
6. It picks the fastest relay and pins that relay's `/32` through the physical
   adapter, so the tunnel's own packets never enter the tunnel. Game routes are
   installed only while a known game process is running.

After that:
- It re-ranks every five minutes, but only between games, and switches only
  for a gain of at least 10 ms.
- A relay with no handshake for 150 s counts as dead. The client moves to
  another relay. If none answers, it removes its routes.

## Game profiles

Routing is by destination address, so the profile must say which addresses
belong to a game, and it must stay narrow. Cloud providers publish /17s and
/18s; routing a whole block would drag unrelated services through a relay.

```
contributors report dst ip:port ──► observed_address (per game, per key)
                                          │  ≥ 3 independent keys
                                          ▼
                                    candidates ──► cross-check against AWS / Azure / ASN ranges
                                                        │  match        │  no match
                                                        ▼               ▼
                                             widen to ≤ /20, ≥ /24   "unverified": never added
                                             cap 131 072 addresses
                                                        │
                                                        ▼
                                             gnl-profile -publish ──► game_profile vN
```

An anycast block such as AWS Global Accelerator is kept as a single `/32`,
never widened. The block says nothing about who is behind it.

## Security boundaries

| Boundary | Defence |
|---|---|
| Unprivileged UI → LocalSystem service | Named pipe open only to SYSTEM, Administrators and INTERACTIVE users. Exactly four verbs, no parameters, bounded line length |
| Service binary and state on disk | `Program Files\GameNoLag` writable only by Administrators. `ProgramData\GameNoLag` readable only by SYSTEM and Administrators |
| Credentials at rest | Contributor keys and relay tokens are stored as SHA-256 hashes |
| Credentials in transit | Installers refuse non-HTTPS control URLs |
| Relay as an open proxy | FORWARD policy DROP. Egress only to the published game ranges (ipset). 64 KB/s per session per direction |
| One client receiving another's traffic | `UNIQUE (relay_id, inner_ip)` in the database |
| A new relay attracting traffic | Stays `pending` until verified from outside, and is not offered before `trusted_after` |
| One person poisoning a profile | Promotion needs three distinct keys and a published-range match |
| Anti-cheat | Games are detected by process name only; nothing opens, reads or hooks a game process |

## Data kept

| Where | What |
|---|---|
| Control plane | Key hashes, relay records and counters, device public keys and fingerprints (hostname), peer bindings, published profiles, observed destination `ip:port` per game and key hash |
| Relay | Its WireGuard private key (`/etc/gnl/relay.key`, 0600), its token, WireGuard peers |
| Client | Device private key, config, a rolling 8 MB log |

No payload, and no source address of a player's game traffic, is recorded
anywhere.
