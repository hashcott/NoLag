package routes

import (
	"context"
	"fmt"
	"net"
	"net/netip"
)

// Resolver is what ResolveEndpoint uses to look up a name. The standard
// net.Resolver satisfies it; tests supply their own.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// ResolveEndpoint turns a relay's endpoint into the literal address the tunnel
// will dial and the /32 that must be pinned through the physical adapter.
//
// The name is resolved here, once, and the literal is what gets configured as
// the peer endpoint. Handing wireguard-go the hostname instead would let it
// resolve independently: a relay behind two A records could then be dialled at
// an address different from the one pinned, the tunnel's own packets would match
// a game route, and the machine would route the tunnel into itself.
func ResolveEndpoint(ctx context.Context, r Resolver, endpoint string) (netip.AddrPort, string, error) {
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil {
		return netip.AddrPort{}, "", fmt.Errorf("routes: relay endpoint %q is not host:port: %w", endpoint, err)
	}
	addrs := []netip.Addr{}
	if a, err := netip.ParseAddr(host); err == nil {
		addrs = append(addrs, a)
	} else {
		// IPv4 only: the routing and pinning below work in AF_INET, so an IPv6
		// relay would be dialled with no pin at all.
		got, err := r.LookupNetIP(ctx, "ip4", host)
		if err != nil {
			return netip.AddrPort{}, "", fmt.Errorf("routes: resolving relay %q: %w", host, err)
		}
		addrs = got
	}
	for _, a := range addrs {
		a = a.Unmap()
		if !a.Is4() {
			continue
		}
		ap, err := netip.ParseAddrPort(net.JoinHostPort(a.String(), port))
		if err != nil {
			return netip.AddrPort{}, "", fmt.Errorf("routes: relay endpoint %q: %w", endpoint, err)
		}
		return ap, a.String() + "/32", nil
	}
	return netip.AddrPort{}, "", fmt.Errorf("routes: relay %q has no IPv4 address; this client "+
		"pins and routes in IPv4 only, and without a pin the tunnel would carry itself", host)
}
