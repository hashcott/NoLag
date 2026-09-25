// Command gnl-relaycheck answers the one question a relay cannot answer about
// itself: is its WireGuard port reachable from the outside world?
//
//	sudo gnl-relaycheck -endpoint 203.0.113.10:51820 -pubkey <relay public key>
//
// The host firewall being open proves nothing, because the VPS provider's own
// security group sits in front of it and is invisible from inside the machine.
// So this runs from somewhere else and tries a real handshake.
//
// Exit 0: reachable. Exit 1: not reachable. Exit 2: could not run the check.
package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

func main() {
	endpoint := flag.String("endpoint", "", "relay endpoint, host:port (required)")
	pubkey := flag.String("pubkey", "", "relay WireGuard public key (required)")
	iface := flag.String("iface", "gnlcheck0", "temporary interface name")
	wait := flag.Duration("wait", 12*time.Second, "how long to wait for a handshake")
	flag.Parse()

	if *endpoint == "" || *pubkey == "" {
		fmt.Fprintln(os.Stderr, "both -endpoint and -pubkey are required")
		os.Exit(2)
	}

	addr, err := net.ResolveUDPAddr("udp", *endpoint)
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve %s: %v\n", *endpoint, err)
		os.Exit(2)
	}
	peerKey, err := wgtypes.ParseKey(*pubkey)
	if err != nil {
		fmt.Fprintf(os.Stderr, "parse public key: %v\n", err)
		os.Exit(2)
	}
	ourKey, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		fmt.Fprintf(os.Stderr, "generate key: %v\n", err)
		os.Exit(2)
	}

	if out, err := exec.Command("ip", "link", "add", *iface, "type", "wireguard").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "create %s: %v: %s\n", *iface, err, out)
		fmt.Fprintln(os.Stderr, "this needs root and a kernel with WireGuard")
		os.Exit(2)
	}
	defer exec.Command("ip", "link", "del", *iface).Run()

	c, err := wgctrl.New()
	if err != nil {
		fmt.Fprintf(os.Stderr, "wgctrl: %v\n", err)
		os.Exit(2)
	}
	defer c.Close()

	keepalive := 5 * time.Second
	err = c.ConfigureDevice(*iface, wgtypes.Config{
		PrivateKey: &ourKey,
		Peers: []wgtypes.PeerConfig{{
			PublicKey:                   peerKey,
			Endpoint:                    addr,
			PersistentKeepaliveInterval: &keepalive,
			// An empty AllowedIPs is deliberate: no traffic is routed to this
			// peer. The handshake alone is the answer being looked for.
			ReplaceAllowedIPs: true,
		}},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "configure %s: %v\n", *iface, err)
		os.Exit(2)
	}
	if out, err := exec.Command("ip", "link", "set", *iface, "up").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "bring up %s: %v: %s\n", *iface, err, out)
		os.Exit(2)
	}

	fmt.Printf("handshaking with %s for up to %s...\n", *endpoint, *wait)
	deadline := time.Now().Add(*wait)
	for time.Now().Before(deadline) {
		dev, err := c.Device(*iface)
		if err == nil {
			for _, p := range dev.Peers {
				if !p.LastHandshakeTime.IsZero() {
					fmt.Printf("REACHABLE: handshake completed at %s\n",
						p.LastHandshakeTime.Format(time.RFC3339))
					os.Exit(0)
				}
			}
		}
		time.Sleep(500 * time.Millisecond)
	}

	fmt.Printf("NOT REACHABLE: no handshake from %s within %s\n", *endpoint, *wait)
	fmt.Println()
	fmt.Println("In order of likelihood:")
	fmt.Println("  1. UDP is blocked in the VPS provider's security group or cloud")
	fmt.Println("     firewall. The host's own iptables being open proves nothing;")
	fmt.Println("     the provider's rules sit in front of it.")
	fmt.Println("  2. The relay is not listening. On the relay: wg show")
	fmt.Println("  3. The public key does not match the relay's. On the relay:")
	fmt.Println("     wg pubkey < /etc/gnl/relay.key")
	fmt.Println("  4. The endpoint address is wrong, for example a private address")
	fmt.Println("     was registered on a machine that is behind NAT.")
	os.Exit(1)
}
