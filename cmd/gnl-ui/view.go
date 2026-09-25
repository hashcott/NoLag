package main

import (
	"fmt"
	"strconv"

	"gamenolag/internal/client/ipc"
)

type rgb struct{ R, G, B uint8 }

// The window's state colours. Cyan and orange rather than the tray's green and
// red, which sit side by side on a dark panel for somebody with red-green colour
// blindness; the shape of the tray icon drawn beside them carries the state too.
var (
	accentOn    = rgb{0x4F, 0xD1, 0xE8}
	accentFault = rgb{0xFF, 0x7A, 0x45}
	accentOff   = rgb{0x7B, 0x87, 0x94}
)

// view is everything the window draws for one poll, decided here so it can be
// tested away from Windows.
type view struct {
	state   state
	tag     string
	accent  rgb
	line    string // the tray menu's one-line summary
	rtt     string
	loss    string
	relay   string
	game    string
	routes  string
	problem string // why a connected tunnel is faulty

	button string
	verb   ipc.Verb
	// canAct is false when the service is not answering: a button that sends a
	// verb nobody will receive only teaches somebody that buttons do nothing.
	canAct bool
}

func viewOf(resp ipc.Response, err error) view {
	v := view{
		state: iconFor(resp, err),
		line:  summary(resp, err),
		rtt:   "--", loss: "--", relay: "--", game: "none", routes: "0",
		button: "Connect", verb: ipc.VerbConnect,
		canAct: err == nil,
	}
	switch v.state {
	case stateOn:
		v.tag, v.accent = "UP", accentOn
	case stateFault:
		v.tag, v.accent, v.problem = "FAULT", accentFault, resp.Error
	default:
		v.tag, v.accent = "OFF", accentOff
	}
	if err != nil {
		return v
	}

	connected := resp.State == "connected"
	if connected {
		if resp.TunnelRTTms > 0 {
			v.rtt = fmt.Sprintf("%.0f", resp.TunnelRTTms)
		}
		v.loss = fmt.Sprintf("%.1f%%", resp.LossPct)
		if resp.RelayID != "" {
			v.relay = resp.RelayID
		}
		v.routes = strconv.Itoa(resp.ActiveRoutes)
		v.button, v.verb = "Disconnect", ipc.VerbDisconnect
	}
	if resp.GameRunning != "" {
		v.game = resp.GameRunning
		if connected {
			v.game += " → relay"
		}
	}
	return v
}

// footnote is the one line under the numbers: why the state is not UP, or
// nothing when it is.
func (v view) footnote() string {
	switch {
	case v.problem != "":
		return "! " + v.problem
	case v.state == stateOn:
		return ""
	}
	return v.line
}
