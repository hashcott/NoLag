package agent

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// RelayState is what the installer recorded about this host, read back so the
// agent can re-assert the egress policy it is responsible for.
type RelayState struct {
	InnerSubnet string
	WAN         string
	Port        string
	SetName     string
	RateLimit   string
	RateBurst   string
}

// LoadRelayState reads /etc/gnl/relay.state, written by the installer.
func LoadRelayState(path string) (RelayState, error) {
	f, err := os.Open(path)
	if err != nil {
		return RelayState{}, err
	}
	defer f.Close()

	st := RelayState{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if !ok {
			continue
		}
		switch k {
		case "INNER_SUBNET":
			st.InnerSubnet = v
		case "WAN":
			st.WAN = v
		case "PORT":
			st.Port = v
		case "SETNAME":
			st.SetName = v
		case "RATE_LIMIT":
			st.RateLimit = v
		case "RATE_BURST":
			st.RateBurst = v
		}
	}
	if err := sc.Err(); err != nil {
		return RelayState{}, err
	}
	if st.InnerSubnet == "" || st.WAN == "" || st.Port == "" || st.SetName == "" {
		return RelayState{}, fmt.Errorf("agent: %s is incomplete", path)
	}
	// Written by installers from before the per-session cap existed.
	if st.RateLimit == "" {
		st.RateLimit = "64kb/s"
	}
	if st.RateBurst == "" {
		st.RateBurst = "256kb"
	}
	return st, nil
}

// Rule is one iptables rule and where it has to sit in its chain.
//
// Position is not cosmetic. The per-session caps DROP above the ACCEPTs, so a
// cap appended below an ACCEPT never matches and the cap silently does nothing.
// Encoding it here means the agent cannot restore a rule into the wrong place,
// which iteration order alone would decide.
type Rule struct {
	Spec  []string
	AtTop bool // -I (position 1) rather than -A
}

// FirewallRules returns the rules this relay must hold, in chain order.
//
// They must match the installer's exactly. iptables -C compares the whole spec,
// so a rule differing by one argument is a different rule: the check never
// matches and the agent inserts a duplicate on every poll.
func FirewallRules(st RelayState) []Rule {
	cap := func(dir, mode, name string) []string {
		return []string{"-t", "filter", "FORWARD", dir, st.InnerSubnet,
			"-m", "hashlimit",
			"--hashlimit-above", st.RateLimit, "--hashlimit-burst", st.RateBurst,
			"--hashlimit-mode", mode, "--hashlimit-name", name,
			"--hashlimit-htable-expire", "60000", "-j", "DROP"}
	}
	return []Rule{
		// Caps first, at the top of FORWARD.
		{Spec: cap("-s", "srcip", "gnl-up"), AtTop: true},
		{Spec: cap("-d", "dstip", "gnl-down"), AtTop: true},
		// Then what is permitted.
		{Spec: []string{"-t", "filter", "FORWARD", "-s", st.InnerSubnet, "-m", "set", "--match-set", st.SetName, "dst", "-j", "ACCEPT"}},
		{Spec: []string{"-t", "filter", "FORWARD", "-d", st.InnerSubnet, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"}},
		{Spec: []string{"-t", "nat", "POSTROUTING", "-s", st.InnerSubnet, "-o", st.WAN, "-j", "MASQUERADE"}},
		{Spec: []string{"-t", "mangle", "FORWARD", "-p", "tcp", "--tcp-flags", "SYN,RST", "SYN", "-j", "TCPMSS", "--clamp-mss-to-pmtu"}},
		{Spec: []string{"-t", "filter", "INPUT", "-p", "udp", "--dport", st.Port, "-j", "ACCEPT"}},
	}
}

// ReassertFirewall puts the FORWARD policy back to DROP and re-adds any missing
// rule.
//
// This exists because the installer is not the owner of that policy — it sets it
// once and exits. Anything afterwards that runs `iptables -P FORWARD ACCEPT`
// (the usual internet answer to "my container networking broke"), or a firewall
// tool that rewrites the chain, turns this relay into an open forwarder on the
// contributor's own IP address: the five rules are all ACCEPT, so they block
// nothing on their own, MASQUERADE stays, and the tunnel stays up. No error, no
// log line, and the abuse report goes to the contributor rather than to us.
//
// Every operation is idempotent, so the steady state costs one -C per rule.
func ReassertFirewall(st RelayState, run func(args ...string) error) error {
	if err := run("-P", "FORWARD", "DROP"); err != nil {
		return fmt.Errorf("agent: reassert FORWARD policy: %w", err)
	}
	for _, r := range FirewallRules(st) {
		check := append([]string{}, r.Spec[:2]...)
		check = append(check, "-C")
		check = append(check, r.Spec[2:]...)
		if err := run(check...); err == nil {
			continue // already present
		}
		op := "-A"
		if r.AtTop {
			op = "-I"
		}
		add := append([]string{}, r.Spec[:2]...)
		add = append(add, op)
		add = append(add, r.Spec[2:]...)
		if err := run(add...); err != nil {
			return fmt.Errorf("agent: reassert rule %v: %w", r.Spec, err)
		}
	}
	return nil
}

// RunIptables executes iptables with the given arguments.
func RunIptables(args ...string) error {
	out, err := exec.Command("iptables", args...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return err
		}
		return fmt.Errorf("%s: %s", err, msg)
	}
	return nil
}
