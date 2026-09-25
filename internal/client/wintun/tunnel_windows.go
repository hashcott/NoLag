//go:build windows

// Package wintun runs the tunnel: a Wintun adapter with wireguard-go driving it
// in-process.
//
// wireguard-go rather than wireguard-nt, for a licensing reason that is not
// negotiable: wireguard-nt is GPLv2 and cannot ship inside a closed-source
// installer, while wireguard-go is MIT. Wintun's prebuilt signed DLL may be
// redistributed only when the software using it goes through the documented
// wintun.h API, which is exactly what wireguard-go's tun package does.
//
// Creating the adapter requires LocalSystem. Running "as administrator" is not
// enough and fails with access denied, which is the whole reason this lives in a
// service rather than in the UI.
package wintun

import (
	"fmt"
	"net/netip"
	"time"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"

	"gamenolag/internal/client/pick"
)

// AdapterName is what the adapter is called in Windows.
const AdapterName = "GameNoLag"

// Tunnel is a running adapter and its WireGuard device.
type Tunnel struct {
	dev  *device.Device
	tun  tun.Device
	luid winipcfg.LUID
	logf func(string, ...any)
}

// Relay is what the client needs to bring a tunnel up against one relay.
type Relay struct {
	ID        string
	Endpoint  string
	PublicKey string
	InnerIP   netip.Prefix
	MTU       int
}

// Open creates the adapter and starts the device.
//
// The adapter is created rather than reused deliberately: it disappears when
// this process exits, and every route pointing at it goes with it. That is the
// safety brake — if this service crashes, the machine's networking heals itself
// instead of being left with routes into an adapter that no longer works.
func Open(privateKeyHex string, mtu int, logf func(string, ...any)) (*Tunnel, error) {
	t, err := tun.CreateTUN(AdapterName, mtu)
	if err != nil {
		return nil, fmt.Errorf("wintun: create adapter (this needs LocalSystem; "+
			"running as administrator is not enough): %w", err)
	}
	nativeTun, ok := t.(*tun.NativeTun)
	if !ok {
		t.Close()
		return nil, fmt.Errorf("wintun: unexpected TUN implementation")
	}
	luid := winipcfg.LUID(nativeTun.LUID())

	dev := device.NewDevice(t, conn.NewDefaultBind(), device.NewLogger(device.LogLevelError, "gnl: "))
	if err := dev.IpcSet("private_key=" + privateKeyHex + "\n"); err != nil {
		dev.Close()
		return nil, fmt.Errorf("wintun: set private key: %w", err)
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		return nil, fmt.Errorf("wintun: bring device up: %w", err)
	}
	return &Tunnel{dev: dev, tun: t, luid: luid, logf: logf}, nil
}

// LUID identifies the adapter to the routing code.
func (t *Tunnel) LUID() winipcfg.LUID { return t.luid }

// Close tears the tunnel down, taking every route pointing at it.
func (t *Tunnel) Close() {
	if t.dev != nil {
		t.dev.Close()
	}
}

// SetPeers replaces the device's peer set with every relay on offer, endpoints
// only.
//
// Every candidate is configured, not just the one in use, because measurement is
// a handshake and a handshake needs a configured peer. None of them is given
// allowed-ips here: without one, not a single packet of the player's traffic is
// eligible to leave through a relay that was merely measured.
//
// Endpoints must already be literal addresses. A hostname would be resolved by
// wireguard-go independently of the pin installed in the routing table, and a
// relay reached at an address the pin does not cover sends the tunnel's own
// packets into the tunnel.
func (t *Tunnel) SetPeers(relays []Relay) error {
	cfg := "replace_peers=true\n"
	for _, r := range relays {
		key, err := base64ToHex(r.PublicKey)
		if err != nil {
			return fmt.Errorf("wintun: relay %s public key: %w", r.ID, err)
		}
		cfg += "public_key=" + key + "\n"
		cfg += "endpoint=" + r.Endpoint + "\n"
	}
	if err := t.dev.IpcSet(cfg); err != nil {
		return fmt.Errorf("wintun: configure peers: %w", err)
	}
	return nil
}

// SetActive gives exactly one relay the allowed-ips and takes them from the
// rest.
//
// AllowedIPs is the cryptographic ACL and stays at the union of every game range
// the profile knows; what actually decides which packets enter the tunnel is the
// routing table, managed separately. That separation is why game routes can come
// and go with the game while the crypto configuration does not churn underneath
// a live session.
//
// This updates peers in place rather than replacing them, so the session
// established while measuring is the session the player connects on — replacing
// the peer set would discard it and make every connect pay for another handshake.
func (t *Tunnel) SetActive(relays []Relay, allowedIPs []netip.Prefix, active string) error {
	var cfg string
	for _, r := range relays {
		key, err := base64ToHex(r.PublicKey)
		if err != nil {
			return fmt.Errorf("wintun: relay %s public key: %w", r.ID, err)
		}
		cfg += "public_key=" + key + "\n"
		cfg += "update_only=true\n"
		cfg += "replace_allowed_ips=true\n"
		if r.ID != active {
			// No allowed-ips and no keepalive: the peer stays configured so it can be
			// measured again, and carries nothing in the meantime.
			cfg += "persistent_keepalive_interval=0\n"
			continue
		}
		for _, p := range allowedIPs {
			cfg += "allowed_ip=" + p.String() + "\n"
		}
		cfg += "persistent_keepalive_interval=25\n"
	}
	if err := t.dev.IpcSet(cfg); err != nil {
		return fmt.Errorf("wintun: select relay %s: %w", active, err)
	}
	return nil
}

// Measure handshakes every relay and reports how long each took.
//
// A real handshake, because WireGuard answers nothing else: an unauthenticated
// probe gets silence, so there is no lighter way to ask "can I reach you, and
// how fast". It is also the honest measurement — it travels the path the
// player's traffic would.
//
// The initiation is sent explicitly. A peer only starts a handshake when it has
// something to send, and every relay but the chosen one is configured with no
// allowed-ips precisely so that nothing can be sent to it — so waiting for a
// handshake to happen by itself would time out on every candidate and leave the
// client with nothing to rank.
func (t *Tunnel) Measure(relays []Relay, wait time.Duration) []pick.Measurement {
	out := make([]pick.Measurement, 0, len(relays))

	// A relay may have been handshaken already, on a previous measurement or as
	// the relay in use. The baseline is what makes "answered just now" different
	// from "answered at some point", which would otherwise rank a stale relay
	// first with a round trip of nearly zero.
	baseline := make(map[string]string, len(relays))
	if dump, err := t.dev.IpcGet(); err == nil {
		for _, r := range relays {
			baseline[r.PublicKey] = handshakeStamp(dump, r.PublicKey)
		}
	}

	pending := make(map[string]time.Time, len(relays))
	for _, r := range relays {
		peer, err := t.peer(r.PublicKey)
		if err != nil {
			out = append(out, pick.Measurement{RelayID: r.ID, Err: err})
			continue
		}
		pending[r.PublicKey] = time.Now()
		if err := peer.SendHandshakeInitiation(false); err != nil {
			// Not fatal on its own: wireguard-go retransmits, so a failure to put the
			// first packet on the wire may still resolve inside the window.
			t.log("relay %s: first handshake attempt did not send: %v", r.ID, err)
		}
	}

	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) && len(pending) > 0 {
		time.Sleep(50 * time.Millisecond)
		dump, err := t.dev.IpcGet()
		if err != nil {
			break
		}
		for _, r := range relays {
			start, waiting := pending[r.PublicKey]
			if !waiting {
				continue
			}
			if stamp := handshakeStamp(dump, r.PublicKey); stamp != "" && stamp != baseline[r.PublicKey] {
				out = append(out, pick.Measurement{RelayID: r.ID, RTT: time.Since(start)})
				delete(pending, r.PublicKey)
			}
		}
	}
	for _, r := range relays {
		if _, stillWaiting := pending[r.PublicKey]; stillWaiting {
			out = append(out, pick.Measurement{
				RelayID: r.ID,
				Err: fmt.Errorf("no handshake within %s; the relay may be down, or its "+
					"provider's firewall may be blocking the port", wait),
			})
		}
	}
	return out
}

// peer finds the configured peer for a public key.
func (t *Tunnel) peer(publicKeyBase64 string) (*device.Peer, error) {
	hexKey, err := base64ToHex(publicKeyBase64)
	if err != nil {
		return nil, fmt.Errorf("wintun: public key: %w", err)
	}
	var pk device.NoisePublicKey
	if err := pk.FromHex(hexKey); err != nil {
		return nil, fmt.Errorf("wintun: public key: %w", err)
	}
	p := t.dev.LookupPeer(pk)
	if p == nil {
		return nil, fmt.Errorf("wintun: this relay is not configured as a peer")
	}
	return p, nil
}

func (t *Tunnel) log(format string, args ...any) {
	if t.logf != nil {
		t.logf(format, args...)
	}
}

// LastHandshake reports when a relay last completed a handshake, and whether it
// ever has.
//
// This is how a relay that has died mid-session is noticed. With a keepalive of
// twenty-five seconds, a handshake that has not refreshed in minutes means the
// relay is gone — and a relay that is gone while its routes are installed is
// worse than no tunnel at all, because the game's traffic goes into it and
// nowhere else.
func (t *Tunnel) LastHandshake(publicKeyBase64 string) (time.Time, bool) {
	dump, err := t.dev.IpcGet()
	if err != nil {
		return time.Time{}, false
	}
	return parseStamp(handshakeStamp(dump, publicKeyBase64))
}
