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

## 1. Install with the setup wizard

Every release carries **`GameNoLag-Setup-<version>.exe`**
([releases](https://github.com/hashcott/NoLag/releases/latest)). It asks for the
contributor key and the control-plane address, installs, and starts the tray for
the person who ran it. Upgrading is running a newer setup over the old one.
Uninstalling is **Settings → Apps → GameNoLag → Uninstall**.

For several machines, run it silently:

```powershell
GameNoLag-Setup-1.2.3.exe /VERYSILENT /SUPPRESSMSGBOXES /KEY=GNL-XXXX-XXXX-XXXX-XXXX /URL=https://cp.example.com
```

Exit code `0` means installed. Anything else means it was not: an invalid key,
a non-`https` address, or a failure in `install.ps1`. Add `/LOG=setup.log` for
the details.

The wizard is a thin shell around `install.ps1`. It checks the key and address
against a narrow alphabet, then runs the script with them, so both ways of
installing leave the machine in exactly the same state. CI builds the wizard on
every push, installs it on a Windows runner, queries the service over its pipe,
and uninstalls it again.

To build the wizard yourself you need [Inno Setup 6](https://jrsoftware.org/isinfo.php):

```powershell
iscc /DAppVersion=1.2.3 /DPayloadDir=C:\path\to\bundle /DDefaultControlUrl=https://cp.example.com deploy\windows\gamenolag.iss
```

`DefaultControlUrl` pre-fills the address field, so players only paste their
key. In CI it comes from the repository variable `GNL_CONTROL_URL`.

## 2. Install from the bundle, by script

The release's `gamenolag-windows-amd64-<version>.zip`, or the CI artifact of
the same name, holds the five files side by side:

| File | What it is |
|---|---|
| `gnl-service.exe` | The service |
| `gnl-ui.exe` | Tray and window, built with `-H=windowsgui` |
| `gnl-ui.exe.manifest` | **Must stay next to `gnl-ui.exe`.** Without it the tray menu is not created and the icon is blurry on high-DPI screens |
| `install.ps1`, `uninstall.ps1` | Installer and uninstaller |

To build the bundle yourself on any OS:

```bash
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o dist/gnl-service.exe ./cmd/gnl-service
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags -H=windowsgui -o dist/gnl-ui.exe ./cmd/gnl-ui
cp deploy/windows/gnl-ui.exe.manifest deploy/windows/*.ps1 dist/
```

In an **administrator** PowerShell, in the folder holding the bundle:

```powershell
Unblock-File .\*.ps1, .\*.exe
.\install.ps1 -ContributorKey GNL-XXXX-XXXX-XXXX-XXXX -ControlUrl https://cp.example.com
```

`Unblock-File` removes the "downloaded from the internet" mark. Without it,
PowerShell refuses to run an unsigned downloaded script.

### What either way does

| Where | What |
|---|---|
| `C:\Program Files\GameNoLag\` | The binaries and the manifest. Readable by users, writable only by Administrators, since a LocalSystem binary a user can replace lets that user become LocalSystem |
| `C:\ProgramData\GameNoLag\` | State. Readable only by SYSTEM and Administrators, since it holds the device private key and the contributor key |
| Service `GameNoLag` | Registered to start automatically. The tunnel does not start until the UI asks for a connect |
| Start Menu, logon | A *GameNoLag* shortcut, and the tray set to start at every logon |

Both refuse a non-`https` control URL, because the key travels as a bearer
token. A running service and tray are stopped before their files are replaced.
`install.ps1` on its own does **not** launch the tray: it runs elevated, and the
tray would land in the administrator's session. The wizard starts it as the
original, unelevated user instead.

## 3. Configuration

`C:\ProgramData\GameNoLag\config.json` is written by the installer:

```json
{
  "control_url": "https://cp.example.com",
  "contributor_key": "GNL-XXXX-XXXX-XXXX-XXXX",
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

Installed with the wizard: **Settings → Apps → GameNoLag → Uninstall**, or
`"C:\Program Files\GameNoLag\unins000.exe" /VERYSILENT` from a script. It runs
`uninstall.ps1` and keeps this machine's identity.

By script, in an administrator PowerShell:

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
