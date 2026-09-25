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
		}
	}
	if err := sc.Err(); err != nil {
		return RelayState{}, err
	}
	if st.InnerSubnet == "" || st.WAN == "" || st.Port == "" || st.SetName == "" {
		return RelayState{}, fmt.Errorf("agent: %s is incomplete", path)
	}
	return st, nil
}

// FirewallRules returns the egress rules this relay must hold, as iptables
// argument vectors. Pure, so the specs can be asserted in a test without root.
//
// They must match the installer's rules exactly. A rule that differs by one
// argument is a second rule, not the same one: iptables -C compares the whole
// spec, so a mismatch means the agent inserts a duplicate on every poll.
func FirewallRules(st RelayState) [][]string {
	return [][]string{
		{"-t", "filter", "FORWARD", "-s", st.InnerSubnet, "-m", "set", "--match-set", st.SetName, "dst", "-j", "ACCEPT"},
		{"-t", "filter", "FORWARD", "-d", st.InnerSubnet, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"},
		{"-t", "nat", "POSTROUTING", "-s", st.InnerSubnet, "-o", st.WAN, "-j", "MASQUERADE"},
		{"-t", "mangle", "FORWARD", "-p", "tcp", "--tcp-flags", "SYN,RST", "SYN", "-j", "TCPMSS", "--clamp-mss-to-pmtu"},
		{"-t", "filter", "INPUT", "-p", "udp", "--dport", st.Port, "-j", "ACCEPT"},
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
	for _, spec := range FirewallRules(st) {
		check := append([]string{}, spec...)
		// -C takes the same spec as -A but with the chain kept in place.
		check = append(check[:2], append([]string{"-C"}, check[2:]...)...)
		if err := run(check...); err == nil {
			continue // already present
		}
		insert := append([]string{}, spec...)
		insert = append(insert[:2], append([]string{"-I"}, insert[2:]...)...)
		if err := run(insert...); err != nil {
			return fmt.Errorf("agent: reassert rule %v: %w", spec, err)
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
