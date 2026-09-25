//go:build linuxroot

// These tests create and destroy real ipsets. See the note in
// internal/agent/firewall_root_test.go for why they exist and how to run them.
package ipsetsync

import (
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
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

func members(t *testing.T, set string) map[string]bool {
	t.Helper()
	out, err := exec.Command("ipset", "list", set).CombinedOutput()
	if err != nil {
		t.Fatalf("ipset list %s: %v\n%s", set, err, out)
	}
	m := map[string]bool{}
	var inMembers bool
	for _, l := range strings.Split(string(out), "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "Members:") {
			inMembers = true
			continue
		}
		if inMembers && l != "" {
			m[l] = true
		}
	}
	return m
}

func TestApplyCreatesTheSetTheFirewallExpects(t *testing.T) {
	requireHostWeMayBreak(t)
	const set = "gnl-test-apply"
	t.Cleanup(func() { _ = exec.Command("ipset", "destroy", set).Run() })

	cidrs := Normalise([]string{"203.0.113.0/24", "198.51.100.0/22"})
	if err := ApplyWithIpset(set, cidrs); err != nil {
		t.Fatalf("apply: %v", err)
	}
	got := members(t, set)
	for _, c := range cidrs {
		if !got[c] {
			t.Errorf("%s is not in the set; it holds %v", c, got)
		}
	}
}

func TestASecondApplyReplacesRatherThanAccumulates(t *testing.T) {
	// The profile shrinks as well as grows. A range that was removed upstream but
	// left in the set keeps a contributor's relay carrying traffic it is no
	// longer meant to.
	requireHostWeMayBreak(t)
	const set = "gnl-test-swap"
	t.Cleanup(func() { _ = exec.Command("ipset", "destroy", set).Run() })

	if err := ApplyWithIpset(set, Normalise([]string{"203.0.113.0/24", "198.51.100.0/24"})); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if err := ApplyWithIpset(set, Normalise([]string{"203.0.113.0/24"})); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	got := members(t, set)
	if got["198.51.100.0/24"] {
		t.Errorf("a range removed from the profile is still in the set: %v", got)
	}
	if !got["203.0.113.0/24"] {
		t.Errorf("the surviving range was dropped: %v", got)
	}
}

func TestTheTemporarySetIsNotLeftBehind(t *testing.T) {
	// The swap goes through a temporary set. One left behind on every publish
	// eventually fills the kernel's set table, and the failure lands on a
	// contributor's machine days later with no obvious cause.
	requireHostWeMayBreak(t)
	const set = "gnl-test-tmp"
	t.Cleanup(func() { _ = exec.Command("ipset", "destroy", set).Run() })

	before := listSetNames(t)
	for i := 0; i < 3; i++ {
		if err := ApplyWithIpset(set, Normalise([]string{"203.0.113.0/24"})); err != nil {
			t.Fatalf("apply %d: %v", i, err)
		}
	}
	after := listSetNames(t)
	for name := range after {
		if !before[name] && name != set {
			t.Errorf("left behind the set %q", name)
		}
	}
}

func listSetNames(t *testing.T) map[string]bool {
	t.Helper()
	out, err := exec.Command("ipset", "list", "-n").CombinedOutput()
	if err != nil {
		t.Fatalf("ipset list -n: %v\n%s", err, out)
	}
	m := map[string]bool{}
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l != "" {
			m[l] = true
		}
	}
	return m
}

// TestIpsetNeverWidensWhatWasAsked checks the property the allowlist rests on:
// whatever ipset does to an entry on the way in, the set must not end up
// permitting an address the profile did not name.
//
// ipset does rewrite entries. A prefix with bits set below its length is stored
// masked, so 203.0.113.5/24 becomes 203.0.113.0/24. That is the same range, so
// nothing widens — which is why usable() lets it through rather than dropping a
// range a relay is meant to carry. This test is here to notice if that ever
// stops being true.
func TestIpsetNeverWidensWhatWasAsked(t *testing.T) {
	requireHostWeMayBreak(t)
	const set = "gnl-test-mask"
	t.Cleanup(func() { _ = exec.Command("ipset", "destroy", set).Run() })

	if err := ApplyWithIpset(set, Normalise([]string{"203.0.113.5/24"})); err != nil {
		t.Fatalf("apply: %v", err)
	}
	got := members(t, set)
	if len(got) != 1 {
		t.Fatalf("one entry went in, %d came out: %v", len(got), got)
	}
	for m := range got {
		_, stored, err := net.ParseCIDR(m)
		if err != nil {
			t.Fatalf("ipset stored something that is not a CIDR: %q", m)
		}
		_, asked, _ := net.ParseCIDR("203.0.113.5/24")
		if stored.String() != asked.String() {
			t.Fatalf("asked for %s, the kernel holds %s", asked, stored)
		}
		t.Logf("ipset rewrote 203.0.113.5/24 to %s, the same range", m)
	}
}

// TestAnEntryThatWouldBeReadAsAnOptionIsDropped is the rejection that matters.
//
// ipset takes options on the same command line as the entry, so an entry
// starting with a dash is read as one. Dropping it keeps the rest of the
// allowlist working; letting it through aborts the rebuild and leaves a relay
// forwarding nothing.
func TestAnEntryThatWouldBeReadAsAnOptionIsDropped(t *testing.T) {
	requireHostWeMayBreak(t)
	const set = "gnl-test-option"
	t.Cleanup(func() { _ = exec.Command("ipset", "destroy", set).Run() })

	in := []string{"203.0.113.0/24", "-exist", "-!"}
	kept := Normalise(in)
	for _, c := range kept {
		if strings.HasPrefix(c, "-") {
			t.Fatalf("Normalise kept %q, which ipset reads as an option", c)
		}
	}
	if err := ApplyWithIpset(set, kept); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := members(t, set); !got["203.0.113.0/24"] || len(got) != 1 {
		t.Fatalf("set holds %v", got)
	}
}
