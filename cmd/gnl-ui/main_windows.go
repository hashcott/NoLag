//go:build windows

// Command gnl-ui is the user-facing half of the GameNoLag client.
//
// It runs as an ordinary user and holds no privilege at all. Everything that
// matters happens in gnl-service, which runs as LocalSystem; this asks it for
// one of four things over a named pipe and shows what came back. It cannot name
// a relay, a route or a file, so a tampered copy of this program can ask for a
// connect it was going to ask for anyway and nothing else.
//
// It is a tray icon rather than a window. The whole interface is: are my packets
// going through a relay, which one, and how fast — which fits in an icon, a
// tooltip and a short menu, and a window would only be somewhere to put things
// nobody asked for.
package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/lxn/walk"
	"golang.org/x/sys/windows"

	"gamenolag/internal/client/ipc"
	"gamenolag/internal/client/winpipe"
)

// pollEvery is how often the tray asks the service where things stand.
//
// Three seconds: fast enough that the icon is not visibly lying after a game
// starts, slow enough to be nothing on a machine that is playing a game.
const pollEvery = 3 * time.Second

// statusTimeout bounds a status request. The service holds its lock across a
// connect, so a status can wait behind one — but not forever, because an
// interface frozen on a read is worse than one saying the service is busy.
const statusTimeout = 20 * time.Second

// actionTimeout bounds connect and disconnect. A connect measures every relay
// before it answers.
const actionTimeout = 90 * time.Second

func main() {
	// One tray icon, not one per launch. Somebody clicking the shortcut twice
	// should get the copy they already have, not a second icon fighting it for
	// the same pipe.
	if !claimSingleInstance() {
		return
	}

	mw, err := walk.NewMainWindow()
	if err != nil {
		fatal("Could not start", err)
	}
	ni, err := walk.NewNotifyIcon(mw)
	if err != nil {
		fatal("Could not create the tray icon", err)
	}
	defer ni.Dispose()

	u := &ui{mw: mw, ni: ni}
	if err := u.build(); err != nil {
		fatal("Could not build the menu", err)
	}
	if err := ni.SetVisible(true); err != nil {
		fatal("Could not show the tray icon", err)
	}

	// The window is an addition to the tray, not a replacement for it: if it
	// cannot be built, the icon and its menu still work.
	if p, err := newPanels(u); err != nil {
		_ = ni.ShowError("GameNoLag", "Could not create the window: "+err.Error())
	} else {
		u.win = p
		ni.MouseDown().Attach(func(_, _ int, b walk.MouseButton) {
			if b == walk.LeftButton {
				p.toggleMini()
			}
		})
	}

	go u.pollLoop()
	mw.Run()
}

type ui struct {
	mw *walk.MainWindow
	ni *walk.NotifyIcon

	icons   map[state]*walk.Icon
	status  *walk.Action
	connect *walk.Action
	discon  *walk.Action
	reload  *walk.Action

	// busy is set while a verb is in flight, and read only on the GUI thread.
	busy bool
	// pending is what the window's button says while busy.
	pending string
	// shown is the state the icon is currently displaying, so a notification is
	// raised on a change rather than on every poll.
	shown state

	// What the window draws, kept whether or not it is open so that opening it
	// shows the last three minutes at once. All of it is touched only on the GUI
	// thread.
	view view
	hist history
	log  eventLog
	win  *panels // nil if the window could not be built
}

func (u *ui) build() error {
	u.icons = map[state]*walk.Icon{}
	for _, s := range []state{stateOff, stateOn, stateFault} {
		ic, err := walk.NewIconFromImageForDPI(trayIcon(s, 64), u.ni.DPI())
		if err != nil {
			return err
		}
		u.icons[s] = ic
	}
	u.shown = stateOff
	u.view = viewOf(ipc.Response{}, nil)
	if err := u.ni.SetIcon(u.icons[stateOff]); err != nil {
		return err
	}
	_ = u.ni.SetToolTip("GameNoLag — not connected")

	// The first item is the status itself, disabled so it reads as a label. A tray
	// menu that makes somebody click something to find out what is going on is a
	// menu that will be clicked at the worst moment.
	u.status = walk.NewAction()
	_ = u.status.SetText("Not connected")
	_ = u.status.SetEnabled(false)

	u.connect = walk.NewAction()
	_ = u.connect.SetText("Connect")
	u.connect.Triggered().Attach(func() { u.send(ipc.VerbConnect, "Connecting…") })

	u.discon = walk.NewAction()
	_ = u.discon.SetText("Disconnect")
	_ = u.discon.SetEnabled(false)
	u.discon.Triggered().Attach(func() { u.send(ipc.VerbDisconnect, "Disconnecting…") })

	u.reload = walk.NewAction()
	_ = u.reload.SetText("Refresh game list")
	u.reload.Triggered().Attach(func() { u.send(ipc.VerbReloadProfile, "Refreshing…") })

	open := walk.NewAction()
	_ = open.SetText("Open window")
	open.Triggered().Attach(func() {
		if u.win != nil {
			u.win.showFull()
		}
	})

	logs := walk.NewAction()
	_ = logs.SetText("Open log folder")
	logs.Triggered().Attach(u.openLogs)

	quit := walk.NewAction()
	_ = quit.SetText("Exit")
	quit.Triggered().Attach(func() {
		// Closing this does not disconnect. The service keeps the tunnel up, which
		// is what somebody who closes a tray icon mid-game wants; disconnecting is
		// its own menu item, one line above.
		walk.App().Exit(0)
	})

	for _, a := range []*walk.Action{
		u.status, open, walk.NewSeparatorAction(),
		u.connect, u.discon,
		walk.NewSeparatorAction(),
		u.reload, logs,
		walk.NewSeparatorAction(),
		quit,
	} {
		if err := u.ni.ContextMenu().Actions().Add(a); err != nil {
			return err
		}
	}
	return nil
}

// send runs one verb off the GUI thread and shows the result.
func (u *ui) send(v ipc.Verb, pending string) {
	if u.busy {
		return
	}
	u.busy, u.pending = true, pending
	u.setBusy(pending)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		resp, err := winpipe.Ask(ctx, v)
		u.mw.Synchronize(func() {
			u.busy = false
			if err != nil {
				u.log.note(time.Now(), levelErr, string(v)+" failed: "+friendly(err))
				u.show(ipc.Response{}, err)
				// Only for something the user asked for: a failed poll updates the icon
				// quietly, but a button that did nothing has to say so.
				_ = u.ni.ShowError("GameNoLag", friendly(err))
				return
			}
			if !resp.OK && resp.Error != "" {
				u.log.note(time.Now(), levelErr, string(v)+" failed: "+resp.Error)
				_ = u.ni.ShowError("GameNoLag", resp.Error)
			}
			u.show(resp, nil)
		})
	}()
}

// pollLoop keeps the icon honest about what the service is doing, including
// changes this interface did not ask for — a game starting, or a relay dying.
func (u *ui) pollLoop() {
	for {
		ctx, cancel := context.WithTimeout(context.Background(), statusTimeout)
		resp, err := winpipe.Ask(ctx, ipc.VerbStatus)
		cancel()
		u.mw.Synchronize(func() {
			// Recorded even while a verb is in flight, so the chart's spacing stays
			// one poll per slot.
			u.hist.add(sampleFrom(time.Now(), resp, err))
			// The window and its log follow polls only. A verb's reply is partial —
			// a disconnect names no game, a failed connect names nothing — so
			// reading one as a poll logs changes that never happened. The next
			// poll, at most three seconds later, reports the real ones.
			u.view = viewOf(resp, err)
			u.log.record(time.Now(), resp, err)
			if u.busy {
				return // a verb is in flight; its own answer is fresher than this
			}
			u.show(resp, err)
		})
		time.Sleep(pollEvery)
	}
}

func (u *ui) setBusy(text string) {
	_ = u.status.SetText(text)
	_ = u.connect.SetEnabled(false)
	_ = u.discon.SetEnabled(false)
	_ = u.reload.SetEnabled(false)
	if u.win != nil {
		u.win.refresh()
	}
}

// show puts one answer on screen.
func (u *ui) show(resp ipc.Response, err error) {
	connected := err == nil && resp.State == "connected"

	_ = u.connect.SetEnabled(err == nil && !connected)
	_ = u.discon.SetEnabled(connected)
	_ = u.reload.SetEnabled(err == nil)

	next := iconFor(resp, err)
	_ = u.status.SetText(summary(resp, err))
	_ = u.ni.SetToolTip(tooltip(resp, err))
	if next != u.shown {
		_ = u.ni.SetIcon(u.icons[next])
		if next == stateFault {
			_ = u.ni.ShowWarning("GameNoLag", resp.Error)
		}
		u.shown = next
	}

	if u.win != nil {
		u.win.refresh()
	}
}

func (u *ui) openLogs() {
	base := os.Getenv("ProgramData")
	if base == "" {
		base = `C:\ProgramData`
	}
	dir := filepath.Join(base, "GameNoLag")
	// The directory is readable only by SYSTEM and Administrators, so an ordinary
	// user gets an access-denied window from Explorer rather than the log. That is
	// the right outcome — the directory holds this machine's private key — and it
	// still tells somebody on the phone with support exactly where to look.
	if err := windows.ShellExecute(0, windows.StringToUTF16Ptr("open"),
		windows.StringToUTF16Ptr(dir), nil, nil, windows.SW_SHOWNORMAL); err != nil {
		_ = u.ni.ShowError("GameNoLag", "Could not open "+dir+": "+err.Error())
	}
}

// claimSingleInstance reports whether this is the only copy running.
func claimSingleInstance() bool {
	// Local\ rather than Global\: the scope is this logon session, because the
	// tray icon belongs to the person logged in. Two people switched between
	// accounts on one machine may each have their own.
	name, err := windows.UTF16PtrFromString(`Local\GameNoLagTray`)
	if err != nil {
		return true
	}
	// The handle is deliberately never closed: it is released when the process
	// ends, which is exactly the lifetime being claimed.
	if _, err := windows.CreateMutex(nil, false, name); err != nil {
		return !errors.Is(err, windows.ERROR_ALREADY_EXISTS)
	}
	return true
}

func fatal(what string, err error) {
	walk.MsgBox(nil, "GameNoLag", what+":\n\n"+err.Error(), walk.MsgBoxIconError)
	os.Exit(1)
}
