# GameNoLag

**Steadier ping for players in Vietnam, through community-run relays next to the game servers.**

[![ci](https://github.com/hashcott/NoLag/actions/workflows/ci.yml/badge.svg)](https://github.com/hashcott/NoLag/actions/workflows/ci.yml)
[![License: Apache-2.0](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

**English** · [Tiếng Việt](README.vi.md) · [简体中文](README.zh-CN.md)

Players in Vietnam are usually matched to game servers in Singapore, and to
Tokyo when the matchmaker spills over. In the evening, the ISP's route there is
often congested, and ping jumps and packets drop in the middle of a match. A VPS in Singapore, on a different
provider with different transit, can often reach the same servers on a cleaner
path.

GameNoLag puts that path under your game. It sends **only your game's traffic**
through a WireGuard relay close to the game servers. It picks the relay by
measuring your own connection, and it steps aside the moment something goes
wrong. The relays are VPSes contributed by the community.

> **Status: early, not yet in players' hands.** Every component is built and
> tested in CI, on Linux and Windows and against a real kernel and database.
> The Windows client has not yet been run by a player. The
> [manual Windows checklist](docs/windows-client-runbook.md#the-window) comes
> first.

## Why it is different

- **Only the game.** It is not a VPN. Routes are installed for a game's servers
  while that game runs, and removed when it exits. Your browser, Discord and
  downloads never leave your normal connection.
- **Your path decides.** Every candidate relay is measured with a real
  WireGuard handshake from your PC, and the fastest wins. It does not switch in
  the middle of a match for a small gain.
- **It never touches the game.** A game is recognised by its process name, the
  same way Task Manager lists it. Nothing opens, reads or injects into a game
  process.
- **It fails safe.** When a relay dies, the client moves to another one. When
  none answers, it removes its routes and your game carries on over your ISP.
  Stopping the service or rebooting leaves nothing behind.
- **Relays cannot be abused.** A relay forwards only to published game address
  ranges, rate-capped per player. It is verified from the outside before it is
  used, and revoking a contributor removes them from the network within one
  sync.

## How it works

```
  Your PC (Windows)                 Relay (community VPS)           Game server
 ┌────────────────────┐  WireGuard  ┌──────────────────────┐        ┌───────────┐
 │ game traffic only  │ ══════════► │ game ranges only,    │ ─────► │ Singapore │
 │ fastest relay wins │             │ rate-capped          │        │ Tokyo     │
 └─────────┬──────────┘             └──────────┬───────────┘        └───────────┘
           │ HTTPS                             │ sync every 10 s
           ▼                                   ▼
       ┌──────────────────────────────────────────────────┐
       │ Control plane: keys, devices, relays, game ranges │
       └──────────────────────────────────────────────────┘
```

The control plane decides who may use which relay and publishes the game
address ranges. It is never on the path your packets take. Game ranges are
kept narrow: an address is added only after three independent contributors
have seen it, and only if it lies inside a range that AWS, Azure or the game's
network announces.

More detail: [Architecture](docs/en/architecture.md).

## Get started

| I want to… | Start here |
|---|---|
| **Play** with lower, steadier ping | [User guide](docs/en/user-guide.md) |
| **Contribute a VPS** as a relay | [Setting up a relay](docs/en/setup-relay.md) |
| **Run a deployment** for a community | [Setting up the control plane](docs/en/setup-control-plane.md), then the [deployment order](docs/en/README.md#bringing-up-a-whole-deployment-in-order) |
| **Install or package** the Windows client | [Setting up the client](docs/en/setup-client.md) |
| **Hack on it** | [Development](#development) and [CONTRIBUTING](CONTRIBUTING.md) |

Players need a contributor key from whoever runs the deployment. In the current
phase, a key is issued to people who contribute a relay, and each key covers up
to three of their own PCs.

## What is in the box

| Binary | Runs on | Role |
|---|---|---|
| `gnl-service` | Windows, as a service | Holds the tunnel, measures relays, routes the running game |
| `gnl-ui` | Windows, as the user | Tray icon, mini panel and full window. No privileges |
| `gnl-control` | Linux | Control plane: keys, device slots, relays, game profiles |
| `gnl-agent` | Each relay | Keeps WireGuard peers, the egress allowlist and the firewall in sync |
| `gnl-relaycheck` | Outside a relay | Proves a relay is reachable from the internet |
| `gnl-profile` | Operator | Builds a game's address list from observations and published ranges |
| `gnl-probe`, `gnl-analyze` | Measurement hosts | Measure whether a VPS path beats the ISP at peak hours |

## Development

You need Go 1.25 or newer. Docker is needed for the kernel and database tests.

```bash
go test ./...                               # runs anywhere
GOOS=windows go vet ./...                   # the Windows client, from any OS
./deploy/verify-firewall-in-docker.sh       # relay firewall against a real kernel
GNL_TEST_DSN=postgres://… go test ./internal/control/   # control-plane store against Postgres
```

Windows-only code sits behind build tags, and the decisions it makes live in
platform-free files, so almost everything is tested on any machine. CI runs all
of the above on every push and pull request. It also builds the Linux binaries
and the Windows bundle as downloadable artifacts.

Build commands, conventions and the boundaries a change must not loosen are in
[CONTRIBUTING.md](CONTRIBUTING.md).

## Security

The client has a single privilege boundary. The tray talks to the service
through a named pipe that accepts four parameterless commands, and only from
the logged-in user. Keys and tokens are stored only as hashes. Relays drop
anything not bound for a game range.

The full list is in
[Architecture → Security boundaries](docs/en/architecture.md#security-boundaries).
Report vulnerabilities privately, as described in [SECURITY.md](SECURITY.md).

## Documentation

Guides in [English](docs/en/README.md), [Tiếng Việt](docs/vi/README.md) and
[简体中文](docs/zh-CN/README.md): user guide, client, relay and control-plane
setup, architecture.

In-depth runbooks (English):
- [Windows client](docs/windows-client-runbook.md)
- [control plane and first relay](docs/p1-runbook.md)
- [route-quality measurement](docs/p0-runbook.md)

## Contributing

Issues and pull requests are welcome. Please read
[CONTRIBUTING.md](CONTRIBUTING.md) first. Everyone taking part follows the
[Code of Conduct](CODE_OF_CONDUCT.md).

## License

[Apache License 2.0](LICENSE).
