# GameNoLag user guide

GameNoLag sends your game's traffic through a relay close to the game servers
(Singapore, Tokyo) instead of your ISP's default route. Only the game goes
through the relay. Your browser, Discord, downloads and everything else stay on
your ordinary connection.

This guide is for players. For installing and packaging, see
[Setting up the Windows client](setup-client.md).

## What you need

- Windows 10 or 11, 64-bit.
- Administrator rights to install. After that, everyday use needs none.
- A **contributor key** (`GNL-XXXX-XXXX-XXXX-XXXX`) from the operator, given when
  you contribute a VPS. One key activates up to **3 PCs**.
- The **control-plane address** (starts with `https://`), also from the
  operator.

## Installing

1. Download **`GameNoLag-Setup-<version>.exe`** from the
   [latest release](https://github.com/hashcott/NoLag/releases/latest).
2. Double-click it. Windows asks for administrator permission; allow it.
   Until the installer is code-signed, Windows SmartScreen may say *Windows
   protected your PC*. Click **More info → Run anyway**, but only for a file
   you downloaded from the release page above.
3. Accept the licence, then enter your **contributor key** and the
   **control-plane address**. The address may already be filled in.
4. Finish with **Start GameNoLag** ticked. The icon appears in the notification
   area, and from now on it starts by itself every time you log in.

Upgrading works the same way: run the newer setup over the old one. Your PC
keeps its identity, so no extra device slot is used.

## Everyday use

### The tray icon

GameNoLag lives in the notification area at the right of the taskbar. If you
cannot see it, click the `^` arrow.

| Icon | Meaning |
|---|---|
| Grey hollow ring | Not connected, or the service is not running |
| Green ring with a filled centre | Connected. When a game is running, its traffic goes through the relay |
| Red ring with a bar across it | Connected, but something is wrong, such as a lost relay or routes that failed to install |

The three states differ in shape as well as colour. Hover over the icon to see
the relay and the latency.

**Right-click** for the menu:

| Item | What it does |
|---|---|
| (first line) | Current status. Read-only |
| Open window | Opens the full window |
| Connect | Measures every relay and picks the fastest. Takes a few seconds to tens of seconds |
| Disconnect | Puts all traffic back on your ordinary path |
| Refresh game list | Reloads the game address ranges |
| Open log folder | Opens the log folder. *Access denied* is expected; see Troubleshooting |
| Exit | Closes the icon. **Does not disconnect**; use Disconnect first if you want that |

### Mini panel and full window

**Left-click** the icon for the mini panel above the taskbar. It shows latency
(rtt) with a small bar chart, packet loss, relay, game, one Connect/Disconnect
button, and the reason when you are not connected.

| Control | What it does |
|---|---|
| **+** | Opens the full window: a 3-minute latency chart, active routes, and an event log (connects, relay switches, games starting and stopping, faults) |
| **−** | In the full window, goes back to the mini panel |
| **×** | Hides the mini panel. Left-clicking the icon again does the same |
| **Tab** / **Enter** | Move between buttons / press the selected one |

Closing or hiding a window **never disconnects**. The chart and the log live in
memory only and start empty each time GameNoLag starts. A gap in the chart
means no measurement at that moment (not connected, or the service not
answering), not zero latency.

The mini panel stays on top of other windows. It shows over games in
**borderless** or **windowed** mode, but not over **exclusive fullscreen**. To
keep it off your game, press **×** before you play.

### While you play

There is nothing to do. When connected, GameNoLag notices a listed game
starting and routes only that game. When the game exits, the routes are
removed.

| Game | Process |
|---|---|
| PUBG | `TslGame.exe` |
| Valorant | `VALORANT-Win64-Shipping.exe` |
| League of Legends | `League of Legends.exe` |
| Counter-Strike 2 | `cs2.exe` |
| Dota 2 | `dota2.exe` |

GameNoLag recognises a game **by its process name only**, the way Task Manager
lists it. It never opens, reads or modifies the game process; it works in the
network stack underneath.

It will not switch relays in the middle of a match just because another one got
faster, since that would cause a stutter. It switches between matches. The
exception is a relay that dies outright (no reply for 150 seconds): GameNoLag
moves to another one at once. If none answers, your game goes back to your
ordinary path, and GameNoLag retries every 30 seconds.

## Troubleshooting

| Symptom | Likely cause | What to do |
|---|---|---|
| *The GameNoLag service is not running* | The service stopped | Administrator PowerShell: `Start-Service GameNoLag` |
| *no relay answered* after Connect | Your network blocks UDP (common on office or café networks) | Try another network. Your game still works on the ordinary path |
| *no device slots left* | The key already has 3 PCs | Remove the identity on a PC you no longer use (see Uninstalling), or ask the operator |
| Red icon with a bar | Connected but faulty | Read the error in the full window. It usually recovers; if not, Disconnect then Connect |
| *Open log folder*: Access denied | The folder holds your private key, so only administrators can read it | Expected. The log is `C:\ProgramData\GameNoLag\service.log`; open it with Notepad run as Administrator |
| No internet at all, and you suspect GameNoLag | — | `Stop-Service GameNoLag`, or reboot. Every GameNoLag route disappears as soon as the service stops |

When asking for help, send `service.log` and say what you were doing when it
happened.

## Uninstalling

**Settings → Apps → Installed apps → GameNoLag → Uninstall.** This removes the
program and keeps this PC's identity, so reinstalling later uses no extra device
slot.

To also free the slot, because you are giving the PC away for example, remove
the identity too. In an administrator PowerShell:

```powershell
Remove-Item -Recurse -Force "$env:ProgramData\GameNoLag"
```

The next install then counts as a new PC. The operator can also free the slot
for you.

## Privacy

- Only traffic of listed games goes through a relay. Everything else stays on
  your ordinary path.
- GameNoLag does not read your traffic's contents or anything inside the game
  process.
- Your PC's private key and your contributor key live in
  `C:\ProgramData\GameNoLag`, readable only by SYSTEM and administrators. The
  control plane stores only a hash of your contributor key.
