package wgsync

import (
	"testing"

	"gamenolag/internal/api"
)

// FakeDevice is the in-memory Device used by this test and by the agent loop
// test in cmd/gnl-agent. It lives in the package under test so both can use it.
func TestFakeDeviceRoundTrip(t *testing.T) {
	d := NewFakeDevice()

	got, err := d.Peers("wg0")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("a fresh device has %d peers, want 0", len(got))
	}

	err = d.Apply("wg0", Change{Add: []api.Peer{
		{PublicKey: "k1", InnerIP: "10.77.0.5/32"},
		{PublicKey: "k2", InnerIP: "10.77.0.6/32"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ = d.Peers("wg0"); len(got) != 2 {
		t.Fatalf("after Add there are %d peers, want 2", len(got))
	}

	if err := d.Apply("wg0", Change{Update: []api.Peer{{PublicKey: "k1", InnerIP: "10.77.0.9/32"}}}); err != nil {
		t.Fatal(err)
	}
	got, _ = d.Peers("wg0")
	for _, p := range got {
		if p.PublicKey == "k1" && p.InnerIP != "10.77.0.9/32" {
			t.Errorf("k1 inner IP = %s, want 10.77.0.9/32", p.InnerIP)
		}
	}

	if err := d.Apply("wg0", Change{Remove: []string{"k2"}}); err != nil {
		t.Fatal(err)
	}
	if got, _ = d.Peers("wg0"); len(got) != 1 {
		t.Fatalf("after Remove there are %d peers, want 1", len(got))
	}
}

// Applying the diff between two states must land exactly on the desired state.
// This is the property the whole reconcile loop rests on.
func TestApplyDiffConverges(t *testing.T) {
	d := NewFakeDevice()
	d.Apply("wg0", Change{Add: []api.Peer{
		{PublicKey: "keep", InnerIP: "10.77.0.1/32"},
		{PublicKey: "move", InnerIP: "10.77.0.2/32"},
		{PublicKey: "drop", InnerIP: "10.77.0.3/32"},
	}})

	desired := []api.Peer{
		{PublicKey: "keep", InnerIP: "10.77.0.1/32"},
		{PublicKey: "move", InnerIP: "10.77.0.9/32"},
		{PublicKey: "new", InnerIP: "10.77.0.4/32"},
	}

	current, _ := d.Peers("wg0")
	if err := d.Apply("wg0", Diff(current, desired)); err != nil {
		t.Fatal(err)
	}

	after, _ := d.Peers("wg0")
	if len(after) != len(desired) {
		t.Fatalf("after reconcile there are %d peers, want %d", len(after), len(desired))
	}
	byKey := map[string]string{}
	for _, p := range after {
		byKey[p.PublicKey] = p.InnerIP
	}
	for _, want := range desired {
		if byKey[want.PublicKey] != want.InnerIP {
			t.Errorf("peer %s at %q, want %q", want.PublicKey, byKey[want.PublicKey], want.InnerIP)
		}
	}

	// A second reconcile against the same desired state must be a no-op.
	current, _ = d.Peers("wg0")
	if c := Diff(current, desired); !c.Empty() {
		t.Errorf("reconcile is not idempotent: second diff = %+v", c)
	}
}
