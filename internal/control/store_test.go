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
		`DROP TABLE IF EXISTS peer_binding, device, relay, contributor_key, game_profile,
		                      observed_address CASCADE;
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

func TestActivateBindsADeviceToEveryRelay(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	key, _ := s.CreateContributorKey(ctx)
	for i := 0; i < 2; i++ {
		if _, err := s.RegisterRelay(ctx, RegisterInput{
			ContributorKey: key, PublicKey: fmt.Sprintf("relay-%d", i),
			Endpoint: fmt.Sprintf("203.0.113.%d:51820", i+1),
		}); err != nil {
			t.Fatal(err)
		}
	}

	id, _, err := s.ActivateDevice(ctx, key, "device-pk", "harry-desktop")
	if err != nil {
		t.Fatal(err)
	}
	var n int
	s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM peer_binding WHERE device_id = $1`, id).Scan(&n)
	if n != 2 {
		t.Errorf("device bound to %d relays, want 2", n)
	}
	// Two relays, two different pools, so two different addresses.
	var distinct int
	s.pool.QueryRow(ctx, `SELECT COUNT(DISTINCT inner_ip) FROM peer_binding WHERE device_id = $1`, id).Scan(&distinct)
	if distinct != 2 {
		t.Errorf("device holds %d distinct addresses across 2 relays", distinct)
	}
}

// A slot that leaks on every reinstall turns a three-device allowance into a
// support burden within a week.
func TestActivateIsIdempotentOnTheDeviceKey(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	key, _ := s.CreateContributorKey(ctx)
	s.RegisterRelay(ctx, RegisterInput{ContributorKey: key, PublicKey: "r1", Endpoint: "203.0.113.1:51820"})

	first, _, err := s.ActivateDevice(ctx, key, "same-device", "laptop")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		again, _, err := s.ActivateDevice(ctx, key, "same-device", "laptop")
		if err != nil {
			t.Fatalf("re-activation %d: %v", i, err)
		}
		if again != first {
			t.Fatalf("device id changed on re-activation: %s then %s", first, again)
		}
	}
	var used int
	s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM device`).Scan(&used)
	if used != 1 {
		t.Errorf("%d devices after five activations of one key, want 1", used)
	}
}

// Running out of slots must name the machines holding them. "You are out of
// slots" alone leaves somebody guessing at their own hardware.
func TestSlotsFullNamesTheDevicesHoldingThem(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	key, _ := s.CreateContributorKey(ctx)

	var maxDevices int
	s.pool.QueryRow(ctx, `SELECT max_devices FROM contributor_key WHERE key_hash = $1`, Hash(key)).Scan(&maxDevices)
	for i := 0; i < maxDevices; i++ {
		if _, _, err := s.ActivateDevice(ctx, key, fmt.Sprintf("dev-pk-%d", i), fmt.Sprintf("machine-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	_, held, err := s.ActivateDevice(ctx, key, "one-too-many", "new-laptop")
	if !errors.Is(err, ErrSlotsFull) {
		t.Fatalf("err = %v, want ErrSlotsFull", err)
	}
	if len(held) != maxDevices {
		t.Fatalf("reported %d devices holding slots, want %d", len(held), maxDevices)
	}
	for _, d := range held {
		if d.Fingerprint == "" {
			t.Error("a device is reported with no fingerprint; the person cannot tell which machine it is")
		}
	}
}

// Session filters rather than ranks: it must exclude relays that are not up and
// relays still inside their observation window.
func TestSessionOffersOnlyUsableRelays(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	key, _ := s.CreateContributorKey(ctx)

	good, _ := s.RegisterRelay(ctx, RegisterInput{ContributorKey: key, PublicKey: "r-good", Endpoint: "203.0.113.1:51820", Region: "sgp"})
	pending, _ := s.RegisterRelay(ctx, RegisterInput{ContributorKey: key, PublicKey: "r-pending", Endpoint: "203.0.113.2:51820"})
	young, _ := s.RegisterRelay(ctx, RegisterInput{ContributorKey: key, PublicKey: "r-young", Endpoint: "203.0.113.3:51820"})

	// good: reachable and past its window. young: reachable but still inside it.
	s.RecordReachability(ctx, key, "r-good", true, "")
	s.RecordReachability(ctx, key, "r-young", true, "")
	s.pool.Exec(ctx, `UPDATE relay SET trusted_after = now() - interval '1 day' WHERE id = $1`, good.RelayID)
	s.pool.Exec(ctx, `UPDATE relay SET trusted_after = now() + interval '1 day' WHERE id = $1`, young.RelayID)
	_ = pending // left at status 'pending': never verified from outside

	if _, _, err := s.ActivateDevice(ctx, key, "dev-pk", "pc"); err != nil {
		t.Fatal(err)
	}
	sess, err := s.Session(ctx, key, "dev-pk", 1420)
	if err != nil {
		t.Fatal(err)
	}
	if len(sess.Relays) != 1 {
		var got []string
		for _, r := range sess.Relays {
			got = append(got, r.RelayID)
		}
		t.Fatalf("offered %v, want only the verified relay past its window", got)
	}
	if sess.Relays[0].RelayID != good.RelayID {
		t.Errorf("offered %s, want %s", sess.Relays[0].RelayID, good.RelayID)
	}
	if sess.Relays[0].InnerIP == "" || sess.Relays[0].PublicKey == "" || sess.Relays[0].MTU != 1420 {
		t.Errorf("offer is incomplete: %+v", sess.Relays[0])
	}
}

func TestReleaseDeviceFreesASlot(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	key, _ := s.CreateContributorKey(ctx)
	id, _, err := s.ActivateDevice(ctx, key, "dev-pk", "pc")
	if err != nil {
		t.Fatal(err)
	}
	other, _ := s.CreateContributorKey(ctx)
	if err := s.ReleaseDevice(ctx, other, id); !errors.Is(err, ErrUnknownKey) {
		t.Errorf("a stranger released somebody else's device: %v", err)
	}
	if err := s.ReleaseDevice(ctx, key, id); err != nil {
		t.Fatal(err)
	}
	var n int
	s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM device`).Scan(&n)
	if n != 0 {
		t.Errorf("%d devices left after release, want 0", n)
	}
}

func TestPublishProfileVersionsMonotonically(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	v1, err := s.PublishProfile(ctx, []string{"20.24.48.0/20"})
	if err != nil {
		t.Fatal(err)
	}
	v2, err := s.PublishProfile(ctx, []string{"20.24.48.0/20", "52.139.208.0/20"})
	if err != nil {
		t.Fatal(err)
	}
	if v2 <= v1 {
		t.Errorf("versions went %d then %d; they must only move forward", v1, v2)
	}
	// Agents fetch the newest.
	prof, err := s.Profile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if prof.Version != v2 || len(prof.CIDRs) != 2 {
		t.Errorf("Profile = %+v, want version %d with two CIDRs", prof, v2)
	}
	// The old version is still there, so re-publishing it is a normal operation
	// rather than a restore.
	var kept int
	s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM game_profile`).Scan(&kept)
	if kept != 2 {
		t.Errorf("%d profile versions kept, want both", kept)
	}
}

// An empty profile means "forward nothing". It is a real state, but never one to
// reach by accident - every path producing one is a bug upstream.
func TestPublishRefusesAnEmptyProfile(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if _, err := s.PublishProfile(ctx, nil); err == nil {
		t.Error("published an empty profile")
	}
}

// The promotion rule is "three INDEPENDENT contributors". One person reporting
// the same address three times must not satisfy it - that is precisely the case
// the rule exists to exclude, and a wrong CIDR drags unrelated traffic through
// somebody else's relay.
func TestOneContributorCannotPromoteAnAddressAlone(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	solo, _ := s.CreateContributorKey(ctx)

	for i := 0; i < 5; i++ {
		if err := s.RecordObservation(ctx, solo, "pubg", "20.24.50.9", 20522); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.CandidateAddresses(ctx, "pubg", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("candidates = %v after five reports from ONE contributor; the rule "+
			"counts independent contributors, not reports", got)
	}
}

func TestThreeIndependentContributorsPromoteAnAddress(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	var keys []string
	for i := 0; i < 3; i++ {
		k, _ := s.CreateContributorKey(ctx)
		keys = append(keys, k)
	}
	for _, k := range keys {
		if err := s.RecordObservation(ctx, k, "pubg", "20.24.50.9", 20522); err != nil {
			t.Fatal(err)
		}
	}
	// A second address seen by only two of them stays below the bar.
	for _, k := range keys[:2] {
		if err := s.RecordObservation(ctx, k, "pubg", "20.24.51.4", 20522); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.CandidateAddresses(ctx, "pubg", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "20.24.50.9" {
		t.Errorf("candidates = %v, want only the address three contributors saw", got)
	}
}

func TestObservationsAreScopedPerGame(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	k, _ := s.CreateContributorKey(ctx)
	if err := s.RecordObservation(ctx, k, "pubg", "20.24.50.9", 20522); err != nil {
		t.Fatal(err)
	}
	other, err := s.ObservedAddresses(ctx, "cs2")
	if err != nil {
		t.Fatal(err)
	}
	if len(other) != 0 {
		t.Errorf("another game sees %v; once two games' addresses are mixed there is "+
			"no separating them", other)
	}
}

func revoke(t *testing.T, s *Store, key string) {
	t.Helper()
	if _, err := s.pool.Exec(context.Background(),
		`UPDATE contributor_key SET status = 'revoked' WHERE key_hash = $1`, Hash(key)); err != nil {
		t.Fatal(err)
	}
}

// usableRelay registers a relay for key and makes it one a session would offer.
func usableRelay(t *testing.T, s *Store, key, pubkey string) RegisterOutput {
	t.Helper()
	ctx := context.Background()
	out, err := s.RegisterRelay(ctx, RegisterInput{ContributorKey: key, PublicKey: pubkey, Endpoint: "203.0.113.9:51820"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordReachability(ctx, key, pubkey, true, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE relay SET trusted_after = now() - interval '1 day' WHERE id = $1`, out.RelayID); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestARevokedKeyGetsNoSession(t *testing.T) {
	// Revoking a key is how somebody is put out of the network. A device it had
	// already activated must not go on fetching relays as if nothing happened.
	ctx := context.Background()
	s := openTestStore(t)
	owner, _ := s.CreateContributorKey(ctx)
	player, _ := s.CreateContributorKey(ctx)
	usableRelay(t, s, owner, "r-1")
	if _, _, err := s.ActivateDevice(ctx, player, "dev-pk", "pc"); err != nil {
		t.Fatal(err)
	}
	revoke(t, s, player)
	if _, err := s.Session(ctx, player, "dev-pk", 1420); !errors.Is(err, ErrUnknownKey) {
		t.Errorf("err = %v, want ErrUnknownKey for a revoked key", err)
	}
}

func TestARevokedKeysDevicesLeaveEveryRelay(t *testing.T) {
	// Refusing the session is not enough: a client already connected keeps its
	// tunnel for as long as the relay keeps its peer.
	ctx := context.Background()
	s := openTestStore(t)
	owner, _ := s.CreateContributorKey(ctx)
	player, _ := s.CreateContributorKey(ctx)
	relay := usableRelay(t, s, owner, "r-1")
	if _, _, err := s.ActivateDevice(ctx, player, "dev-pk", "pc"); err != nil {
		t.Fatal(err)
	}
	revoke(t, s, player)
	peers, _, err := s.DesiredState(ctx, relay.RelayID)
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 0 {
		t.Errorf("relay still carries %+v for a revoked key", peers)
	}
}

func TestARevokedContributorsRelayCarriesNobody(t *testing.T) {
	// A key is often revoked because of what its owner did. Their relay must stop
	// being offered and stop carrying anybody's traffic.
	ctx := context.Background()
	s := openTestStore(t)
	owner, _ := s.CreateContributorKey(ctx)
	player, _ := s.CreateContributorKey(ctx)
	relay := usableRelay(t, s, owner, "r-1")
	if _, _, err := s.ActivateDevice(ctx, player, "dev-pk", "pc"); err != nil {
		t.Fatal(err)
	}
	revoke(t, s, owner)
	sess, err := s.Session(ctx, player, "dev-pk", 1420)
	if err != nil {
		t.Fatal(err)
	}
	if len(sess.Relays) != 0 {
		t.Errorf("a revoked contributor's relay is still offered: %+v", sess.Relays)
	}
	peers, _, err := s.DesiredState(ctx, relay.RelayID)
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 0 {
		t.Errorf("a revoked contributor's relay is still given peers: %+v", peers)
	}
}

func TestARevokedKeyCannotVouchForItsRelay(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	owner, _ := s.CreateContributorKey(ctx)
	if _, err := s.RegisterRelay(ctx, RegisterInput{ContributorKey: owner, PublicKey: "r-1", Endpoint: "203.0.113.9:51820"}); err != nil {
		t.Fatal(err)
	}
	revoke(t, s, owner)
	if err := s.RecordReachability(ctx, owner, "r-1", true, ""); !errors.Is(err, ErrUnknownKey) {
		t.Errorf("err = %v, want ErrUnknownKey: a revoked key must not mark its relay up", err)
	}
}

func TestARevokedKeyNoLongerCountsTowardsAProfile(t *testing.T) {
	// The promotion rule counts independent contributors. One of them being
	// revoked, before or after reporting, must take their vote with them.
	ctx := context.Background()
	s := openTestStore(t)
	var keys []string
	for i := 0; i < 3; i++ {
		k, _ := s.CreateContributorKey(ctx)
		keys = append(keys, k)
		if err := s.RecordObservation(ctx, k, "pubg", "20.24.50.9", 20522); err != nil {
			t.Fatal(err)
		}
	}
	revoke(t, s, keys[0])
	got, err := s.CandidateAddresses(ctx, "pubg", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("candidates = %v with one of three reporters revoked", got)
	}
	if err := s.RecordObservation(ctx, keys[0], "pubg", "20.24.50.10", 20522); !errors.Is(err, ErrUnknownKey) {
		t.Errorf("err = %v, want ErrUnknownKey for a report from a revoked key", err)
	}
}
