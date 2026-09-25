package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRelayState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay.state")
	os.WriteFile(path, []byte("INNER_SUBNET=10.77.0.0/16\nWAN=eth0\nPORT=51820\nIFACE=wg0\nSETNAME=gnl-games\n"), 0o600)

	st, err := LoadRelayState(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.InnerSubnet != "10.77.0.0/16" || st.WAN != "eth0" || st.Port != "51820" || st.SetName != "gnl-games" {
		t.Errorf("state = %+v", st)
	}
}

func TestLoadRelayStateRejectsIncomplete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay.state")
	os.WriteFile(path, []byte("WAN=eth0\nPORT=51820\n"), 0o600)
	if _, err := LoadRelayState(path); err == nil {
		t.Error("an incomplete state file must be an error: acting on half of it would " +
			"assert the wrong rules")
	}
}

// The policy is the whole control. The five rules are all ACCEPT, so with the
// policy at ACCEPT they block nothing and the relay forwards anywhere.
func TestReassertAlwaysSetsPolicyFirst(t *testing.T) {
	var calls [][]string
	run := func(args ...string) error {
		calls = append(calls, args)
		return nil
	}
	st := RelayState{InnerSubnet: "10.77.0.0/16", WAN: "eth0", Port: "51820", SetName: "gnl-games"}
	if err := ReassertFirewall(st, run); err != nil {
		t.Fatal(err)
	}
	if len(calls) == 0 {
		t.Fatal("nothing was run")
	}
	first := strings.Join(calls[0], " ")
	if first != "-P FORWARD DROP" {
		t.Errorf("first call = %q, want the policy set before anything else", first)
	}
}

// A rule already present must not be inserted again, or every poll stacks a
// duplicate and the chain grows without bound for the life of the campaign.
func TestReassertDoesNotDuplicateExistingRules(t *testing.T) {
	var inserts int
	run := func(args ...string) error {
		for _, a := range args {
			if a == "-I" {
				inserts++
			}
		}
		return nil // -C succeeds: every rule is already there
	}
	st := RelayState{InnerSubnet: "10.77.0.0/16", WAN: "eth0", Port: "51820", SetName: "gnl-games"}
	for i := 0; i < 5; i++ {
		if err := ReassertFirewall(st, run); err != nil {
			t.Fatal(err)
		}
	}
	if inserts != 0 {
		t.Errorf("%d inserts across five passes with every rule present; want 0", inserts)
	}
}

// A missing rule must be restored.
func TestReassertRestoresAMissingRule(t *testing.T) {
	var inserted [][]string
	run := func(args ...string) error {
		for i, a := range args {
			if a == "-C" {
				// Only the MASQUERADE rule is missing.
				if strings.Contains(strings.Join(args[i:], " "), "MASQUERADE") {
					return errors.New("no such rule")
				}
				return nil
			}
			if a == "-I" {
				inserted = append(inserted, args)
			}
		}
		return nil
	}
	st := RelayState{InnerSubnet: "10.77.0.0/16", WAN: "eth0", Port: "51820", SetName: "gnl-games"}
	if err := ReassertFirewall(st, run); err != nil {
		t.Fatal(err)
	}
	if len(inserted) != 1 {
		t.Fatalf("inserted %d rules, want exactly the missing one", len(inserted))
	}
	if !strings.Contains(strings.Join(inserted[0], " "), "MASQUERADE") {
		t.Errorf("inserted %v, want the MASQUERADE rule", inserted[0])
	}
}

// The specs the agent asserts must match the installer's byte for byte. iptables
// -C compares the whole spec, so a rule differing by one argument is a different
// rule: the check never matches and the agent inserts a duplicate every poll.
func TestFirewallRulesMatchTheInstaller(t *testing.T) {
	script, err := os.ReadFile("../../deploy/relay-v1.sh")
	if err != nil {
		t.Skipf("installer not readable: %v", err)
	}
	text := string(script)
	st := RelayState{InnerSubnet: "$INNER_SUBNET", WAN: "$WAN", Port: "$PORT", SetName: "$SETNAME"}
	for _, spec := range FirewallRules(st) {
		// Rebuild the tail of the add_rule line the installer uses.
		tail := strings.Join(spec[3:], " ")
		// The installer quotes its variables; compare on the distinguishing parts.
		needle := strings.ReplaceAll(tail, "$INNER_SUBNET", `"$INNER_SUBNET"`)
		needle = strings.ReplaceAll(needle, "$SETNAME", `"$SETNAME"`)
		needle = strings.ReplaceAll(needle, "$WAN", `"$WAN"`)
		needle = strings.ReplaceAll(needle, "$PORT", `"$PORT"`)
		if !strings.Contains(text, needle) {
			t.Errorf("the agent asserts a rule the installer never adds:\n  agent:     %s\n"+
				"  not found in deploy/relay-v1.sh", needle)
		}
	}
}
