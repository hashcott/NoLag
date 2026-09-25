package wgsync

import (
	"fmt"
	"net"
	"sync"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"

	"gamenolag/internal/api"
)

// activeWindow is how recently a peer must have completed a handshake to count
// as a player currently on the relay. WireGuard rekeys about every two minutes,
// so a session in use is never quiet for longer than this.
const activeWindow = 180 * time.Second

// Stats is what the agent reports upward about the interface.
type Stats struct {
	ActivePeers int
	TotalPeers  int
	RxBytes     int64
	TxBytes     int64
}

// Device is the kernel-facing surface, behind an interface so the agent loop can
// be tested without root or a WireGuard interface.
type Device interface {
	Peers(iface string) ([]api.Peer, error)
	Apply(iface string, c Change) error
	Stats(iface string) (Stats, error)
	Close() error
}

type wgctlDevice struct{ c *wgctrl.Client }

// NewWGCtl opens a WireGuard control client. It needs root.
func NewWGCtl() (Device, error) {
	c, err := wgctrl.New()
	if err != nil {
		return nil, fmt.Errorf("wgsync: open wgctrl (are we root, and is the wireguard module loaded?): %w", err)
	}
	return &wgctlDevice{c: c}, nil
}

func (d *wgctlDevice) Close() error { return d.c.Close() }

func (d *wgctlDevice) Peers(iface string) ([]api.Peer, error) {
	dev, err := d.c.Device(iface)
	if err != nil {
		return nil, fmt.Errorf("wgsync: read %s: %w", iface, err)
	}
	out := make([]api.Peer, 0, len(dev.Peers))
	for _, p := range dev.Peers {
		inner := ""
		if len(p.AllowedIPs) > 0 {
			inner = p.AllowedIPs[0].String()
		}
		out = append(out, api.Peer{PublicKey: p.PublicKey.String(), InnerIP: inner})
	}
	return out, nil
}

func (d *wgctlDevice) Stats(iface string) (Stats, error) {
	dev, err := d.c.Device(iface)
	if err != nil {
		return Stats{}, fmt.Errorf("wgsync: read %s: %w", iface, err)
	}
	s := Stats{TotalPeers: len(dev.Peers)}
	cutoff := time.Now().Add(-activeWindow)
	for _, p := range dev.Peers {
		s.RxBytes += p.ReceiveBytes
		s.TxBytes += p.TransmitBytes
		if p.LastHandshakeTime.After(cutoff) {
			s.ActivePeers++
		}
	}
	return s, nil
}

func (d *wgctlDevice) Apply(iface string, c Change) error {
	if c.Empty() {
		return nil
	}
	var cfgs []wgtypes.PeerConfig

	for _, p := range append(append([]api.Peer{}, c.Add...), c.Update...) {
		key, err := wgtypes.ParseKey(p.PublicKey)
		if err != nil {
			return fmt.Errorf("wgsync: peer %.8s has an unparseable public key: %w", p.PublicKey, err)
		}
		_, ipnet, err := net.ParseCIDR(p.InnerIP)
		if err != nil {
			return fmt.Errorf("wgsync: peer %.8s inner IP %q: %w", p.PublicKey, p.InnerIP, err)
		}
		cfgs = append(cfgs, wgtypes.PeerConfig{
			PublicKey: key,
			// ReplaceAllowedIPs makes Add and Update the same operation: the peer
			// ends up holding exactly the address the control plane assigned,
			// whatever it held before.
			ReplaceAllowedIPs: true,
			AllowedIPs:        []net.IPNet{*ipnet},
		})
	}

	for _, k := range c.Remove {
		key, err := wgtypes.ParseKey(k)
		if err != nil {
			// A key the kernel gave us that we cannot parse back is not worth
			// failing the whole batch over; skip it and let the next poll retry.
			continue
		}
		cfgs = append(cfgs, wgtypes.PeerConfig{PublicKey: key, Remove: true})
	}

	if err := d.c.ConfigureDevice(iface, wgtypes.Config{Peers: cfgs}); err != nil {
		return fmt.Errorf("wgsync: configure %s: %w", iface, err)
	}
	return nil
}

// FakeDevice is an in-memory Device for tests.
type FakeDevice struct {
	mu    sync.Mutex
	peers map[string]string // public key -> inner IP
	Err   error             // when set, every method returns it
}

// NewFakeDevice returns an empty in-memory device.
func NewFakeDevice() *FakeDevice {
	return &FakeDevice{peers: map[string]string{}}
}

func (f *FakeDevice) Close() error { return nil }

func (f *FakeDevice) Peers(string) ([]api.Peer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return nil, f.Err
	}
	out := make([]api.Peer, 0, len(f.peers))
	for k, ip := range f.peers {
		out = append(out, api.Peer{PublicKey: k, InnerIP: ip})
	}
	return out, nil
}

func (f *FakeDevice) Stats(string) (Stats, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return Stats{}, f.Err
	}
	return Stats{TotalPeers: len(f.peers), ActivePeers: len(f.peers)}, nil
}

func (f *FakeDevice) Apply(_ string, c Change) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return f.Err
	}
	for _, p := range c.Add {
		f.peers[p.PublicKey] = p.InnerIP
	}
	for _, p := range c.Update {
		f.peers[p.PublicKey] = p.InnerIP
	}
	for _, k := range c.Remove {
		delete(f.peers, k)
	}
	return nil
}
