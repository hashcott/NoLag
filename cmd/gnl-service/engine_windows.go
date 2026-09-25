//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"sync"
	"time"

	"gamenolag/internal/api"
	"gamenolag/internal/client/cloud"
	"gamenolag/internal/client/device"
	"gamenolag/internal/client/gamewatch"
	"gamenolag/internal/client/ipc"
	"gamenolag/internal/client/pick"
	"gamenolag/internal/client/routes"
	"gamenolag/internal/client/winnet"
	"gamenolag/internal/client/winproc"
	"gamenolag/internal/client/wintun"
)

// pollInterval is how often the running process list is read.
//
// Two seconds: a game takes far longer than that to reach a server after its
// process appears, so the routes are always in place before the first packet,
// and the cost of the toolhelp snapshot at this rate is not measurable.
const pollInterval = 2 * time.Second

// measureWindow bounds how long a connect waits for handshakes.
//
// Six seconds, because wireguard-go re-sends an initiation after five: the
// window has to outlast one retransmit, or a single dropped packet reads as a
// relay that is down. A relay that has not answered by then is not one a player
// should be waiting on.
const measureWindow = 6 * time.Second

// staleAfter is how long without a handshake means the relay is gone.
//
// The keepalive is twenty-five seconds, so anything past a couple of minutes is
// not jitter. A dead relay with its routes still installed is worse than no
// tunnel at all: the game's traffic goes into it and nowhere else.
const staleAfter = 150 * time.Second

// remeasureEvery is how often a healthy client re-ranks the fleet.
const remeasureEvery = 5 * time.Minute

// retryEvery is how often to try again once nothing answered.
const retryEvery = 30 * time.Second

// defaultMTU is used when a relay offers none. 1420 is what WireGuard leaves
// inside a 1500-byte path.
const defaultMTU = 1420

// builtinGames is the list used when the configuration names none.
var builtinGames = []gamewatch.Game{
	{ID: "pubg", ProcessNames: []string{"TslGame.exe"}},
	{ID: "valorant", ProcessNames: []string{"VALORANT-Win64-Shipping.exe"}},
	{ID: "lol", ProcessNames: []string{"League of Legends.exe"}},
	{ID: "csgo", ProcessNames: []string{"cs2.exe"}},
	{ID: "dota2", ProcessNames: []string{"dota2.exe"}},
}

type engine struct {
	cfg  device.Config
	key  device.Key
	api  *cloud.Client
	logf func(string, ...any)

	// mu guards everything below. Every verb from the pipe and the poll loop pass
	// through it: two of them acting on the routing table at once is how a machine
	// ends up with a route into an adapter that has gone.
	mu          sync.Mutex
	watch       *gamewatch.Watcher
	tun         *wintun.Tunnel
	applier     *winnet.Applier
	installed   routes.Table
	relays      []wintun.Relay
	pins        map[string]string
	active      string
	activeRTT   time.Duration
	cidrs       []string
	allowed     []netip.Prefix
	lastErr     string
	lastMeasure time.Time
}

func newEngine(cfg device.Config, key device.Key, logf func(string, ...any)) *engine {
	games := builtinGames
	if len(cfg.Games) > 0 {
		games = make([]gamewatch.Game, 0, len(cfg.Games))
		for _, g := range cfg.Games {
			games = append(games, gamewatch.Game{ID: g.ID, ProcessNames: g.ProcessNames})
		}
	}
	return &engine{
		cfg:  cfg,
		key:  key,
		logf: logf,
		api: &cloud.Client{
			BaseURL:         cfg.ControlURL,
			ContributorKey:  cfg.ContributorKey,
			DevicePublicKey: key.PublicBase64,
		},
		watch: gamewatch.New(games),
		pins:  map[string]string{},
	}
}

// handle answers one verb from the pipe.
func (e *engine) handle(ctx context.Context, v ipc.Verb) ipc.Response {
	e.mu.Lock()
	defer e.mu.Unlock()

	switch v {
	case ipc.VerbConnect:
		if err := e.connect(ctx); err != nil {
			e.lastErr = err.Error()
			e.logf("connect: %v", err)
			// Leave nothing half-installed. A pin without a tunnel, or routes into an
			// adapter that failed to come up, is worse than being disconnected.
			e.teardown()
			return ipc.Response{OK: false, Error: err.Error()}
		}
		e.lastErr = ""
		return e.statusLocked()
	case ipc.VerbDisconnect:
		e.teardown()
		return ipc.Response{OK: true, State: "disconnected"}
	case ipc.VerbStatus:
		return e.statusLocked()
	case ipc.VerbReloadProfile:
		if err := e.reloadProfile(ctx); err != nil {
			return ipc.Response{OK: false, Error: err.Error()}
		}
		return e.statusLocked()
	}
	return ipc.Refuse(fmt.Errorf("ipc: unknown verb %q", v))
}

func (e *engine) statusLocked() ipc.Response {
	state := "disconnected"
	if e.tun != nil {
		state = "connected"
	}
	n := len(e.installed.Lobby) + len(e.installed.Game)
	if e.installed.RelayPin != "" {
		n++
	}
	return ipc.Response{
		OK:           e.lastErr == "",
		Error:        e.lastErr,
		State:        state,
		RelayID:      e.active,
		GameRunning:  e.watch.Current().GameID,
		ActiveRoutes: n,
		TunnelRTTms:  float64(e.activeRTT.Microseconds()) / 1000,
	}
}

// connect brings the tunnel up and installs the routes for whatever is running.
func (e *engine) connect(ctx context.Context) error {
	if e.tun != nil {
		return nil
	}

	if err := e.fetchProfile(ctx); err != nil {
		return err
	}
	sess, err := e.session(ctx)
	if err != nil {
		return err
	}

	// The physical adapter is read before the tunnel adapter exists. Reading it
	// afterwards risks picking the tunnel itself as the way out, and pinning the
	// relay through the tunnel is exactly the loop the pin exists to prevent.
	physLUID, gateway, err := winnet.DefaultRoute()
	if err != nil {
		return err
	}

	relays, pins, mtu, err := e.offers(ctx, sess.Relays)
	if err != nil {
		return err
	}

	tun, err := wintun.Open(e.key.PrivateHex, mtu, e.logf)
	if err != nil {
		return err
	}
	e.tun = tun
	e.applier = &winnet.Applier{Tunnel: tun.LUID(), Physical: physLUID, PhysicalGateway: gateway}
	e.relays, e.pins = relays, pins

	if err := tun.SetPeers(relays); err != nil {
		return err
	}
	e.lastMeasure = time.Now()
	chosen, err := pick.Best(tun.Measure(relays, measureWindow))
	if err != nil {
		return fmt.Errorf("%w; every relay on offer failed to answer, which usually means "+
			"this network blocks UDP on the relay port", err)
	}
	return e.activate(chosen.Primary, rttOf(chosen, chosen.Primary))
}

// activate points the tunnel at one relay: its address on the adapter, its
// allowed-ips, its pin, and the routes for whatever game is running.
func (e *engine) activate(relayID string, rtt time.Duration) error {
	r, ok := relayByID(e.relays, relayID)
	if !ok {
		return fmt.Errorf("gnl-service: relay %s is not on offer", relayID)
	}
	mtu := r.MTU
	if mtu <= 0 {
		mtu = defaultMTU
	}
	if err := winnet.ConfigureAdapter(e.tun.LUID(), r.InnerIP, uint32(mtu)); err != nil {
		return err
	}
	if err := e.tun.SetActive(e.relays, e.allowed, relayID); err != nil {
		return err
	}
	e.active, e.activeRTT = relayID, rtt
	return e.applyRoutes()
}

// applyRoutes moves the routing table to what the current situation wants.
func (e *engine) applyRoutes() error {
	want := routes.Disconnected()
	if e.tun != nil && e.active != "" {
		var gameCIDRs []string
		if e.watch.Current().Running() {
			gameCIDRs = e.cidrs
		}
		want = routes.Want(e.pins[e.active], nil, gameCIDRs)
	}
	change := routes.Plan(e.installed, want)
	if change.Empty() {
		return nil
	}
	if err := e.applier.Apply(change); err != nil {
		// The table is now in an unknown state, so what is recorded as installed
		// must be the superset. Recording the goal instead would leave routes this
		// service believes it never added, and never removes.
		e.installed = union(e.installed, want)
		return err
	}
	e.installed = want
	return nil
}

// poll is one turn of the process watch.
func (e *engine) poll() {
	names, err := winproc.List()
	if err != nil {
		e.logf("listing processes: %v", err)
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	state, changed := e.watch.Poll(names)
	if e.tun == nil {
		return
	}
	if !changed {
		e.checkRelay()
		return
	}
	if state.Running() {
		e.logf("%s is running (%s); installing its routes", state.GameID, state.Process)
	} else {
		e.logf("no game running; removing game routes")
	}
	if err := e.applyRoutes(); err != nil {
		e.lastErr = err.Error()
		e.logf("applying routes: %v", err)
		return
	}
	// Cleared on success, not only when a measurement happens to run. Otherwise
	// one transient route failure leaves the interface showing a fault until the
	// next re-rank, which is five minutes away at best and never while a game is
	// running.
	e.lastErr = ""
}

// checkRelay notices a relay that has stopped answering, and re-ranks the fleet
// when it is free to.
func (e *engine) checkRelay() {
	if e.active == "" {
		// Nothing answered last time, so the routes are out and the player is on
		// their ordinary path. Try again on a slow cadence rather than spending six
		// seconds of every poll on it.
		if time.Since(e.lastMeasure) > retryEvery {
			e.remeasure(true)
		}
		return
	}
	r, ok := relayByID(e.relays, e.active)
	if !ok {
		return
	}
	last, ever := e.tun.LastHandshake(r.PublicKey)
	if !ever || time.Since(last) > staleAfter {
		e.logf("relay %s has not handshaken in over %s; re-measuring", e.active, staleAfter)
		e.remeasure(true)
		return
	}
	// Only between games. Switching re-addresses the adapter and reinstalls every
	// route, which a player in a match feels — and a match is precisely when they
	// would least forgive it.
	if !e.watch.Current().Running() && time.Since(e.lastMeasure) > remeasureEvery {
		e.remeasure(false)
	}
}

// remeasure re-ranks the fleet and moves if it is worth it.
//
// ponytail: this holds the lock for the measurement window, so a status request
// can wait a few seconds behind it. Worth splitting only if the UI shows it.
func (e *engine) remeasure(force bool) {
	e.lastMeasure = time.Now()
	chosen, err := pick.Best(e.tun.Measure(e.relays, measureWindow))
	if err != nil {
		if e.active == "" {
			return // already down to the ordinary path; nothing changed
		}
		e.logf("no relay answered; removing routes so the game falls back to the ordinary path")
		e.lastErr = err.Error()
		e.active, e.activeRTT = "", 0
		if aerr := e.applyRoutes(); aerr != nil {
			e.logf("removing routes: %v", aerr)
		}
		return
	}
	rtt := rttOf(chosen, chosen.Primary)
	if chosen.Primary == e.active {
		e.activeRTT = rtt
		e.lastErr = ""
		return
	}
	// When the relay in use is gone, anything that answers is better; the margin
	// exists to stop the client chasing jitter, not to keep it on a dead relay.
	current := e.activeRTT
	if force {
		current = 0
	}
	if !pick.WorthSwitching(current, rtt, pick.DefaultSwitchMargin) {
		return
	}
	e.logf("switching from relay %s to %s (%.1f ms)", e.active, chosen.Primary, float64(rtt.Microseconds())/1000)
	if err := e.activate(chosen.Primary, rtt); err != nil {
		e.lastErr = err.Error()
		e.logf("switching to relay %s: %v", chosen.Primary, err)
		// Half-switched: the adapter may carry the new relay's address while the
		// peers still point at the old one. Leaving the old relay marked active
		// would look healthy on the next poll — its last handshake is recent — and
		// nothing would repair it until the next re-rank, five minutes away.
		// Standing down puts the player on their ordinary path and brings the
		// thirty-second retry into play.
		e.active, e.activeRTT = "", 0
		if aerr := e.applyRoutes(); aerr != nil {
			e.logf("removing routes after a failed switch: %v", aerr)
		}
		return
	}
	e.lastErr = ""
}

// teardown removes every route this service installed and closes the tunnel.
//
// Routes first, in the order the plan gives: the pin comes out last, after the
// game routes that would otherwise catch the tunnel's own packets on the way
// down.
func (e *engine) teardown() {
	if e.applier != nil {
		if err := e.applier.Apply(routes.Plan(e.installed, routes.Disconnected())); err != nil {
			e.logf("removing routes: %v; they are non-persistent and will be gone after a restart", err)
		}
	}
	if e.tun != nil {
		e.tun.Close()
	}
	e.tun, e.applier = nil, nil
	e.installed = routes.Disconnected()
	// Whatever went wrong belonged to the session being torn down. Keeping it
	// would have a disconnected client reporting a fault it can no longer have.
	e.lastErr = ""
	e.relays, e.pins = nil, map[string]string{}
	e.active, e.activeRTT = "", 0
}

func (e *engine) session(ctx context.Context) (sess api.SessionResponse, err error) {
	sess, err = e.api.Session(ctx)
	if errors.Is(err, cloud.ErrNotActivated) {
		// First run on this machine, or a device that was released. The hostname is
		// the fingerprint because it is what a person recognises in the list of
		// machines holding their slots.
		host, _ := os.Hostname()
		if _, aerr := e.api.Activate(ctx, host); aerr != nil {
			return api.SessionResponse{}, aerr
		}
		sess, err = e.api.Session(ctx)
	}
	if err != nil {
		return api.SessionResponse{}, err
	}
	if len(sess.Relays) == 0 {
		return api.SessionResponse{}, fmt.Errorf("gnl-service: the control plane offered no relays; " +
			"none are verified and available right now")
	}
	return sess, nil
}

// fetchProfile refreshes the game ranges.
func (e *engine) fetchProfile(ctx context.Context) error {
	prof, err := e.api.Profile(ctx)
	if err != nil {
		if len(e.cidrs) > 0 {
			// A control plane that is briefly unreachable must not stop a player
			// connecting on ranges that are already known and change rarely.
			e.logf("fetching the profile: %v; continuing on version already held", err)
			return nil
		}
		return err
	}
	allowed := make([]netip.Prefix, 0, len(prof.CIDRs))
	kept := make([]string, 0, len(prof.CIDRs))
	for _, c := range prof.CIDRs {
		p, perr := netip.ParsePrefix(c)
		if perr != nil || !p.Addr().Is4() {
			// Dropped rather than refused: one bad prefix in a published profile must
			// not leave a player with no routes at all.
			e.logf("profile version %d: ignoring %q", prof.Version, c)
			continue
		}
		allowed = append(allowed, p.Masked())
		kept = append(kept, p.Masked().String())
	}
	if len(kept) == 0 {
		return fmt.Errorf("gnl-service: profile version %d has no usable IPv4 ranges", prof.Version)
	}
	e.cidrs, e.allowed = kept, allowed
	return nil
}

// reloadProfile refetches the ranges and puts the new set into effect.
func (e *engine) reloadProfile(ctx context.Context) error {
	if err := e.fetchProfile(ctx); err != nil {
		return err
	}
	if e.tun == nil || e.active == "" {
		return nil
	}
	// The ACL and the routing table both carry the ranges, so both move.
	if err := e.tun.SetActive(e.relays, e.allowed, e.active); err != nil {
		return err
	}
	return e.applyRoutes()
}

// offers turns what the control plane sent into peers with literal endpoints and
// the pin each one needs.
func (e *engine) offers(ctx context.Context, in []api.RelayOffer) ([]wintun.Relay, map[string]string, int, error) {
	relays := make([]wintun.Relay, 0, len(in))
	pins := make(map[string]string, len(in))
	mtu := 0
	for _, o := range in {
		ap, pin, err := routes.ResolveEndpoint(ctx, net.DefaultResolver, o.Endpoint)
		if err != nil {
			// One unusable relay must not sink a session that has others.
			e.logf("relay %s: %v", o.RelayID, err)
			continue
		}
		inner, err := netip.ParsePrefix(o.InnerIP)
		if err != nil {
			e.logf("relay %s: inner address %q: %v", o.RelayID, o.InnerIP, err)
			continue
		}
		m := o.MTU
		if m <= 0 {
			m = defaultMTU
		}
		// The adapter is created once with a single MTU while peers may differ, so
		// the smallest offered is what it gets: an adapter larger than a relay's
		// path fragments, and a fragmented game packet is the problem this exists
		// to remove.
		if mtu == 0 || m < mtu {
			mtu = m
		}
		relays = append(relays, wintun.Relay{
			ID: o.RelayID, Endpoint: ap.String(), PublicKey: o.PublicKey, InnerIP: inner, MTU: m,
		})
		pins[o.RelayID] = pin
	}
	if len(relays) == 0 {
		return nil, nil, 0, fmt.Errorf("gnl-service: none of the %d relays on offer could be used; "+
			"see the log above for each", len(in))
	}
	return relays, pins, mtu, nil
}

func relayByID(relays []wintun.Relay, id string) (wintun.Relay, bool) {
	for _, r := range relays {
		if r.ID == id {
			return r, true
		}
	}
	return wintun.Relay{}, false
}

func rttOf(c pick.Choice, id string) time.Duration {
	for _, m := range c.Ranked {
		if m.RelayID == id {
			return m.RTT
		}
	}
	return 0
}

// union is what must be assumed installed after a partial failure.
func union(a, b routes.Table) routes.Table {
	out := routes.Table{RelayPin: a.RelayPin}
	if out.RelayPin == "" {
		out.RelayPin = b.RelayPin
	}
	out.Lobby = mergeUnique(a.Lobby, b.Lobby)
	out.Game = mergeUnique(a.Game, b.Game)
	return out
}

func mergeUnique(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, s := range append(append([]string{}, a...), b...) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
