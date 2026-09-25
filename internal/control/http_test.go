package control

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"gamenolag/internal/api"
)

type fakeBackend struct {
	registerOut RegisterOutput
	registerErr error
	authRelay   string
	authErr     error
	statusSeen  api.RelayStatus
	peers       []api.Peer
	cidrs       []string
	desiredErr  error
	reachKey    string
	reachPub    string
	reachOK     bool
	reachDetail string
	reachErr    error
}

func (f *fakeBackend) RegisterRelay(context.Context, RegisterInput) (RegisterOutput, error) {
	return f.registerOut, f.registerErr
}
func (f *fakeBackend) AuthenticateRelay(context.Context, string) (string, error) {
	return f.authRelay, f.authErr
}
func (f *fakeBackend) RecordStatus(_ context.Context, _ string, st api.RelayStatus) error {
	f.statusSeen = st
	return nil
}
func (f *fakeBackend) RecordReachability(_ context.Context, key, pubkey string, ok bool, detail string) error {
	f.reachKey, f.reachPub, f.reachOK, f.reachDetail = key, pubkey, ok, detail
	return f.reachErr
}
func (f *fakeBackend) DesiredState(context.Context, string) ([]api.Peer, []string, error) {
	return f.peers, f.cidrs, f.desiredErr
}

func post(t *testing.T, h http.Handler, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRegisterReturnsTokenAndAssignment(t *testing.T) {
	b := &fakeBackend{registerOut: RegisterOutput{
		RelayID: "relay-1", RelayToken: "tok", InnerSubnet: "10.77.0.0/16", InnerIP: "10.77.0.1/16",
	}}
	rec := post(t, NewServer(b, 10), "/v1/relay/register", "", api.RegisterRequest{
		ContributorKey: "GNL-AAAA-BBBB-CCCC-DDDD",
		PublicKey:      "pk", Endpoint: "203.0.113.10:51820",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	var out api.RegisterResponse
	json.Unmarshal(rec.Body.Bytes(), &out)
	if out.RelayToken != "tok" || out.RelayID != "relay-1" {
		t.Errorf("out = %+v", out)
	}
	if out.ListenPort != 51820 {
		t.Errorf("ListenPort = %d, want the 51820 default", out.ListenPort)
	}
}

func TestRegisterRejectsUnknownKeyAsForbidden(t *testing.T) {
	b := &fakeBackend{registerErr: ErrUnknownKey}
	rec := post(t, NewServer(b, 10), "/v1/relay/register", "", api.RegisterRequest{
		ContributorKey: "GNL-ZZZZ-ZZZZ-ZZZZ-ZZZZ", PublicKey: "pk", Endpoint: "203.0.113.10:51820",
	})
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestRegisterRequiresMandatoryFields(t *testing.T) {
	cases := []struct {
		name string
		req  api.RegisterRequest
	}{
		{"no key", api.RegisterRequest{PublicKey: "pk", Endpoint: "203.0.113.10:51820"}},
		{"no public key", api.RegisterRequest{ContributorKey: "GNL-A", Endpoint: "203.0.113.10:51820"}},
		{"no endpoint", api.RegisterRequest{ContributorKey: "GNL-A", PublicKey: "pk"}},
		{"endpoint without port", api.RegisterRequest{ContributorKey: "GNL-A", PublicKey: "pk", Endpoint: "203.0.113.10"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := post(t, NewServer(&fakeBackend{}, 10), "/v1/relay/register", "", c.req)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
		})
	}
}

func TestSyncRequiresBearerToken(t *testing.T) {
	b := &fakeBackend{authErr: ErrUnauthorized}
	rec := post(t, NewServer(b, 10), "/v1/relay/sync", "", api.SyncRequest{})
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestSyncReturnsDesiredState(t *testing.T) {
	b := &fakeBackend{
		authRelay: "relay-1",
		peers:     []api.Peer{{PublicKey: "dev-pk", InnerIP: "10.77.0.5/32"}},
		cidrs:     []string{"20.24.48.0/20"},
	}
	rec := post(t, NewServer(b, 10), "/v1/relay/sync", "tok", api.SyncRequest{
		Status: api.RelayStatus{ActivePeers: 2, TotalPeers: 4, RxBytes: 10, TxBytes: 20},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	var out api.SyncResponse
	json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Peers) != 1 || out.Peers[0].InnerIP != "10.77.0.5/32" {
		t.Errorf("peers = %+v", out.Peers)
	}
	if len(out.GameCIDRs) != 1 || out.PollSecs != 10 {
		t.Errorf("out = %+v", out)
	}
	if b.statusSeen.ActivePeers != 2 {
		t.Errorf("status was not recorded: %+v", b.statusSeen)
	}
}

// A malformed peer must never be handed to an agent: the agent would pass it to
// the kernel, and a peer with a wide AllowedIPs can receive another client's
// return traffic.
func TestSyncDropsInvalidPeers(t *testing.T) {
	b := &fakeBackend{
		authRelay: "relay-1",
		peers: []api.Peer{
			{PublicKey: "good", InnerIP: "10.77.0.5/32"},
			{PublicKey: "wide", InnerIP: "10.77.0.0/24"},
			{PublicKey: "empty", InnerIP: ""},
		},
	}
	rec := post(t, NewServer(b, 10), "/v1/relay/sync", "tok", api.SyncRequest{})
	var out api.SyncResponse
	json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Peers) != 1 || out.Peers[0].PublicKey != "good" {
		t.Errorf("peers = %+v, want only the valid one", out.Peers)
	}
}

func TestRejectsWrongMethod(t *testing.T) {
	h := NewServer(&fakeBackend{}, 10)
	req := httptest.NewRequest(http.MethodGet, "/v1/relay/sync", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

func TestHealthz(t *testing.T) {
	h := NewServer(&fakeBackend{}, 10)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestReachabilityRecordsTheVerdict(t *testing.T) {
	b := &fakeBackend{}
	rec := post(t, NewServer(b, 10), "/v1/relay/reachability", "", api.ReachabilityReport{
		ContributorKey: "GNL-AAAA-BBBB-CCCC-DDDD",
		RelayPublicKey: "relay-pk", Reachable: true,
	})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body %s", rec.Code, rec.Body)
	}
	if b.reachPub != "relay-pk" || !b.reachOK {
		t.Errorf("backend saw pubkey=%q ok=%v", b.reachPub, b.reachOK)
	}
}

// A contributor may report on their own relays and nobody else's. Without that,
// anyone could mark a stranger's relay unreachable and take it out of service.
func TestReachabilityRejectsARelayTheKeyDoesNotOwn(t *testing.T) {
	b := &fakeBackend{reachErr: ErrUnknownKey}
	rec := post(t, NewServer(b, 10), "/v1/relay/reachability", "", api.ReachabilityReport{
		ContributorKey: "GNL-ZZZZ-ZZZZ-ZZZZ-ZZZZ", RelayPublicKey: "someone-elses", Reachable: false,
	})
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestReachabilityRequiresBothFields(t *testing.T) {
	for _, r := range []api.ReachabilityReport{
		{RelayPublicKey: "pk"},
		{ContributorKey: "GNL-A"},
	} {
		rec := post(t, NewServer(&fakeBackend{}, 10), "/v1/relay/reachability", "", r)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d for %+v, want 400", rec.Code, r)
		}
	}
}
