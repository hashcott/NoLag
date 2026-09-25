package api

import (
	"encoding/json"
	"testing"
)

func TestPeerJSONFieldNames(t *testing.T) {
	b, err := json.Marshal(Peer{PublicKey: "abc=", InnerIP: "10.77.0.5/32"})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"public_key":"abc=","inner_ip":"10.77.0.5/32"}`
	if string(b) != want {
		t.Errorf("Peer JSON = %s, want %s", b, want)
	}
}

func TestPeerValidateAcceptsWellFormed(t *testing.T) {
	p := Peer{PublicKey: "TGlzdGVuIHRvIHRoZSBieXRlcyBoZXJlIG9r", InnerIP: "10.77.0.5/32"}
	if err := p.Validate(); err != nil {
		t.Errorf("Validate = %v, want nil", err)
	}
}

func TestPeerValidateRejectsBadInput(t *testing.T) {
	cases := []struct {
		name string
		peer Peer
	}{
		{"empty public key", Peer{PublicKey: "", InnerIP: "10.77.0.5/32"}},
		{"empty inner ip", Peer{PublicKey: "abc=", InnerIP: ""}},
		{"inner ip without prefix", Peer{PublicKey: "abc=", InnerIP: "10.77.0.5"}},
		{"inner ip not a /32", Peer{PublicKey: "abc=", InnerIP: "10.77.0.0/24"}},
		{"inner ip unparseable", Peer{PublicKey: "abc=", InnerIP: "not-an-ip/32"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.peer.Validate(); err == nil {
				t.Errorf("Validate accepted %+v", c.peer)
			}
		})
	}
}

func TestSyncResponseRoundTrip(t *testing.T) {
	in := SyncResponse{
		Peers:     []Peer{{PublicKey: "k1", InnerIP: "10.77.0.5/32"}},
		GameCIDRs: []string{"20.24.48.0/20", "52.139.208.0/20"},
		PollSecs:  10,
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out SyncResponse
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Peers) != 1 || out.Peers[0].InnerIP != "10.77.0.5/32" {
		t.Errorf("peers did not survive the round trip: %+v", out.Peers)
	}
	if len(out.GameCIDRs) != 2 || out.PollSecs != 10 {
		t.Errorf("out = %+v", out)
	}
}

func TestRegisterRequestJSONFieldNames(t *testing.T) {
	b, err := json.Marshal(RegisterRequest{
		ContributorKey: "GNL-A", PublicKey: "pk", Endpoint: "203.0.113.10:51820",
		Region: "sgp", Hostname: "vps-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(b, &m)
	for _, k := range []string{"contributor_key", "public_key", "endpoint", "region", "hostname"} {
		if _, ok := m[k]; !ok {
			t.Errorf("missing JSON field %q in %s", k, b)
		}
	}
}
