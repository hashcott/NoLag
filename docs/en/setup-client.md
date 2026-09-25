# Setting up the Windows client

The client is two processes:

- **`gnl-service`** runs as LocalSystem and does everything that needs
  privilege: the Wintun adapter, WireGuard, routes, game detection.
- **`gnl-ui`** runs as the logged-in user and holds no privilege at all: a tray
  icon, a mini panel and a full window.

The UI asks the service for one of four verbs over a named pipe, and nothing
else.

This page is for whoever installs and packages the client. For day-to-day use,
see the [user guide](user-guide.md).

## Requirements

- Windows 10 or 11, 64-bit.
- Administrator rights to install. Daily use needs none.
- A contributor key and the control-plane URL, from the operator.

## 1. Get the bundle

The CI artifact `gamenolag-windows-amd64` holds everything, side by side:

| File | What it is |
|---|---|
| `gnl-service.exe` | The service |
| `gnl-ui.exe` | Tray and window, built with `-H=windowsgui` |
| `gnl-ui.exe.manifest` | **Must stay next to `gnl-ui.exe`.** Without it the tray menu is not created and the icon is blurry on high-DPI screens |
| `install.ps1`, `uninstall.ps1` | Installer and uninstaller |

To build it yourself on any OS:

```bash
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o dist/gnl-service.exe ./cmd/gnl-service
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags -H=windowsgui -o dist/gnl-ui.exe ./cmd/gnl-ui
cp deploy/windows/gnl-ui.exe.manifest deploy/windows/*.ps1 dist/
```

## 2. Install

In an **administrator** PowerShell, in the folder holding the bundle:

```powershell
Unblock-File .\*.ps1, .\*.exe
.\install.ps1 -ContributorKey GNL-XXXX-XXXX-XXXX -ControlUrl https://cp.example.com
```

`Unblock-File` removes the "downloaded from the internet" mark. Without it,
PowerShell refuses to run an unsigned downloaded script. The installer refuses
a non-`https` control URL, because the key travels as a bearer token.

What it does:

| Where | What |
|---|---|
| `C:\Program Files\GameNoLag\` | The binaries and the manifest. Readable by users, writable only by Administrators, since a LocalSystem binary a user can replace lets that user become LocalSystem |
| `C:\ProgramData\GameNoLag\` | State. Readable only by SYSTEM and Administrators, since it holds the device private key and the contributor key |
| Service `GameNoLag` | Registered to start automatically. The tunnel does not start until the UI asks for a connect |
| Start Menu, logon | A *GameNoLag* shortcut, and the tray set to start at every logon |

The installer does **not** launch the tray. It runs elevated, so anything it
started would run elevated and land in the administrator's session. Start
*GameNoLag* from the Start Menu, or log out and back in.

## 3. Configuration

`C:\ProgramData\GameNoLag\config.json` is written by the installer:

```json
{
  "control_url": "https://cp.example.com",
  "contributor_key": "GNL-XXXX-XXXX-XXXX",
  "games": [
    { "id": "pubg", "process_names": ["TslGame.exe"] }
  ]
}
```

- `games` is optional. When present it **replaces** the built-in list.
- Unknown fields are refused, so a typo fails loudly instead of silently doing
  nothing.
- Restart the service after editing: `Restart-Service GameNoLag`.

Built-in games:

| id | Process |
|---|---|
| `pubg` | `TslGame.exe` |
| `valorant` | `VALORANT-Win64-Shipping.exe` |
| `lol` | `League of Legends.exe` |
| `csgo` | `cs2.exe` |
| `dota2` | `dota2.exe` |

Other files in the same folder:

| File | What it is |
|---|---|
| `device.key` | This machine's WireGuard private key. Generated once and kept, because each new identity uses a device slot. A corrupt file is refused, not replaced; delete it deliberately if you must |
| `service.log` | Rolled to `service.log.1` past 8 MB |

## 4. First connect

1. Open the tray menu and choose **Connect**.
2. On first use the device activates itself, using the hostname as its name.
   This takes one of the key's device slots (three by default).
3. The client handshakes every relay, picks the fastest, and pins it.
4. When a listed game starts, its routes are installed. When it exits, they
   are removed.

If activation fails with *slots full*, the error lists the machines holding
the slots. Free one by uninstalling with `-Purge` on a machine you no longer
use, or ask the operator.

## 5. Logs

`C:\ProgramData\GameNoLag\service.log` needs an administrator to read.

| Line | Meaning |
|---|---|
| `no relay answered` | The network blocks UDP to the relays. Routes are removed and the player is on their ordinary path |
| `relay X has not handshaken in over 2m30s` | That relay stopped answering. The client is re-measuring |
| `relay X: ... has no IPv4 address` | That relay was skipped. The client routes IPv4 only |
| `profile version N: ignoring "..."` | One malformed prefix in the profile was dropped |
| `... is not a usable key file` | `device.key` is corrupt. Deleting it costs a device slot |

To watch the service live in a console (it still needs LocalSystem):

```
psexec -s -i "C:\Program Files\GameNoLag\gnl-service.exe"
```

## 6. Uninstall

```powershell
.\uninstall.ps1          # keeps this machine's identity and its device slot
.\uninstall.ps1 -Purge   # also deletes it; reinstalling costs a new slot
```

Stopping the service is what undoes its effect on the network. The adapter
disappears with the process, and every route pointing at it goes too.

## If a player's internet breaks

Stop the service (`Stop-Service GameNoLag`) or reboot. Either restores the
machine completely:

- The adapter is created by the service, not reused, and disappears when it
  exits.
- Routes are written to the active store only, never persisted, so a reboot
  clears them.

For how the client works inside, see the
[Windows client runbook](../windows-client-runbook.md).
