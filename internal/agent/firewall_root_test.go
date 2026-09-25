//go:build linuxroot

// These tests change the machine's firewall. They are behind a build tag and an
// environment variable so that they cannot run by accident: `go test ./...` on a
// developer's machine must never touch iptables.
//
//	docker run --rm --privileged -v "$PWD":/src -w /src golang:1.25-bookworm \
//	  sh -c 'apt-get update -qq && apt-get install -y -qq iptables ipset &&
//	         GNL_FIREWALL_TESTS=1 go test -tags linuxroot ./internal/agent/'
//
// They exist because everything here was written on macOS, where iptables and
// ipset do not exist. The unit tests prove the rules are the ones intended; only
// a real kernel proves the kernel accepts them.
package agent

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"gamenolag/internal/ipsetsync"
)

func requireHostWeMayBreak(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("not root")
	}
	if os.Getenv("GNL_FIREWALL_TESTS") != "1" {
		t.Skip("set GNL_FIREWALL_TESTS=1 on a host you are willing to destroy")
	}
}

func testState(t *testing.T) RelayState {
	t.Helper()
	return RelayState{
		InnerSubnet: "10.77.0.0/16",
		WAN:         wanInterface(t),
		Port:        "51820",
		SetName:     "gnl-test-games",
		RateLimit:   "64kb/s",
		RateBurst:   "256kb",
	}
}

// wanInterface picks an interface that exists, because MASQUERADE -o names one
// and iptables rejects a name it cannot find.
func wanInterface(t *testing.T) string {
	t.Helper()
	names, err := os.ReadDir("/sys/class/net")
	if err != nil {
		t.Fatalf("reading /sys/class/net: %v", err)
	}
	for _, n := range names {
		if n.Name() != "lo" {
			return n.Name()
		}
	}
	t.Skip("this host has no interface other than loopback")
	return ""
}

func iptablesDump(t *testing.T, table, chain string) []string {
	t.Helper()
	out, err := exec.Command("iptables", "-t", table, "-S", chain).CombinedOutput()
	if err != nil {
		t.Fatalf("iptables -t %s -S %s: %v\n%s", table, chain, err, out)
	}
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

// TestEveryRuleIsAcceptedAndFoundAgain is the one that matters.
//
// iptables -C compares the whole spec, so a rule that the kernel stores
// differently from how it was given — a module that normalises an argument, an
// option order the parser rewrites — is never found by the check, and the agent
// inserts a duplicate on every single poll until the chain is thousands of rules
// long. That cannot be seen from macOS at all.
func TestEveryRuleIsAcceptedAndFoundAgain(t *testing.T) {
	requireHostWeMayBreak(t)
	st := testState(t)

	// The allowlist rule names a set, and iptables refuses a set that does not
	// exist, so this also proves the two halves agree on the set's name and type.
	if err := ipsetsync.ApplyWithIpset(st.SetName, []string{"203.0.113.0/24"}); err != nil {
		t.Fatalf("creating the allowlist: %v", err)
	}
	t.Cleanup(func() { _ = exec.Command("ipset", "destroy", st.SetName).Run() })

	if err := ReassertFirewall(st, RunIptables); err != nil {
		t.Fatalf("first assert: %v", err)
	}
	t.Cleanup(func() {
		for _, r := range FirewallRules(st) {
			del := append([]string{}, r.Spec[:2]...)
			del = append(del, "-D")
			del = append(del, r.Spec[2:]...)
			_ = exec.Command("iptables", del...).Run()
		}
		_ = exec.Command("iptables", "-P", "FORWARD", "ACCEPT").Run()
	})

	for _, r := range FirewallRules(st) {
		check := append([]string{}, r.Spec[:2]...)
		check = append(check, "-C")
		check = append(check, r.Spec[2:]...)
		if out, err := exec.Command("iptables", check...).CombinedOutput(); err != nil {
			t.Errorf("the kernel did not store this rule as it was given, so -C will never\n"+
				"match it and the agent will duplicate it on every poll:\n  %v\n  %v\n  %s",
				r.Spec, err, strings.TrimSpace(string(out)))
		}
	}
}

func TestReassertingIsIdempotent(t *testing.T) {
	requireHostWeMayBreak(t)
	st := testState(t)
	st.SetName = "gnl-test-idem"

	if err := ipsetsync.ApplyWithIpset(st.SetName, []string{"203.0.113.0/24"}); err != nil {
		t.Fatalf("creating the allowlist: %v", err)
	}
	t.Cleanup(func() { _ = exec.Command("ipset", "destroy", st.SetName).Run() })

	if err := ReassertFirewall(st, RunIptables); err != nil {
		t.Fatalf("first assert: %v", err)
	}
	t.Cleanup(func() {
		for _, r := range FirewallRules(st) {
			del := append([]string{}, r.Spec[:2]...)
			del = append(del, "-D")
			del = append(del, r.Spec[2:]...)
			_ = exec.Command("iptables", del...).Run()
		}
		_ = exec.Command("iptables", "-P", "FORWARD", "ACCEPT").Run()
	})

	before := map[string][]string{}
	for _, c := range []string{"filter/FORWARD", "filter/INPUT", "nat/POSTROUTING", "mangle/FORWARD"} {
		p := strings.SplitN(c, "/", 2)
		before[c] = iptablesDump(t, p[0], p[1])
	}

	// The agent re-asserts on every poll, which is every few seconds for as long
	// as the relay is up.
	for i := 0; i < 3; i++ {
		if err := ReassertFirewall(st, RunIptables); err != nil {
			t.Fatalf("assert %d: %v", i+2, err)
		}
	}

	for c, want := range before {
		p := strings.SplitN(c, "/", 2)
		got := iptablesDump(t, p[0], p[1])
		if len(got) != len(want) {
			t.Errorf("%s grew from %d rules to %d across three re-asserts:\n%s",
				c, len(want), len(got), strings.Join(got, "\n"))
		}
	}
}

// TestTheEgressPolicyIsDropAfterAssert checks the property the whole allowlist
// rests on: anything not explicitly permitted is dropped. An ACCEPT policy makes
// every rule above it decoration, and the relay becomes an open proxy.
func TestTheEgressPolicyIsDropAfterAssert(t *testing.T) {
	requireHostWeMayBreak(t)
	st := testState(t)
	st.SetName = "gnl-test-policy"

	if err := ipsetsync.ApplyWithIpset(st.SetName, []string{"203.0.113.0/24"}); err != nil {
		t.Fatalf("creating the allowlist: %v", err)
	}
	t.Cleanup(func() { _ = exec.Command("ipset", "destroy", st.SetName).Run() })

	_ = exec.Command("iptables", "-P", "FORWARD", "ACCEPT").Run()
	if err := ReassertFirewall(st, RunIptables); err != nil {
		t.Fatalf("assert: %v", err)
	}
	t.Cleanup(func() {
		for _, r := range FirewallRules(st) {
			del := append([]string{}, r.Spec[:2]...)
			del = append(del, "-D")
			del = append(del, r.Spec[2:]...)
			_ = exec.Command("iptables", del...).Run()
		}
		_ = exec.Command("iptables", "-P", "FORWARD", "ACCEPT").Run()
	})

	for _, l := range iptablesDump(t, "filter", "FORWARD") {
		if strings.HasPrefix(l, "-P FORWARD ") {
			if l != "-P FORWARD DROP" {
				t.Fatalf("the egress policy is %q; the relay forwards anything", l)
			}
			return
		}
	}
	t.Fatal("no FORWARD policy line in the dump")
}
