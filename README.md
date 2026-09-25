# GameNoLag

[![ci](https://github.com/hashcott/NoLag/actions/workflows/ci.yml/badge.svg)](https://github.com/hashcott/NoLag/actions/workflows/ci.yml)
[![License: Apache-2.0](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

**English** · [Tiếng Việt](README.vi.md) · [简体中文](README.zh-CN.md)

GameNoLag sends a game's traffic from a player in Vietnam through a WireGuard
relay near the game servers, for the hours when that path beats the ISP's
default route. Only the game goes through the relay. Everything else on the
machine stays on its ordinary internet path.

The relays are VPSes contributed by the community. A contributor gets a key.
The key activates up to three of their own machines, and those machines are
routed through the fleet.

> **Status: early.** Every component below exists and is tested. The Windows
> client has passed CI on Windows but has not yet been run by a player; the
> manual checklist in the [client runbook](docs/windows-client-runbook.md#the-window)
> is the gate before it is.

## How it works

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

- **The client measures, the control plane filters.** The control plane hands
  out the relays that are up and trusted. The client handshakes each one over
  the player's own path and picks the fastest. The control plane cannot rank
  relays, because it has no view of any player's path.
- **Game ranges are narrow and cross-checked.** An address reaches the published
  profile only after three independent contributors have seen it, and only if
  it falls inside a range AWS, Azure or the game's ASN publishes. Routes are
  installed while the game runs and removed when it exits.
- **Failure falls back to the ordinary path.** If the relay stops answering,
  the client moves to another one. If none answers, it removes its routes. The
  tunnel adapter and every route vanish when the service stops. Rebooting
  restores the machine completely.

## Components

| Command | Runs on | What it does |
|---|---|---|
| `gnl-service` | Windows, LocalSystem | Holds the tunnel. Fetches the session and profile, measures relays, installs and removes routes. |
| `gnl-ui` | Windows, as the user | Tray icon, mini panel and full window. Asks the service for one of `connect`, `disconnect`, `status`, `reload-profile`, and nothing else. |
| `gnl-control` | Linux | Control plane: contributor keys, device slots, relay registration and sync, the game profile. Also mints keys (`-mint-key`). |
| `gnl-agent` | Linux relay | Reconciles WireGuard peers, the egress allowlist (ipset) and the firewall with the control plane. |
| `gnl-relaycheck` | Anywhere outside the relay | Proves a relay's UDP port is reachable from the internet, and reports that to the control plane. |
| `gnl-profile` | Operator | Builds a game's CIDR list from observed addresses and published ranges. Dry-run unless `-publish` is given. |
| `gnl-probe`, `gnl-analyze` | Measurement hosts | The P0 route-quality campaign: is a VPS path better than the ISP's at peak? |

## Repository layout

```
cmd/            one directory per binary above
internal/
  api/          request and response types shared by client, relay and control plane
  control/      HTTP server, Postgres store, schema, rate limits
  agent/        relay firewall rules and the sync loop
  ipsetsync/    atomic ipset swaps for the egress allowlist
  wgsync/       WireGuard peer reconciliation
  profile/      turning observations and published ranges into a narrow profile
  probe/, analyze/, stats/   the P0 measurement campaign
  client/       the Windows client: ipc, routes, wintun, winpipe, gamewatch, pick, …
deploy/         relay installer, Windows install/uninstall, verification scripts
docs/           runbooks and guides (see below)
```

## Development

Needs Go 1.25 or newer. Most of the code builds and tests on any OS. The
Windows-only parts are behind build tags, and their decisions sit in
platform-free files so they are tested everywhere.

```bash
go test ./...                       # everything that runs anywhere
GOOS=windows go vet ./...           # the Windows client, from any OS
./deploy/verify-firewall-in-docker.sh   # iptables/ipset against a real kernel (needs Docker)
```

The firewall tests change the machine's firewall. They sit behind the
`linuxroot` build tag and `GNL_FIREWALL_TESTS=1`, so `go test ./...` never
touches iptables. The script runs them in a privileged throwaway container.

### Building

```bash
# Windows client
GOOS=windows GOARCH=amd64 go build -o gnl-service.exe ./cmd/gnl-service
GOOS=windows GOARCH=amd64 go build -ldflags -H=windowsgui -o gnl-ui.exe ./cmd/gnl-ui

# relay and control plane
GOOS=linux GOARCH=amd64 go build -o gnl-agent ./cmd/gnl-agent
GOOS=linux GOARCH=amd64 go build -o gnl-control ./cmd/gnl-control
```

`gnl-ui.exe` must ship with `deploy/windows/gnl-ui.exe.manifest` beside it.
Without the manifest the tray menu is not created.

### CI

[`.github/workflows/ci.yml`](.github/workflows/ci.yml) runs on every push to
`main` and every pull request:

1. `gofmt`, `go vet` and `go test` on Ubuntu and on Windows. On Ubuntu it also
   vets the Windows build.
2. The firewall tests against a real kernel.
3. Once both pass, it builds the Linux binaries and the Windows client bundle
   (both executables, the manifest and the install scripts). Both are
   downloadable from the run's artifacts.

### Conventions

- Commits follow Conventional Commits (`feat(client): …`, `fix(relay): …`),
  with a body that says why.
- Logic that decides something gets a test. Code that only calls the operating
  system is kept thin, so what is left untested is as little as possible.
- Comments explain why, not what.

## Security model, in brief

- **One privilege boundary on the client.** The UI runs unprivileged and may
  ask the service for four verbs with no parameters. It cannot name a relay, a
  route, a file or a command. The pipe admits only the interactively logged-in
  user.
- **Secrets are stored as hashes.** Contributor keys and relay tokens are
  stored only as hashes. The device private key lives in
  `C:\ProgramData\GameNoLag`, readable only by SYSTEM and Administrators.
- **Relays are not open proxies.** The FORWARD policy is DROP. Egress is
  limited to the published game ranges, and each session is capped at 64 KB/s
  per direction. A new relay carries no traffic until its reachability is
  proven from outside and its observation window has passed.
- **The game is never touched.** It is detected by process name, the same way
  Task Manager does it. The client never opens a handle into it, reads its
  memory or injects anything.
- **Profiles need evidence.** Observations record destination addresses only,
  and one contributor cannot promote an address alone.

## Documentation

Full documentation in [English](docs/en/README.md),
[Tiếng Việt](docs/vi/README.md) and [简体中文](docs/zh-CN/README.md).

| Guide | For |
|---|---|
| [User guide](docs/en/user-guide.md) | Players: install, use, troubleshoot, uninstall |
| [Setting up the client](docs/en/setup-client.md) | Installing, configuring and packaging the Windows client |
| [Setting up a relay](docs/en/setup-relay.md) | Contributors running a relay on a VPS |
| [Setting up the control plane](docs/en/setup-control-plane.md) | Operators: Postgres, TLS, keys, verifying relays, game profiles |
| [Architecture](docs/en/architecture.md) | How the parts fit, the API, relay lifecycle, security boundaries |

Deep references: the [Windows client runbook](docs/windows-client-runbook.md),
[P1 runbook](docs/p1-runbook.md) and [P0 runbook](docs/p0-runbook.md).

## Contributing

Contributions are welcome. Read [CONTRIBUTING.md](CONTRIBUTING.md) first, and
report vulnerabilities privately as described in [SECURITY.md](SECURITY.md),
not in a public issue. Everyone taking part follows the
[Code of Conduct](CODE_OF_CONDUCT.md).

## License

[Apache License 2.0](LICENSE).
