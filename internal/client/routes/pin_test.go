package routes

import (
	"context"
	"fmt"
	"net/netip"
	"testing"
)

type fakeResolver struct {
	addrs []netip.Addr
	err   error
	asked string
}

func (f *fakeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	f.asked = host
	return f.addrs, f.err
}

func TestALiteralEndpointIsNotResolved(t *testing.T) {
	r := &fakeResolver{err: fmt.Errorf("resolver must not be used")}
	ap, pin, err := ResolveEndpoint(context.Background(), r, "203.0.113.9:51820")
	if err != nil {
		t.Fatal(err)
	}
	if r.asked != "" {
		t.Fatalf("resolved a literal address (%q)", r.asked)
	}
	if ap.String() != "203.0.113.9:51820" {
		t.Fatalf("endpoint = %s", ap)
	}
	if pin != "203.0.113.9/32" {
		t.Fatalf("pin = %s", pin)
	}
}

func TestTheDialledAddressIsTheOnePinned(t *testing.T) {
	// The whole point: one resolution, and the address handed to WireGuard is the
	// same one the pin covers. Two A records must not become two different
	// answers.
	r := &fakeResolver{addrs: []netip.Addr{
		netip.MustParseAddr("198.51.100.4"),
		netip.MustParseAddr("198.51.100.5"),
	}}
	ap, pin, err := ResolveEndpoint(context.Background(), r, "relay.example.com:51820")
	if err != nil {
		t.Fatal(err)
	}
	if pin != ap.Addr().String()+"/32" {
		t.Fatalf("pin %s does not cover the dialled address %s", pin, ap.Addr())
	}
}

func TestAnIPv6OnlyRelayIsRefused(t *testing.T) {
	// Refused rather than dialled unpinned: a peer reached with no pin routes the
	// tunnel's own packets into the tunnel.
	r := &fakeResolver{addrs: []netip.Addr{netip.MustParseAddr("2001:db8::1")}}
	if _, _, err := ResolveEndpoint(context.Background(), r, "relay.example.com:51820"); err == nil {
		t.Fatal("an IPv6-only relay was accepted, which would leave the tunnel unpinned")
	}
}

func TestAnIPv6LiteralIsRefused(t *testing.T) {
	if _, _, err := ResolveEndpoint(context.Background(), &fakeResolver{}, "[2001:db8::1]:51820"); err == nil {
		t.Fatal("an IPv6 literal was accepted")
	}
}

func TestAnEndpointWithoutAPortIsRefused(t *testing.T) {
	if _, _, err := ResolveEndpoint(context.Background(), &fakeResolver{}, "203.0.113.9"); err == nil {
		t.Fatal("an endpoint with no port was accepted")
	}
}

func TestResolutionFailureIsReported(t *testing.T) {
	r := &fakeResolver{err: fmt.Errorf("no such host")}
	_, _, err := ResolveEndpoint(context.Background(), r, "relay.example.com:51820")
	if err == nil {
		t.Fatal("a failed lookup was reported as success")
	}
}
