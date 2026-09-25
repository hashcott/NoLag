# Windows client runbook

What the client is, how to install it, and what to look at when a machine
misbehaves. The client is two processes: `gnl-service`, which holds every
privilege, and `gnl-ui`, a tray icon that holds none.

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

Build both halves:

```
GOOS=windows GOARCH=amd64 go build -o gnl-service.exe ./cmd/gnl-service
GOOS=windows GOARCH=amd64 go build -ldflags -H=windowsgui -o gnl-ui.exe ./cmd/gnl-ui
```

`-H=windowsgui` on the interface only. Without it a console window opens behind
the tray icon and stays there.

Copy `gnl-service.exe`, `gnl-ui.exe` and `deploy/windows/gnl-ui.exe.manifest`
next to `deploy/windows/install.ps1` on the target machine, then from an
administrator PowerShell:

```powershell
.\install.ps1 -ContributorKey GNL-XXXX-XXXX-XXXX -ControlUrl https://api.example.com
```

The script refuses a non-https control URL, because the contributor key travels
as a bearer token.

It puts the binaries in `C:\Program Files\GameNoLag` (readable by users so the
interface can launch, writable only by administrators — a LocalSystem binary an
ordinary user can replace is a way for that user to become LocalSystem) and the
state in `C:\ProgramData\GameNoLag`, readable by nobody but SYSTEM and
Administrators, because it holds this machine's private key.

It also registers the tray icon to start at every logon and puts it in the Start
Menu. It does **not** launch it: the installer runs elevated, so anything it
starts runs elevated too, and the tray icon would end up in the administrator's
session rather than the player's. It holds no privilege and should not have any.

The manifest has to travel with `gnl-ui.exe`. Go binaries carry no embedded
manifest, so Windows reads `gnl-ui.exe.manifest` from beside the binary; without
it the tray menu will not create, and the icon is drawn at 96 dpi and scaled up
by Windows on the high-resolution screen a gaming machine tends to have.

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

## The tray icon

The whole interface is: are my packets going through a relay, which one, and how
fast. That fits in an icon, a tooltip and a short menu. The window described
below is for the other question — why does this game feel slow — and the tray
works without it.

The icon is a ring, drawn in code rather than shipped as a resource, because an
`.ico` needs a resource compiler that does not run on the machine this is built
on. Filled means connected, hollow means not, and a bar across the ring means
connected but faulty, so all three are distinct in shape as well as colour —
many people cannot rely on the colour alone.

| Icon | Meaning |
| --- | --- |
| Grey ring | Not connected, or the service is not running |
| Green filled ring | Connected; a game's traffic is on the relay when one is running |
| Red ring with a bar across it | Connected but complaining — a relay went away, or routes failed to install |

The menu's first item is the status itself, disabled so it reads as a label. A
tray menu that makes somebody click something to find out what is going on is a
menu that will be clicked at the worst moment.

**Exit does not disconnect.** The service keeps the tunnel up, which is what
somebody who closes a tray icon mid-game wants. Disconnecting is its own item,
one line above.

It polls status every three seconds, so the icon also follows changes it did not
ask for — a game starting, a relay dying.

Only one copy runs, held by a `Local\GameNoLagTray` mutex. Local rather than
Global: the tray icon belongs to the person logged in, and two people switched
between accounts on one machine may each have their own.

**Open log folder** will give an ordinary user an access-denied window from
Explorer, because that directory holds this machine's private key. That is the
right outcome, and it still tells somebody on the phone with support exactly
where to look.

## The window

Left-click the tray icon for the mini panel, pinned above the taskbar in the
corner of the work area: RTT with a short sparkline, loss, relay, game, and one
connect or disconnect button. **+** opens the full window, **×** hides the
panel, and clicking the icon again hides it too. The full window adds a
three-minute RTT chart, active routes, and an event log; **−** collapses it back
to the panel. "Open window" in the menu opens the full window directly.

Both read what the tray's own poll already fetched. Neither asks the service
anything, so neither widens the pipe. The chart and the event log are built in
memory from consecutive polls — the service keeps no history — so they start
empty when the tray starts and are gone when it exits. A gap in the chart means
a poll with no measurement: not connected, or the service not answering.

Closing or hiding either window never disconnects.

The mini panel is topmost, which puts it above ordinary windows but not above a
game in exclusive fullscreen. Over a game it is visible only in borderless or
windowed mode.

### Checking it by hand

None of this can run on the machine it is written on, so after a change to
`window_windows.go`, on a Windows machine:

- [ ] At 100 % and at 150 % display scaling, the panel sits fully inside the
      corner above the taskbar, text is not clipped, and the full window's chart
      and event log fit.
- [ ] With the taskbar docked on the left or the right, the panel still sits
      above it rather than under it.
- [ ] With two monitors, the panel opens on the primary one.
- [ ] Stop `gnl-service` with the full window open: the tag turns OFF, the
      buttons disable, the chart breaks, and the log says the service was lost.
      Start it again: the log says it is answering.
- [ ] Connect, then close the full window and hide the panel: the tray icon is
      still filled and the tunnel is still up.
- [ ] Tab reaches every button, and Enter presses it.
