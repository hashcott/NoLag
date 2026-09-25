package agent

import (
	"context"
	"errors"
	"testing"

	"gamenolag/internal/api"
	"gamenolag/internal/ipsetsync"
	"gamenolag/internal/wgsync"
)

type fakeCP struct {
	resp       api.SyncResponse
	err        error
	statusSeen api.RelayStatus
	calls      int
}

func (f *fakeCP) Sync(_ context.Context, st api.RelayStatus) (api.SyncResponse, error) {
	f.calls++
	f.statusSeen = st
	return f.resp, f.err
}

func TestRunOnceAppliesDesiredPeers(t *testing.T) {
	dev := wgsync.NewFakeDevice()
	cp := &fakeCP{resp: api.SyncResponse{
		Peers: []api.Peer{
			{PublicKey: "k1", InnerIP: "10.77.0.5/32"},
			{PublicKey: "k2", InnerIP: "10.77.0.6/32"},
		},
		GameCIDRs: []string{"20.24.48.0/20"},
	}}
	var appliedCIDRs []string
	apply := func(_ string, sorted []string) error { appliedCIDRs = sorted; return nil }

	err := RunOnce(context.Background(), dev, cp, ipsetsync.New("gnl-games"), "wg0", apply, nil)
	if err != nil {
		t.Fatal(err)
	}

	peers, _ := dev.Peers("wg0")
	if len(peers) != 2 {
		t.Errorf("device holds %d peers, want 2", len(peers))
	}
	if len(appliedCIDRs) != 1 || appliedCIDRs[0] != "20.24.48.0/20" {
		t.Errorf("applied CIDRs = %v, want [20.24.48.0/20]", appliedCIDRs)
	}
}

func TestRunOnceIsIdempotent(t *testing.T) {
	dev := wgsync.NewFakeDevice()
	cp := &fakeCP{resp: api.SyncResponse{
		Peers:     []api.Peer{{PublicKey: "k1", InnerIP: "10.77.0.5/32"}},
		GameCIDRs: []string{"20.24.48.0/20"},
	}}
	sets := ipsetsync.New("gnl-games")
	applyCalls := 0
	apply := func(string, []string) error { applyCalls++; return nil }

	for i := 0; i < 3; i++ {
		if err := RunOnce(context.Background(), dev, cp, sets, "wg0", apply, nil); err != nil {
			t.Fatal(err)
		}
	}

	peers, _ := dev.Peers("wg0")
	if len(peers) != 1 {
		t.Errorf("device holds %d peers after three passes, want 1", len(peers))
	}
	if applyCalls != 1 {
		t.Errorf("ipset applied %d times for an unchanged list, want 1", applyCalls)
	}
}

func TestRunOnceRemovesRevokedPeer(t *testing.T) {
	dev := wgsync.NewFakeDevice()
	dev.Apply("wg0", wgsync.Change{Add: []api.Peer{
		{PublicKey: "revoked", InnerIP: "10.77.0.9/32"},
	}})

	cp := &fakeCP{resp: api.SyncResponse{Peers: []api.Peer{{PublicKey: "k1", InnerIP: "10.77.0.5/32"}}}}
	if err := RunOnce(context.Background(), dev, cp, ipsetsync.New("s"), "wg0", func(string, []string) error { return nil }, nil); err != nil {
		t.Fatal(err)
	}

	peers, _ := dev.Peers("wg0")
	for _, p := range peers {
		if p.PublicKey == "revoked" {
			t.Fatal("a peer removed from desired state is still on the device")
		}
	}
}

// A control plane outage must leave the running peers alone. Players mid-match
// are the whole reason this rule exists.
func TestRunOnceLeavesPeersAloneWhenControlPlaneFails(t *testing.T) {
	dev := wgsync.NewFakeDevice()
	dev.Apply("wg0", wgsync.Change{Add: []api.Peer{
		{PublicKey: "playing", InnerIP: "10.77.0.5/32"},
	}})

	cp := &fakeCP{err: errors.New("connection refused")}
	err := RunOnce(context.Background(), dev, cp, ipsetsync.New("s"), "wg0", func(string, []string) error { return nil }, nil)
	if err == nil {
		t.Error("RunOnce must report the failure so it can be logged")
	}

	peers, _ := dev.Peers("wg0")
	if len(peers) != 1 || peers[0].PublicKey != "playing" {
		t.Errorf("peers = %+v; a control plane outage must not disconnect anyone", peers)
	}
}

func TestRunOnceReportsStatusUpward(t *testing.T) {
	dev := wgsync.NewFakeDevice()
	dev.Apply("wg0", wgsync.Change{Add: []api.Peer{
		{PublicKey: "k1", InnerIP: "10.77.0.5/32"},
		{PublicKey: "k2", InnerIP: "10.77.0.6/32"},
	}})
	cp := &fakeCP{}

	if err := RunOnce(context.Background(), dev, cp, ipsetsync.New("s"), "wg0", func(string, []string) error { return nil }, nil); err != nil {
		t.Fatal(err)
	}
	if cp.statusSeen.TotalPeers != 2 {
		t.Errorf("reported TotalPeers = %d, want 2", cp.statusSeen.TotalPeers)
	}
	if cp.statusSeen.AgentVersion == "" {
		t.Error("reported AgentVersion is empty")
	}
}

// An ipset failure must not stop peers being reconciled: the two halves fail
// independently, and a stale allowlist is better than nobody being able to connect.
func TestRunOncePeersSucceedEvenIfIPSetFails(t *testing.T) {
	dev := wgsync.NewFakeDevice()
	cp := &fakeCP{resp: api.SyncResponse{
		Peers:     []api.Peer{{PublicKey: "k1", InnerIP: "10.77.0.5/32"}},
		GameCIDRs: []string{"20.24.48.0/20"},
	}}
	apply := func(string, []string) error { return errors.New("ipset: command not found") }

	err := RunOnce(context.Background(), dev, cp, ipsetsync.New("s"), "wg0", apply, nil)
	if err == nil {
		t.Error("RunOnce must report the ipset failure")
	}
	peers, _ := dev.Peers("wg0")
	if len(peers) != 1 {
		t.Errorf("device holds %d peers, want 1: an ipset failure must not block peer reconcile", len(peers))
	}
}
