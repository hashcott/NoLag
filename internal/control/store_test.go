package control

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

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

func TestRecordStatusStoresCountersWithoutClaimingReachable(t *testing.T) {
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
	var lastSeen *time.Time
	err = s.pool.QueryRow(ctx,
		`SELECT status, active_peers, last_seen FROM relay WHERE id = $1`, out.RelayID).
		Scan(&status, &active, &lastSeen)
	if err != nil {
		t.Fatal(err)
	}
	if active != 3 {
		t.Errorf("active_peers = %d, want 3", active)
	}
	if lastSeen == nil {
		t.Error("last_seen was not recorded")
	}
	// The point of the change: a sync proves the relay reached US. Whether a
	// player can reach IT is a different question, settled only by an external
	// check. See TestSyncingAloneDoesNotMakeARelayUp.
	if status != "pending" {
		t.Errorf("status = %q, want pending: syncing alone must not claim reachable", status)
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

// A relay's WireGuard public key is not a secret: the installer prints it, the
// runbook uses it, and every client that connects is told it. Matching on it
// alone let any valid contributor key adopt somebody else's relay - receiving a
// working token for it, reading its whole peer list, rewriting its endpoint, and
// locking the real agent out. TestRegisterRelayIsIdempotentForTheSamePublicKey
// cannot see this: it re-registers under the same contributor key.
func TestRegisterRelayRefusesAnotherContributorsRelay(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	victimKey, _ := s.CreateContributorKey(ctx)
	attackerKey, _ := s.CreateContributorKey(ctx)

	victim, err := s.RegisterRelay(ctx, RegisterInput{
		ContributorKey: victimKey, PublicKey: "relay-pk-victim", Endpoint: "203.0.113.10:51820",
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.RegisterRelay(ctx, RegisterInput{
		ContributorKey: attackerKey,
		PublicKey:      "relay-pk-victim", // public by construction
		Endpoint:       "198.51.100.66:51820",
	})
	if !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("err = %v, want ErrUnknownKey: a valid contributor key must not adopt another contributor's relay", err)
	}

	// The victim's own token must still work, and its endpoint must be untouched.
	if _, err := s.AuthenticateRelay(ctx, victim.RelayToken); err != nil {
		t.Errorf("the legitimate agent was locked out: %v", err)
	}
	var endpoint string
	if err := s.pool.QueryRow(ctx, `SELECT endpoint FROM relay WHERE id = $1`, victim.RelayID).Scan(&endpoint); err != nil {
		t.Fatal(err)
	}
	if endpoint != "203.0.113.10:51820" {
		t.Errorf("endpoint = %q, want the victim's own: it was rewritten", endpoint)
	}
}

// status only ever moved pending -> up, so a relay that rebooted, was unplugged,
// or was simply switched off went on being offered to players forever. The agent
// syncs every 10s; one silent for minutes is gone, not busy.
func TestMarkStaleRelaysDown(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	key, _ := s.CreateContributorKey(ctx)

	fresh, _ := s.RegisterRelay(ctx, RegisterInput{ContributorKey: key, PublicKey: "pk-fresh", Endpoint: "203.0.113.1:51820"})
	stale, _ := s.RegisterRelay(ctx, RegisterInput{ContributorKey: key, PublicKey: "pk-stale", Endpoint: "203.0.113.2:51820"})

	// Both must be genuinely up first: the sweep moves up -> down, and a relay
	// that was never verified reachable has nothing to fall from.
	for _, pk := range []string{"pk-fresh", "pk-stale"} {
		if err := s.RecordReachability(ctx, key, pk, true, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RecordStatus(ctx, fresh.RelayID, api.RelayStatus{}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordStatus(ctx, stale.RelayID, api.RelayStatus{}); err != nil {
		t.Fatal(err)
	}
	// Age the stale one past the window.
	if _, err := s.pool.Exec(ctx,
		`UPDATE relay SET last_seen = now() - interval '1 hour' WHERE id = $1`, stale.RelayID); err != nil {
		t.Fatal(err)
	}

	n, err := s.MarkStaleRelaysDown(ctx, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("marked %d relays down, want exactly the stale one", n)
	}

	var freshStatus, staleStatus string
	s.pool.QueryRow(ctx, `SELECT status FROM relay WHERE id = $1`, fresh.RelayID).Scan(&freshStatus)
	s.pool.QueryRow(ctx, `SELECT status FROM relay WHERE id = $1`, stale.RelayID).Scan(&staleStatus)
	if freshStatus != "up" {
		t.Errorf("the relay that just synced is %q, want up", freshStatus)
	}
	if staleStatus != "down" {
		t.Errorf("the relay silent for an hour is %q, want down", staleStatus)
	}
}

// A relay that syncs has shown it can reach US. That says nothing about whether
// a player can reach IT: the provider's security group sits in front of its UDP
// port and is invisible from inside the machine. status='up' must mean verified
// reachable, or P3 selection hands players relays behind a closed firewall.
func TestSyncingAloneDoesNotMakeARelayUp(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	key, _ := s.CreateContributorKey(ctx)
	out, _ := s.RegisterRelay(ctx, RegisterInput{
		ContributorKey: key, PublicKey: "pk-reach", Endpoint: "203.0.113.10:51820",
	})

	for i := 0; i < 5; i++ {
		if err := s.RecordStatus(ctx, out.RelayID, api.RelayStatus{ActivePeers: 3}); err != nil {
			t.Fatal(err)
		}
	}
	var status string
	s.pool.QueryRow(ctx, `SELECT status FROM relay WHERE id = $1`, out.RelayID).Scan(&status)
	if status != "pending" {
		t.Errorf("status = %q after five syncs and no external check, want pending", status)
	}

	// An external check succeeds: now it is up.
	if err := s.RecordReachability(ctx, key, "pk-reach", true, ""); err != nil {
		t.Fatal(err)
	}
	s.pool.QueryRow(ctx, `SELECT status FROM relay WHERE id = $1`, out.RelayID).Scan(&status)
	if status != "up" {
		t.Errorf("status = %q after a successful reachability check, want up", status)
	}

	// A later failed check takes it out of service.
	if err := s.RecordReachability(ctx, key, "pk-reach", false, "no handshake within 12s"); err != nil {
		t.Fatal(err)
	}
	var detail string
	s.pool.QueryRow(ctx, `SELECT status, unreachable_detail FROM relay WHERE id = $1`, out.RelayID).Scan(&status, &detail)
	if status != "unreachable" {
		t.Errorf("status = %q after a failed check, want unreachable", status)
	}
	if detail == "" {
		t.Error("the reason was not recorded; an operator needs to know why")
	}
}

func TestReachabilityIsScopedToTheOwningContributor(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	owner, _ := s.CreateContributorKey(ctx)
	stranger, _ := s.CreateContributorKey(ctx)
	if _, err := s.RegisterRelay(ctx, RegisterInput{
		ContributorKey: owner, PublicKey: "pk-owned", Endpoint: "203.0.113.10:51820",
	}); err != nil {
		t.Fatal(err)
	}
	err := s.RecordReachability(ctx, stranger, "pk-owned", false, "sabotage")
	if !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("err = %v, want ErrUnknownKey: a stranger must not be able to mark a relay unreachable", err)
	}
}

// relay_octet_seq runs 77..255 and does not recycle, so without a cap one key
// could burn the whole pool and every later registration by anybody would fail.
func TestOneKeyCannotDrainTheSubnetPool(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	key, _ := s.CreateContributorKey(ctx)

	for i := 0; i < maxRelaysPerKey; i++ {
		if _, err := s.RegisterRelay(ctx, RegisterInput{
			ContributorKey: key,
			PublicKey:      fmt.Sprintf("pk-%d", i),
			Endpoint:       fmt.Sprintf("203.0.113.%d:51820", i+1),
		}); err != nil {
			t.Fatalf("relay %d: %v", i, err)
		}
	}
	_, err := s.RegisterRelay(ctx, RegisterInput{
		ContributorKey: key, PublicKey: "pk-one-too-many", Endpoint: "203.0.113.200:51820",
	})
	if !errors.Is(err, ErrTooManyRelays) {
		t.Fatalf("err = %v, want ErrTooManyRelays after %d relays", err, maxRelaysPerKey)
	}

	// A different contributor is unaffected.
	other, _ := s.CreateContributorKey(ctx)
	if _, err := s.RegisterRelay(ctx, RegisterInput{
		ContributorKey: other, PublicKey: "pk-other", Endpoint: "203.0.113.250:51820",
	}); err != nil {
		t.Errorf("an unrelated contributor was blocked by somebody else's cap: %v", err)
	}
}
