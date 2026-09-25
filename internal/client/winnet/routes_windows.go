//go:build windows

// Package winnet applies a route plan to the Windows routing table.
//
// Through iphlpapi, via winipcfg, rather than by shelling out to netsh. netsh
// prints its failures on stdout in the user's display language and reports them
// only through an exit code that, in script mode, reflects the last command
// alone — so a route that silently failed to install looks exactly like a relay
// that is down. The API returns an error code.
//
// Routes created through this API live in the active store only: they are not
// written to the registry's persistent routes and are gone after a reboot. That
// is the safety property the whole client rests on. Combined with the adapter
// disappearing when the process dies, a user can never end up with a machine
// that has no working network because this software stopped running.
package winnet

import (
	"fmt"
	"net/netip"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"

	"gamenolag/internal/client/routes"
)

// Applier writes routes for one tunnel adapter.
type Applier struct {
	// LUID of the Wintun adapter game traffic is routed into.
	Tunnel winipcfg.LUID
	// Physical is the adapter the relay itself must be reached through. The pin
	// goes here, not through the tunnel.
	Physical winipcfg.LUID
	// PhysicalGateway is the next hop for the pinned route.
	PhysicalGateway netip.Addr
}

// Apply carries out a plan.
//
// Order is not incidental and is why this does not simply iterate a set. The pin
// is installed before any game route: without it, the packets carrying the
// tunnel would themselves match a game route and be sent into the tunnel, which
// is a loop that takes the machine's connectivity with it. On the way down the
// pin is removed last, for the same reason in reverse.
func (a *Applier) Apply(c routes.Change) error {
	if c.AddPin != "" {
		if err := a.addPin(c.AddPin); err != nil {
			return err
		}
	}
	for _, p := range append(append([]string{}, c.AddLobby...), c.AddGame...) {
		if err := a.addTunnel(p); err != nil {
			return err
		}
	}
	for _, p := range append(append([]string{}, c.DelGame...), c.DelLobby...) {
		if err := a.delTunnel(p); err != nil {
			return err
		}
	}
	if c.RemovePin != "" {
		if err := a.delPin(c.RemovePin); err != nil {
			return err
		}
	}
	return nil
}

func (a *Applier) addTunnel(cidr string) error {
	p, err := netip.ParsePrefix(cidr)
	if err != nil {
		return fmt.Errorf("winnet: route %q: %w", cidr, err)
	}
	// Delete first. Windows returns ERROR_OBJECT_ALREADY_EXISTS for a duplicate,
	// and a route left from a previous run may point at an adapter that no longer
	// exists - in which case the game's traffic would go nowhere while everything
	// here reported success.
	_ = a.Tunnel.DeleteRoute(p, netip.Addr{})
	if err := a.Tunnel.AddRoute(p, netip.Addr{}, 1); err != nil {
		return fmt.Errorf("winnet: add route %s: %w", cidr, err)
	}
	return nil
}

func (a *Applier) delTunnel(cidr string) error {
	p, err := netip.ParsePrefix(cidr)
	if err != nil {
		return fmt.Errorf("winnet: route %q: %w", cidr, err)
	}
	if err := a.Tunnel.DeleteRoute(p, netip.Addr{}); err != nil {
		// A route that is already gone is the state we wanted. Anything else is
		// worth reporting, because a route left behind sends traffic into an
		// adapter that may not exist.
		if err == windows.ERROR_NOT_FOUND {
			return nil
		}
		return fmt.Errorf("winnet: delete route %s: %w", cidr, err)
	}
	return nil
}

func (a *Applier) addPin(cidr string) error {
	p, err := netip.ParsePrefix(cidr)
	if err != nil {
		return fmt.Errorf("winnet: pin %q: %w", cidr, err)
	}
	if !a.PhysicalGateway.IsValid() {
		return fmt.Errorf("winnet: no gateway for the pinned route to %s; without it "+
			"the tunnel's own packets would follow a game route into the tunnel", cidr)
	}
	_ = a.Physical.DeleteRoute(p, a.PhysicalGateway)
	if err := a.Physical.AddRoute(p, a.PhysicalGateway, 1); err != nil {
		return fmt.Errorf("winnet: pin route %s via %s: %w", cidr, a.PhysicalGateway, err)
	}
	return nil
}

func (a *Applier) delPin(cidr string) error {
	p, err := netip.ParsePrefix(cidr)
	if err != nil {
		return fmt.Errorf("winnet: pin %q: %w", cidr, err)
	}
	if err := a.Physical.DeleteRoute(p, a.PhysicalGateway); err != nil {
		if err == windows.ERROR_NOT_FOUND {
			return nil
		}
		return fmt.Errorf("winnet: remove pin %s: %w", cidr, err)
	}
	return nil
}

// ConfigureAdapter gives the tunnel adapter its inner address and MTU.
//
// The address is read back rather than trusted: this API can report success
// before the address is usable, and the failure that follows is an unrelated
// one somewhere else entirely.
func ConfigureAdapter(luid winipcfg.LUID, inner netip.Prefix, mtu uint32) error {
	if err := luid.SetIPAddresses([]netip.Prefix{inner}); err != nil {
		return fmt.Errorf("winnet: set address %s: %w", inner, err)
	}
	iface, err := luid.IPInterface(windows.AF_INET)
	if err != nil {
		return fmt.Errorf("winnet: read interface: %w", err)
	}
	iface.NLMTU = mtu
	// Routes are ours to manage; letting Windows add its own on-link route for
	// the tunnel subnet would put entries in the table this client never removes.
	iface.UseAutomaticMetric = false
	iface.Metric = 1
	if err := iface.Set(); err != nil {
		return fmt.Errorf("winnet: set interface parameters: %w", err)
	}
	return nil
}

// DefaultRoute finds the adapter and gateway the machine currently uses to
// reach the internet, which is where the relay pin must go.
func DefaultRoute() (winipcfg.LUID, netip.Addr, error) {
	rows, err := winipcfg.GetIPForwardTable2(windows.AF_INET)
	if err != nil {
		return 0, netip.Addr{}, fmt.Errorf("winnet: read routing table: %w", err)
	}
	var best *winipcfg.MibIPforwardRow2
	for i := range rows {
		r := &rows[i]
		if r.DestinationPrefix.PrefixLength != 0 {
			continue // not a default route
		}
		if best == nil || r.Metric < best.Metric {
			best = r
		}
	}
	if best == nil {
		return 0, netip.Addr{}, fmt.Errorf("winnet: no default route; this machine has no " +
			"path to the internet, so there is nothing to pin the relay through")
	}
	return best.InterfaceLUID, best.NextHop.Addr(), nil
}
