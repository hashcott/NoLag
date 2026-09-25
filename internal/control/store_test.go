package control

import (
	"context"
	"errors"
	"os"
	"testing"

	"gamenolag/internal/api"
)

// openTestStore connects to the Postgres named by GNL_TEST_DSN and gives the
// test a clean schema. Without that variable the test is skipped rather than
// failing, so `go test ./...` works on a laptop with no database.
func openTestStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("GNL_TEST_DSN")
	if dsn == "" {
		t.Skip("GNL_TEST_DSN not set; skipping store tests")
	}
	ctx := context.Background()
	s, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	if _, err := s.pool.Exec(ctx,
		`DROP TABLE IF EXISTS peer_binding, device, relay, contributor_key, game_profile CASCADE;
		 DROP SEQUENCE IF EXISTS relay_octet_seq`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return s
}

func TestMigrateIsIdempotent(t *testing.T) {
	s := openTestStore(t)
	if err := s.Migrate(context.Background()); err != nil {
		t.Errorf("second Migrate: %v", err)
	}
}

func TestRegisterRelayAssignsTokenAndSubnet(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	key, err := s.CreateContributorKey(ctx)
	if err != nil {
		t.Fatal(err)
	}

	out, err := s.RegisterRelay(ctx, RegisterInput{
		ContributorKey: key,
		PublicKey:      "pubkey-one",
		Endpoint:       "203.0.113.10:51820",
		Region:         "sgp",
		Hostname:       "vps-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.RelayID == "" || out.RelayToken == "" {
		t.Fatalf("out = %+v, want a relay id and a token", out)
	}
	if out.InnerSubnet == "" || out.InnerIP == "" {
		t.Errorf("out = %+v, want an inner subnet and the relay's own address", out)
	}

	relayID, err := s.AuthenticateRelay(ctx, out.RelayToken)
	if err != nil {
		t.Fatalf("AuthenticateRelay with the issued token: %v", err)
	}
	if relayID != out.RelayID {
		t.Errorf("authenticated as %q, want %q", relayID, out.RelayID)
	}
}

func TestRegisterRelayRejectsUnknownKey(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	_, err := s.RegisterRelay(ctx, RegisterInput{
		ContributorKey: "GNL-ZZZZ-ZZZZ-ZZZZ-ZZZZ",
		PublicKey:      "pubkey-x",
		Endpoint:       "203.0.113.99:51820",
	})
	if !errors.Is(err, ErrUnknownKey) {
		t.Errorf("err = %v, want ErrUnknownKey", err)
	}
}

func TestRegisterRelayIsIdempotentForTheSamePublicKey(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	key, _ := s.CreateContributorKey(ctx)

	in := RegisterInput{ContributorKey: key, PublicKey: "pk-same", Endpoint: "203.0.113.10:51820"}
	first, err := s.RegisterRelay(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	// Re-running the installer must not strand the relay or create a second row.
	second, err := s.RegisterRelay(ctx, in)
	if err != nil {
		t.Fatalf("re-registering the same public key: %v", err)
	}
	if second.RelayID != first.RelayID {
		t.Errorf("relay id changed on re-register: %q then %q", first.RelayID, second.RelayID)
	}
	if second.RelayToken == first.RelayToken {
		t.Error("re-register returned the same token; it must be rotated, since the old one may be lost")
	}
	if _, err := s.AuthenticateRelay(ctx, first.RelayToken); !errors.Is(err, ErrUnauthorized) {
		t.Error("the previous token still authenticates after a re-register")
	}
}

func TestAuthenticateRelayRejectsBadToken(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if _, err := s.AuthenticateRelay(ctx, "not-a-token"); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("err = %v, want ErrUnauthorized", err)
	}
}

func TestRecordStatusMarksRelayUp(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	key, _ := s.CreateContributorKey(ctx)
	out, _ := s.RegisterRelay(ctx, RegisterInput{
		ContributorKey: key, PublicKey: "pk-status", Endpoint: "203.0.113.10:51820",
	})

	err := s.RecordStatus(ctx, out.RelayID, api.RelayStatus{
		ActivePeers: 3, TotalPeers: 5, RxBytes: 1000, TxBytes: 2000, AgentVersion: "test",
	})
	if err != nil {
		t.Fatal(err)
	}

	var status string
	var active int
	err = s.pool.QueryRow(ctx,
		`SELECT status, active_peers FROM relay WHERE id = $1`, out.RelayID).Scan(&status, &active)
	if err != nil {
		t.Fatal(err)
	}
	if active != 3 {
		t.Errorf("active_peers = %d, want 3", active)
	}
	if status != "up" {
		t.Errorf("status = %q, want up: a relay that syncs is reachable from us", status)
	}
}

func TestDesiredStateReturnsBoundPeersAndCIDRs(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	key, _ := s.CreateContributorKey(ctx)
	out, _ := s.RegisterRelay(ctx, RegisterInput{
		ContributorKey: key, PublicKey: "pk-desired", Endpoint: "203.0.113.10:51820",
	})

	_, err := s.pool.Exec(ctx,
		`INSERT INTO device (id, key_hash, wg_pubkey) VALUES ('dev-1', $1, 'device-pk-1')`, Hash(key))
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO peer_binding (device_id, relay_id, inner_ip) VALUES ('dev-1', $1, '10.77.0.5/32')`, out.RelayID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO game_profile (version, cidrs) VALUES (1, ARRAY['20.24.48.0/20','52.139.208.0/20'])`)
	if err != nil {
		t.Fatal(err)
	}

	peers, cidrs, err := s.DesiredState(ctx, out.RelayID)
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 1 || peers[0].PublicKey != "device-pk-1" || peers[0].InnerIP != "10.77.0.5/32" {
		t.Errorf("peers = %+v, want one device-pk-1 at 10.77.0.5/32", peers)
	}
	if len(cidrs) != 2 {
		t.Errorf("cidrs = %v, want two entries", cidrs)
	}
}

func TestDesiredStateWithNoProfileReturnsNoCIDRs(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	key, _ := s.CreateContributorKey(ctx)
	out, _ := s.RegisterRelay(ctx, RegisterInput{
		ContributorKey: key, PublicKey: "pk-noprofile", Endpoint: "203.0.113.10:51820",
	})

	peers, cidrs, err := s.DesiredState(ctx, out.RelayID)
	if err != nil {
		t.Fatalf("no published profile must not be an error: %v", err)
	}
	if len(peers) != 0 || len(cidrs) != 0 {
		t.Errorf("peers = %v, cidrs = %v; want both empty", peers, cidrs)
	}
}

func TestRelaysGetDistinctSubnets(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	key, _ := s.CreateContributorKey(ctx)

	a, _ := s.RegisterRelay(ctx, RegisterInput{ContributorKey: key, PublicKey: "pk-a", Endpoint: "203.0.113.1:51820"})
	b, _ := s.RegisterRelay(ctx, RegisterInput{ContributorKey: key, PublicKey: "pk-b", Endpoint: "203.0.113.2:51820"})

	if a.InnerSubnet == b.InnerSubnet {
		t.Errorf("both relays got subnet %s; each needs its own pool", a.InnerSubnet)
	}
}

// The allocator must not reuse a subnet that is still in service. It was once
// SELECT COUNT(*)+77, so deleting a relay lowered the count and the next
// registration handed out a live relay's subnet. TestRelaysGetDistinctSubnets
// cannot catch that - it never deletes anything - so this is the regression
// guard for the sequence.
func TestSubnetNotReusedAfterDelete(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	key, _ := s.CreateContributorKey(ctx)

	a, err := s.RegisterRelay(ctx, RegisterInput{ContributorKey: key, PublicKey: "pk-a", Endpoint: "203.0.113.1:51820"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.RegisterRelay(ctx, RegisterInput{ContributorKey: key, PublicKey: "pk-b", Endpoint: "203.0.113.2:51820"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM relay WHERE id = $1`, a.RelayID); err != nil {
		t.Fatal(err)
	}
	c, err := s.RegisterRelay(ctx, RegisterInput{ContributorKey: key, PublicKey: "pk-c", Endpoint: "203.0.113.3:51820"})
	if err != nil {
		t.Fatal(err)
	}
	if c.InnerSubnet == b.InnerSubnet {
		t.Errorf("new relay got %s, the subnet the live relay b still holds", c.InnerSubnet)
	}
	if c.InnerSubnet == a.InnerSubnet {
		t.Errorf("new relay reused %s from the deleted relay a", c.InnerSubnet)
	}
}
