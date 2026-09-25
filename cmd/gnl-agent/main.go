// Command gnl-agent keeps a relay's WireGuard peers and egress allowlist in step
// with the control plane.
//
//	gnl-agent -control https://cp.example.com -token-file /etc/gnl/relay.token
//
// It runs as root because it configures a WireGuard interface and an ipset.
// It is open source for that reason: asking for root on somebody else's machine
// is only legitimate if they can read what they are running.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"gamenolag/internal/agent"
	"gamenolag/internal/ipsetsync"
	"gamenolag/internal/wgsync"
)

func main() {
	control := flag.String("control", "", "control plane base URL (required)")
	tokenFile := flag.String("token-file", "/etc/gnl/relay.token", "file holding this relay's token")
	iface := flag.String("iface", "wg0", "WireGuard interface name")
	setName := flag.String("ipset", "gnl-games", "ipset holding the egress allowlist")
	stateFile := flag.String("state-file", "/etc/gnl/relay.state", "what the installer recorded about this host")
	poll := flag.Duration("poll", 10*time.Second, "how often to sync")
	flag.Parse()

	if *control == "" {
		log.Fatal("-control is required")
	}
	if !strings.HasPrefix(*control, "https://") {
		log.Printf("WARNING: -control is not https. The relay token is a bearer token " +
			"and must not cross plain HTTP outside a local test.")
	}

	raw, err := os.ReadFile(*tokenFile)
	if err != nil {
		log.Fatalf("read %s: %v (run the installer first)", *tokenFile, err)
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		log.Fatalf("%s is empty", *tokenFile)
	}

	dev, err := wgsync.NewWGCtl()
	if err != nil {
		log.Fatalf("%v", err)
	}
	defer dev.Close()

	if _, err := dev.Peers(*iface); err != nil {
		log.Fatalf("cannot read interface %s: %v\n"+
			"The installer creates it; check: ip link show %s", *iface, err, *iface)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Printf("gnl-agent %s: syncing %s with %s every %s", agent.Version, *iface, *control, *poll)

	// Re-assert the egress policy each poll. Without a state file the agent cannot
	// know the rules this host installed, so it says so and carries on doing the
	// rest rather than refusing to run - but a relay in that condition has nobody
	// watching its forwarding policy, which is worth saying out loud.
	var reassert func() error
	if st, err := agent.LoadRelayState(*stateFile); err != nil {
		log.Printf("WARNING: %v: the egress firewall will not be re-asserted. "+
			"If anything resets the FORWARD policy, this relay becomes an open "+
			"forwarder on your address and nothing here will notice.", err)
	} else {
		reassert = func() error { return agent.ReassertFirewall(st, agent.RunIptables) }
	}

	agent.Loop(ctx, agent.Config{
		ControlURL: *control,
		Token:      token,
		Iface:      *iface,
		SetName:    *setName,
		Poll:       *poll,
	}, dev, agent.NewHTTPSyncer(*control, token), ipsetsync.ApplyWithIpset, reassert)
}
