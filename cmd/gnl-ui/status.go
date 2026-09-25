package main

import (
	"context"
	"errors"
	"fmt"

	"gamenolag/internal/client/ipc"
)

// summary is the one line at the top of the menu.
//
// Disabled in the menu, so it reads as a label rather than something to click. A
// tray menu that makes somebody click to find out what is going on is a menu
// that will be clicked at the worst moment.
func summary(resp ipc.Response, err error) string {
	if err != nil {
		return friendly(err)
	}
	if resp.State != "connected" {
		if resp.Error != "" {
			return "Not connected — " + resp.Error
		}
		return "Not connected"
	}
	s := "Connected"
	if resp.RelayID != "" {
		s += " · " + resp.RelayID
	}
	if resp.TunnelRTTms > 0 {
		s += fmt.Sprintf(" · %.0f ms", resp.TunnelRTTms)
	}
	return s
}

// maxTooltip is what Windows shows. Past it the tooltip is truncated, and a
// tooltip truncated by Windows loses whichever half mattered.
const maxTooltip = 127

// tooltip is what hovering the icon says.
func tooltip(resp ipc.Response, err error) string {
	s := "GameNoLag — " + summary(resp, err)
	if err == nil && resp.State == "connected" {
		if resp.GameRunning != "" {
			s += "\n" + resp.GameRunning + " is running; its traffic is on the relay"
		} else {
			s += "\nNo game running; nothing is being routed"
		}
	}
	// By runes, not bytes: a game name or a relay id may be non-ASCII, and a cut
	// through the middle of a rune reaches Windows as a broken string.
	if r := []rune(s); len(r) > maxTooltip {
		s = string(r[:maxTooltip-1]) + "…"
	}
	return s
}

// friendly turns the errors somebody will actually hit into something they can
// act on, and leaves the rest alone.
func friendly(err error) string {
	if errors.Is(err, ipc.ErrNoService) {
		return "The GameNoLag service is not running"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "The service is not answering"
	}
	return err.Error()
}

// iconFor decides what the tray is showing.
func iconFor(resp ipc.Response, err error) state {
	if err != nil || resp.State != "connected" {
		return stateOff
	}
	if resp.Error != "" {
		// Connected and complaining: the tunnel is up but something is wrong with
		// it, which is exactly the case a plain green icon would hide.
		return stateFault
	}
	return stateOn
}
