# Windows client runbook

What the client is, how to install it, and what to look at when a machine
misbehaves. The client is two processes: a service that holds every privilege,
and a user interface that holds none.

## Why there is a service at all

Creating a Wintun adapter requires LocalSystem. Running the user interface "as
administrator" is not enough — it fails with access denied. So the privileged
work lives in a service, and the interface talks to it over a named pipe.

That pipe is the only privilege boundary in the client, and it is defended
twice:

- **Who may open it.** The descriptor grants full access to LocalSystem and
  Administrators, and read/write to INTERACTIVE USERS — the person physically
  logged in. Not Everyone, not Authenticated Users, so service accounts, network
  logons and scheduled tasks are excluded.
- **What they may ask for.** Four verbs: `connect`, `disconnect`, `status`,
  `reload-profile`. No parameters that name anything — no file path, no
  endpoint, no CIDR, no command. Everything the service acts on it fetched
  itself over TLS. An interface that has been tampered with can ask for a
  connect it was going to ask for anyway; it cannot point the service at an
  attacker's relay.

## Two properties that hold whatever goes wrong

These are why a crash cannot leave somebody without a working network:

1. **The adapter is created, not reused.** It disappears when the service exits,
   and every route pointing at it goes with it.
2. **Routes are non-persistent.** They are written through iphlpapi into the
   active store only, never into the registry's persistent routes, so a reboot
   clears them.

If somebody reports that their internet is broken and they suspect this
software: stop the service, or reboot. Both restore the machine completely.

## Installing

Build and install:

```
GOOS=windows GOARCH=amd64 go build -o gnl-service.exe ./cmd/gnl-service
```

Copy `gnl-service.exe` next to `deploy/windows/install.ps1` on the target
machine, then from an administrator PowerShell:

```powershell
.\install.ps1 -ContributorKey GNL-XXXX-XXXX-XXXX -ControlUrl https://api.example.com
```

The script refuses a non-https control URL, because the contributor key travels
as a bearer token.

It puts the binary in `C:\Program Files\GameNoLag` (readable by users so the
interface can launch, writable only by administrators — a LocalSystem binary an
ordinary user can replace is a way for that user to become LocalSystem) and the
state in `C:\ProgramData\GameNoLag`, readable by nobody but SYSTEM and
Administrators, because it holds this machine's private key.

Removing it:

```powershell
.\uninstall.ps1          # keeps this machine's device slot
.\uninstall.ps1 -Purge   # also deletes the identity; reinstalling costs a slot
```

## State on disk

`C:\ProgramData\GameNoLag\`

| File | What it is |
| --- | --- |
| `config.json` | `control_url`, `contributor_key`, and optionally `games`. Written by the installer. Unknown fields are refused, so a typo fails loudly instead of silently not taking effect. |
| `device.key` | This machine's WireGuard private key, hex. Generated on first start and then kept. |
| `service.log` | Rolled to `service.log.1` past 8 MB. |

`device.key` is kept rather than regenerated because each new identity consumes
a device slot on the contributor's key. A corrupt key file is **refused, not
replaced** — silently generating a new one would look, from the control plane,
like a second machine, and the contributor would lose a slot to a file nobody
knew had rotted. The error says to delete it deliberately.

## What a connect does

1. Fetch the profile (the published game address ranges). If the control plane
   is unreachable but ranges are already held, it continues on those — a brief
   outage should not stop somebody playing.
2. Fetch the session. A `403` means this device is not activated, so it
   activates — hostname as the fingerprint, because that is what a person
   recognises in the list of machines holding their slots — and asks again.
3. Read the machine's current default route. This happens **before** the tunnel
   adapter exists; reading it afterwards risks picking the tunnel itself.
4. Resolve every relay endpoint to a literal address, once, and configure
   WireGuard with the literal. Handing it a hostname would let it resolve
   independently of the pin, and a relay reached at an address the pin does not
   cover routes the tunnel into itself.
5. Create the adapter, configure every relay as a peer with **no allowed-ips**,
   and handshake each one. The handshake is the measurement: WireGuard answers
   nothing else, and it travels the path the player's traffic would. The
   initiation is sent explicitly, because a peer with no allowed-ips has nothing
   to send and would never start one by itself.
6. Pick the fastest, give it the allowed-ips and the adapter address, install
   its pin, and install game routes if a game is already running.

The control plane never ranks relays — it has no idea what any individual
player's path looks like. It filters; the client measures and decides.

## Routes, and why game ranges come and go

Two different things carry the address list and they are not the same:

- **AllowedIPs** is WireGuard's cryptographic ACL. It stays at the union of
  every range the profile knows.
- **The routing table** is what actually decides which packets enter the tunnel.

Game routes are installed when the game process appears and removed when it
goes. The ranges belong to AWS and Azure and are shared with thousands of
unrelated services, so leaving them installed would drag other applications'
traffic through a contributor's relay.

The relay's own `/32` is pinned through the **physical** adapter, installed
first and removed last. Without it the packets carrying the tunnel would
themselves match a game route — a loop that takes the machine's connectivity
with it.

## Detecting a game

By process name, from the toolhelp snapshot — the same public API Task Manager
uses. The client never opens a handle into a game, reads its memory, hooks it,
or injects anything. It is never in the game's address space, only in the
network stack beneath it, so an anti-cheat watching for exactly that finds
nothing.

The built-in list is in `cmd/gnl-service/engine_windows.go`. Override it per
machine with a `games` array in `config.json`:

```json
{
  "control_url": "https://api.example.com",
  "contributor_key": "GNL-XXXX-XXXX-XXXX",
  "games": [{ "id": "pubg", "process_names": ["TslGame.exe"] }]
}
```

## When a relay dies mid-session

Checked every poll against the last handshake. With a keepalive of 25 seconds,
nothing for 150 seconds means the relay is gone, and a dead relay with its
routes installed is worse than no tunnel at all — the game's traffic goes into
it and nowhere else. The client re-measures and moves to whatever answers. If
nothing answers, it **removes the routes**, which puts the player back on their
ordinary internet path, and retries every 30 seconds.

While healthy, it re-ranks the fleet every five minutes but only **between
games**: switching re-addresses the adapter and reinstalls every route, which a
player in a match would feel. A switch also needs the alternative to be at least
10 ms faster, so the client does not chase measurement jitter around the fleet.

## Reading the log

| Line | What it means |
| --- | --- |
| `relay X: ... has no IPv4 address` | That relay was skipped. The client pins and routes in IPv4 only. |
| `no relay answered` | Usually the network blocks UDP on the relay port. Routes are out; the player is on their ordinary path. |
| `relay X has not handshaken in over 2m30s` | That relay stopped answering; the client is re-measuring. |
| `profile version N: ignoring "..."` | One malformed prefix in the published profile. Dropped rather than refused, so one bad entry cannot leave a player with no routes. |
| `... is not a usable key file` | `device.key` has rotted. Deleting it costs a device slot; see above. |

Run it in a console to watch it live. It still needs LocalSystem:

```
psexec -s -i C:\"Program Files"\GameNoLag\gnl-service.exe
```

## What is not built yet

The user interface. Everything it will need is in place — the pipe, the four
verbs, and the status reply carrying state, relay, game, route count and round
trip.
