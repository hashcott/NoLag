package control

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"gamenolag/internal/api"
)

//go:embed schema.sql
var schemaSQL string

// Errors the HTTP layer turns into status codes.
// maxRelaysPerKey bounds how much of the subnet pool one contributor can hold.
// Generous for anyone genuinely donating hardware; far below the 179 the pool has.
const maxRelaysPerKey = 10

var (
	ErrUnknownKey    = errors.New("control: unknown contributor key")
	ErrTooManyRelays = errors.New("control: this key already has the maximum number of relays")
	ErrUnauthorized  = errors.New("control: unauthorized")
)

// trustWindow is how long a newly registered relay carries no user traffic.
// It is an observation window: a relay contributed in order to watch other
// people's traffic has to sit visible before it can attract any.
const trustWindow = 48 * time.Hour

// Store is every database query the control plane makes. No HTTP type crosses
// this boundary in either direction.
type Store struct{ pool *pgxpool.Pool }

// Open connects to Postgres.
func Open(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("control: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("control: ping: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Close releases the connection pool.
func (s *Store) Close() { s.pool.Close() }

// Migrate applies the schema. Every statement is IF NOT EXISTS, so this is safe
// on every start and needs no migration framework at this size.
func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx, schemaSQL); err != nil {
		return fmt.Errorf("control: migrate: %w", err)
	}
	return nil
}

// CreateContributorKey mints a key and stores only its hash. The plaintext is
// returned once and is not recoverable afterwards.
func (s *Store) CreateContributorKey(ctx context.Context) (string, error) {
	key, err := NewContributorKey()
	if err != nil {
		return "", err
	}
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO contributor_key (key_hash) VALUES ($1)`, Hash(key)); err != nil {
		return "", fmt.Errorf("control: insert contributor key: %w", err)
	}
	return key, nil
}

// RegisterInput is what a relay presents when it first installs the agent.
type RegisterInput struct {
	ContributorKey string
	PublicKey      string
	Endpoint       string
	Region         string
	Hostname       string
}

// RegisterOutput is the relay's identity and address assignment.
type RegisterOutput struct {
	RelayID     string
	RelayToken  string
	InnerSubnet string
	InnerIP     string
}

// RegisterRelay creates or re-registers a relay, keyed on its WireGuard public
// key.
//
// Re-registering is normal, not an error: a contributor who re-runs the
// installer, or who lost the token file, must be able to recover without the
// relay being stranded. The token is rotated on every register, because the
// previous one may be the reason they are here.
func (s *Store) RegisterRelay(ctx context.Context, in RegisterInput) (RegisterOutput, error) {
	keyHash := Hash(in.ContributorKey)

	var status string
	err := s.pool.QueryRow(ctx,
		`SELECT status FROM contributor_key WHERE key_hash = $1`, keyHash).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return RegisterOutput{}, ErrUnknownKey
	}
	if err != nil {
		return RegisterOutput{}, fmt.Errorf("control: look up contributor key: %w", err)
	}
	if status != "active" {
		return RegisterOutput{}, ErrUnknownKey
	}

	token, err := NewRelayToken()
	if err != nil {
		return RegisterOutput{}, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return RegisterOutput{}, fmt.Errorf("control: begin: %w", err)
	}
	defer tx.Rollback(ctx)

	// Scoped by key_hash as well as public key. A relay's WireGuard public key is
	// not a secret - the installer prints it, the runbook uses it to verify
	// reachability, and every client that connects must be told it. Matching on it
	// alone would let any holder of any valid contributor key re-register somebody
	// else's relay: they would receive a working token for it, read its entire peer
	// list, rewrite its endpoint, and lock the real agent out with a rotated token.
	// Contributors are semi-trusted third parties, which is exactly who this stops.
	var relayID, subnet string
	err = tx.QueryRow(ctx,
		`SELECT id, inner_subnet FROM relay WHERE wg_pubkey = $1 AND key_hash = $2 FOR UPDATE`,
		in.PublicKey, keyHash).
		Scan(&relayID, &subnet)

	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Cap how many relays one key may enrol. Every new relay consumes a value
		// from relay_octet_seq, which runs 77..255 and does not recycle, so without
		// this one key could burn the whole pool in 179 requests and every later
		// registration by anybody would fail. The rate limiter bounds the speed of
		// that; this bounds the total.
		var owned int
		if err := tx.QueryRow(ctx,
			`SELECT COUNT(*) FROM relay WHERE key_hash = $1`, keyHash).Scan(&owned); err != nil {
			return RegisterOutput{}, fmt.Errorf("control: count relays for key: %w", err)
		}
		if owned >= maxRelaysPerKey {
			return RegisterOutput{}, ErrTooManyRelays
		}

		// No relay with this public key under THIS contributor key. It may still
		// exist under a different one, in which case this is somebody trying to
		// adopt another contributor's relay. Refuse it here rather than letting the
		// insert collide on UNIQUE(wg_pubkey) and surface as an opaque database
		// error, and refuse it with the same vague message an unknown key gets, so
		// registering is not an oracle for which public keys are already enrolled.
		var taken bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM relay WHERE wg_pubkey = $1)`, in.PublicKey).Scan(&taken); err != nil {
			return RegisterOutput{}, fmt.Errorf("control: check public key: %w", err)
		}
		if taken {
			return RegisterOutput{}, ErrUnknownKey
		}

		// New relay. Allocate the next /16 out of 10.x.0.0/16 from a sequence.
		//
		// A sequence, not SELECT COUNT(*)+77: deleting a relay lowers a count, so
		// the next registration would hand out a subnet that is still in use. A
		// sequence only ever moves forward, which is the property actually wanted.
		// peer_binding's UNIQUE(relay_id, inner_ip) guards addresses inside a pool.
		var octet int
		if err := tx.QueryRow(ctx, `SELECT nextval('relay_octet_seq')`).Scan(&octet); err != nil {
			return RegisterOutput{}, fmt.Errorf("control: allocate subnet: %w", err)
		}
		if octet > 255 {
			return RegisterOutput{}, fmt.Errorf("control: the 10.x.0.0/16 pool is exhausted after %d relays", octet-77)
		}
		subnet = fmt.Sprintf("10.%d.0.0/16", octet)
		relayID = fmt.Sprintf("relay-%s", Hash(in.PublicKey)[:12])

		_, err = tx.Exec(ctx,
			`INSERT INTO relay (id, key_hash, region, hostname, endpoint, wg_pubkey,
			                    inner_subnet, trusted_after, token_hash)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			relayID, keyHash, in.Region, in.Hostname, in.Endpoint, in.PublicKey,
			subnet, time.Now().Add(trustWindow), Hash(token))
		if err != nil {
			return RegisterOutput{}, fmt.Errorf("control: insert relay: %w", err)
		}

	case err != nil:
		return RegisterOutput{}, fmt.Errorf("control: look up relay: %w", err)

	default:
		// Existing relay: rotate the token, refresh the endpoint, keep the subnet.
		// Keeping the subnet is what lets existing peer bindings stay valid.
		_, err = tx.Exec(ctx,
			`UPDATE relay SET token_hash = $1, endpoint = $2, region = $3, hostname = $4
			 WHERE id = $5 AND key_hash = $6`,
			Hash(token), in.Endpoint, in.Region, in.Hostname, relayID, keyHash)
		if err != nil {
			return RegisterOutput{}, fmt.Errorf("control: rotate relay token: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return RegisterOutput{}, fmt.Errorf("control: commit: %w", err)
	}

	// The relay's own address is the .0.1 of whatever subnet it holds. Parse the
	// octet once out of the subnet string rather than scanning it twice.
	var octet int
	if _, err := fmt.Sscanf(subnet, "10.%d.", &octet); err != nil {
		return RegisterOutput{}, fmt.Errorf("control: relay %s has an unparseable subnet %q: %w", relayID, subnet, err)
	}
	innerIP := fmt.Sprintf("10.%d.0.1/16", octet)

	return RegisterOutput{
		RelayID: relayID, RelayToken: token, InnerSubnet: subnet, InnerIP: innerIP,
	}, nil
}

// AuthenticateRelay resolves a bearer token to a relay id.
func (s *Store) AuthenticateRelay(ctx context.Context, token string) (string, error) {
	if token == "" {
		return "", ErrUnauthorized
	}
	var id string
	err := s.pool.QueryRow(ctx,
		`SELECT id FROM relay WHERE token_hash = $1`, Hash(token)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrUnauthorized
	}
	if err != nil {
		return "", fmt.Errorf("control: authenticate relay: %w", err)
	}
	return id, nil
}

// RecordStatus stores the agent's report and marks the relay reachable.
//
// A relay that syncs has proved it can reach us, which is not the same as us
// being able to reach it; cmd/gnl-relaycheck answers that second question.
func (s *Store) RecordStatus(ctx context.Context, relayID string, st api.RelayStatus) error {
	// Deliberately does NOT set status = 'up'. A sync proves the relay can reach
	// us; it proves nothing about whether a player can reach the relay, because
	// the provider's security group sits in front of its UDP port and is invisible
	// from inside the machine. Only an external handshake settles that, and it
	// arrives through RecordReachability. A relay that has never been verified
	// stays 'pending' however faithfully it syncs.
	_, err := s.pool.Exec(ctx,
		`UPDATE relay
		    SET last_seen = now(),
		        active_peers = $2, rx_bytes = $3, tx_bytes = $4,
		        status = CASE
		                   WHEN reachable_at IS NOT NULL THEN 'up'
		                   ELSE status
		                 END
		  WHERE id = $1`,
		relayID, st.ActivePeers, st.RxBytes, st.TxBytes)
	if err != nil {
		return fmt.Errorf("control: record status: %w", err)
	}
	return nil
}

// RecordReachability stores the verdict of an external reachability check.
//
// This is the only thing that can move a relay to 'up'. gnl-relaycheck runs from
// somewhere other than the relay and attempts a real WireGuard handshake, which
// is the one test that covers the provider's security group as well as the host
// firewall - and a closed security group is the most common reason a relay looks
// healthy locally and is unreachable from everywhere else.
//
// Scoped by contributor key: a contributor may report on their own relays and
// nobody else's. Without that, anyone could mark a stranger's relay unreachable
// and take it out of service.
func (s *Store) RecordReachability(ctx context.Context, contributorKey, relayPubKey string, ok bool, detail string) error {
	keyHash := Hash(contributorKey)

	var tag pgconn.CommandTag
	var err error
	if ok {
		tag, err = s.pool.Exec(ctx,
			`UPDATE relay
			    SET reachable_at = now(), status = 'up', unreachable_detail = ''
			  WHERE wg_pubkey = $1 AND key_hash = $2
			    AND key_hash IN (SELECT key_hash FROM contributor_key WHERE status = 'active')`,
			relayPubKey, keyHash)
	} else {
		// Not cleared: a relay that was reachable and now is not keeps its
		// reachable_at, so the record shows it worked once. Status says it does not
		// work now, which is what selection needs to read.
		tag, err = s.pool.Exec(ctx,
			`UPDATE relay
			    SET status = 'unreachable', unreachable_detail = $3
			  WHERE wg_pubkey = $1 AND key_hash = $2
			    AND key_hash IN (SELECT key_hash FROM contributor_key WHERE status = 'active')`,
			relayPubKey, keyHash, detail)
	}
	if err != nil {
		return fmt.Errorf("control: record reachability: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Same vague answer an unknown key gets, so this is not an oracle for which
		// public keys are enrolled under which contributor.
		return ErrUnknownKey
	}
	return nil
}

// MarkStaleRelaysDown flips a relay to 'down' once it has stopped syncing.
//
// Without this, status only ever moves pending -> up, so a relay that died -
// rebooted, unplugged, or whose contributor simply turned it off - is offered to
// players forever. The agent syncs every 10s by default; a relay silent for
// several minutes is not having a bad moment, it is gone.
//
// Returns how many were marked, so the caller can say so rather than doing it
// silently.
func (s *Store) MarkStaleRelaysDown(ctx context.Context, after time.Duration) (int, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE relay
		    SET status = 'down'
		  WHERE status = 'up'
		    AND (last_seen IS NULL OR last_seen < now() - $1::interval)`,
		fmt.Sprintf("%d seconds", int(after.Seconds())))
	if err != nil {
		return 0, fmt.Errorf("control: mark stale relays down: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// DesiredState returns every peer this relay must accept and the current game
// CIDR allowlist.
//
// Only peers whose key is active, and none at all on a relay whose owner's key
// is not: revoking a key has to reach a tunnel that is already up, and the only
// thing that reaches it is the relay dropping the peer on its next sync.
//
// No published profile is not an error: it means nothing may be forwarded yet,
// which is the correct state for a relay brought up before the first profile
// exists.
func (s *Store) DesiredState(ctx context.Context, relayID string) ([]api.Peer, []string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT d.wg_pubkey, b.inner_ip
		   FROM peer_binding b
		   JOIN device d ON d.id = b.device_id
		   JOIN contributor_key dk ON dk.key_hash = d.key_hash AND dk.status = 'active'
		   JOIN relay r ON r.id = b.relay_id
		   JOIN contributor_key rk ON rk.key_hash = r.key_hash AND rk.status = 'active'
		  WHERE b.relay_id = $1
		  ORDER BY d.wg_pubkey`, relayID)
	if err != nil {
		return nil, nil, fmt.Errorf("control: read peers: %w", err)
	}
	defer rows.Close()

	var peers []api.Peer
	for rows.Next() {
		var p api.Peer
		if err := rows.Scan(&p.PublicKey, &p.InnerIP); err != nil {
			return nil, nil, fmt.Errorf("control: scan peer: %w", err)
		}
		peers = append(peers, p)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("control: read peers: %w", err)
	}

	var cidrs []string
	err = s.pool.QueryRow(ctx,
		`SELECT cidrs FROM game_profile ORDER BY version DESC LIMIT 1`).Scan(&cidrs)
	if errors.Is(err, pgx.ErrNoRows) {
		return peers, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("control: read profile: %w", err)
	}
	return peers, cidrs, nil
}

// ErrSlotsFull is returned when a contributor key already has its maximum
// number of devices activated.
var ErrSlotsFull = errors.New("control: no device slots left on this key")

// ActivateDevice registers a device against a contributor key and binds it an
// inner address on every relay that key may use.
//
// Idempotent on the device public key: re-running the client, or reinstalling
// it without wiping its key, must not consume a second slot. That matters more
// than it sounds - a slot that leaks on every reinstall turns a three-device
// allowance into a support burden within a week.
func (s *Store) ActivateDevice(ctx context.Context, contributorKey, devicePubKey, fingerprint string) (string, []api.DeviceSummary, error) {
	keyHash := Hash(contributorKey)

	var status string
	var maxDevices int
	err := s.pool.QueryRow(ctx,
		`SELECT status, max_devices FROM contributor_key WHERE key_hash = $1`, keyHash).
		Scan(&status, &maxDevices)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && status != "active") {
		return "", nil, ErrUnknownKey
	}
	if err != nil {
		return "", nil, fmt.Errorf("control: look up key: %w", err)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", nil, fmt.Errorf("control: begin: %w", err)
	}
	defer tx.Rollback(ctx)

	var deviceID string
	err = tx.QueryRow(ctx,
		`SELECT id FROM device WHERE wg_pubkey = $1 AND key_hash = $2`,
		devicePubKey, keyHash).Scan(&deviceID)

	switch {
	case errors.Is(err, pgx.ErrNoRows):
		var used int
		if err := tx.QueryRow(ctx,
			`SELECT COUNT(*) FROM device WHERE key_hash = $1`, keyHash).Scan(&used); err != nil {
			return "", nil, fmt.Errorf("control: count devices: %w", err)
		}
		if used >= maxDevices {
			held, err := s.devicesForKey(ctx, keyHash)
			if err != nil {
				return "", nil, err
			}
			return "", held, ErrSlotsFull
		}
		deviceID = fmt.Sprintf("dev-%s", Hash(devicePubKey)[:12])
		if _, err := tx.Exec(ctx,
			`INSERT INTO device (id, key_hash, wg_pubkey, fingerprint) VALUES ($1,$2,$3,$4)`,
			deviceID, keyHash, devicePubKey, fingerprint); err != nil {
			return "", nil, fmt.Errorf("control: insert device: %w", err)
		}
	case err != nil:
		return "", nil, fmt.Errorf("control: look up device: %w", err)
	default:
		// Already activated. Refresh what a human reads and consume nothing.
		if _, err := tx.Exec(ctx,
			`UPDATE device SET fingerprint = $2, last_seen = now() WHERE id = $1`,
			deviceID, fingerprint); err != nil {
			return "", nil, fmt.Errorf("control: refresh device: %w", err)
		}
	}

	if err := s.bindDeviceToRelays(ctx, tx, deviceID, keyHash); err != nil {
		return "", nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", nil, fmt.Errorf("control: commit: %w", err)
	}
	return deviceID, nil, nil
}

func (s *Store) devicesForKey(ctx context.Context, keyHash string) ([]api.DeviceSummary, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, fingerprint, COALESCE(last_seen::text, '')
		   FROM device WHERE key_hash = $1 ORDER BY created_at`, keyHash)
	if err != nil {
		return nil, fmt.Errorf("control: list devices: %w", err)
	}
	defer rows.Close()
	var out []api.DeviceSummary
	for rows.Next() {
		var d api.DeviceSummary
		if err := rows.Scan(&d.ID, &d.Fingerprint, &d.LastSeen); err != nil {
			return nil, fmt.Errorf("control: scan device: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// bindDeviceToRelays gives the device an inner address on every relay it may
// use, reusing the one it already holds.
//
// Stable addresses are the point: a client that loses its network for a few
// seconds must not have to re-address its adapter and reinstall every route at
// exactly the moment the network is least reliable.
func (s *Store) bindDeviceToRelays(ctx context.Context, tx pgx.Tx, deviceID, keyHash string) error {
	rows, err := tx.Query(ctx, `SELECT id, inner_subnet FROM relay`)
	if err != nil {
		return fmt.Errorf("control: list relays: %w", err)
	}
	type rel struct{ id, subnet string }
	var relays []rel
	for rows.Next() {
		var r rel
		if err := rows.Scan(&r.id, &r.subnet); err != nil {
			rows.Close()
			return fmt.Errorf("control: scan relay: %w", err)
		}
		relays = append(relays, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("control: list relays: %w", err)
	}

	for _, r := range relays {
		var octet int
		if _, err := fmt.Sscanf(r.subnet, "10.%d.", &octet); err != nil {
			continue // a malformed subnet is that relay's problem, not this device's
		}
		// Next free host address in this relay's /16, skipping .0.1 which the relay
		// itself holds. UNIQUE(relay_id, inner_ip) in the schema is what actually
		// guarantees two devices never share one - not this query.
		prefix := fmt.Sprintf("10.%d.", octet)
		var ip string
		err := tx.QueryRow(ctx,
			`SELECT cand FROM (
			   SELECT $2 || (n / 254) || '.' || (n % 254 + 1) || '/32' AS cand
			     FROM generate_series(1, 65000) AS n
			 ) c
			 WHERE cand NOT IN (SELECT inner_ip FROM peer_binding WHERE relay_id = $1)
			 LIMIT 1`, r.id, prefix).Scan(&ip)
		if err != nil {
			return fmt.Errorf("control: allocate inner ip on %s: %w", r.id, err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO peer_binding (device_id, relay_id, inner_ip)
			 VALUES ($1,$2,$3) ON CONFLICT (device_id, relay_id) DO NOTHING`,
			deviceID, r.id, ip); err != nil {
			return fmt.Errorf("control: bind device to %s: %w", r.id, err)
		}
	}
	return nil
}

// Session returns the relays a device may use, already filtered.
//
// Filtered, not ranked. The control plane cannot know what any individual
// player's path looks like, so it removes what is unusable and lets the client
// measure the rest.
func (s *Store) Session(ctx context.Context, contributorKey, devicePubKey string, mtu int) (api.SessionResponse, error) {
	keyHash := Hash(contributorKey)

	// The key's status, not only its existence: a device activated before its key
	// was revoked still has its row, and must not go on being handed relays.
	var deviceID string
	err := s.pool.QueryRow(ctx,
		`SELECT d.id
		   FROM device d
		   JOIN contributor_key k ON k.key_hash = d.key_hash AND k.status = 'active'
		  WHERE d.wg_pubkey = $1 AND d.key_hash = $2`,
		devicePubKey, keyHash).Scan(&deviceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return api.SessionResponse{}, ErrUnknownKey
	}
	if err != nil {
		return api.SessionResponse{}, fmt.Errorf("control: look up device: %w", err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE device SET last_seen = now() WHERE id = $1`, deviceID); err != nil {
		return api.SessionResponse{}, fmt.Errorf("control: touch device: %w", err)
	}

	rows, err := s.pool.Query(ctx,
		`SELECT r.id, r.endpoint, r.wg_pubkey, b.inner_ip, r.region
		   FROM relay r
		   JOIN peer_binding b ON b.relay_id = r.id AND b.device_id = $1
		   JOIN contributor_key rk ON rk.key_hash = r.key_hash AND rk.status = 'active'
		  WHERE r.status = 'up'
		    AND r.trusted_after <= now()
		  ORDER BY r.region, r.id`, deviceID)
	if err != nil {
		return api.SessionResponse{}, fmt.Errorf("control: list relays: %w", err)
	}
	defer rows.Close()

	out := api.SessionResponse{}
	for rows.Next() {
		o := api.RelayOffer{MTU: mtu}
		if err := rows.Scan(&o.RelayID, &o.Endpoint, &o.PublicKey, &o.InnerIP, &o.Region); err != nil {
			return api.SessionResponse{}, fmt.Errorf("control: scan relay: %w", err)
		}
		out.Relays = append(out.Relays, o)
	}
	if err := rows.Err(); err != nil {
		return api.SessionResponse{}, fmt.Errorf("control: list relays: %w", err)
	}

	var version int
	err = s.pool.QueryRow(ctx, `SELECT version FROM game_profile ORDER BY version DESC LIMIT 1`).Scan(&version)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return api.SessionResponse{}, fmt.Errorf("control: read profile version: %w", err)
	}
	out.ProfileVersion = version
	return out, nil
}

// Profile returns the current game address list.
func (s *Store) Profile(ctx context.Context) (api.ProfileResponse, error) {
	var out api.ProfileResponse
	err := s.pool.QueryRow(ctx,
		`SELECT version, cidrs FROM game_profile ORDER BY version DESC LIMIT 1`).
		Scan(&out.Version, &out.CIDRs)
	if errors.Is(err, pgx.ErrNoRows) {
		// No profile yet is a real state, not an error: a client that routes
		// nothing is correct before the first profile exists.
		return api.ProfileResponse{}, nil
	}
	if err != nil {
		return api.ProfileResponse{}, fmt.Errorf("control: read profile: %w", err)
	}
	return out, nil
}

// ReleaseDevice frees a slot.
func (s *Store) ReleaseDevice(ctx context.Context, contributorKey, deviceID string) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM device WHERE id = $1 AND key_hash = $2`, deviceID, Hash(contributorKey))
	if err != nil {
		return fmt.Errorf("control: release device: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrUnknownKey
	}
	return nil
}

// PublishProfile stores a new game profile version and returns it.
//
// Versions are append-only and the agents always fetch the newest. Keeping the
// old rows is what makes a bad publish recoverable: re-publishing the previous
// CIDR list is a normal operation rather than a restore.
func (s *Store) PublishProfile(ctx context.Context, cidrs []string) (int, error) {
	if len(cidrs) == 0 {
		// An empty profile is a real state - it means forward nothing - but it is
		// never something to publish by accident, and every path that produces one
		// is a bug somewhere upstream.
		return 0, fmt.Errorf("control: refusing to publish an empty profile")
	}
	var version int
	err := s.pool.QueryRow(ctx,
		`INSERT INTO game_profile (version, cidrs)
		 VALUES ((SELECT COALESCE(MAX(version), 0) + 1 FROM game_profile), $1)
		 RETURNING version`, cidrs).Scan(&version)
	if err != nil {
		return 0, fmt.Errorf("control: publish profile: %w", err)
	}
	return version, nil
}

// ObservedAddresses returns every address contributors have reported for a game.
func (s *Store) ObservedAddresses(ctx context.Context, gameID string) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT DISTINCT dst_ip FROM observed_address WHERE game_id = $1 ORDER BY dst_ip`, gameID)
	if err != nil {
		return nil, fmt.Errorf("control: read observations: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var ip string
		if err := rows.Scan(&ip); err != nil {
			return nil, fmt.Errorf("control: scan observation: %w", err)
		}
		out = append(out, ip)
	}
	return out, rows.Err()
}

// RecordObservation stores one address a contributor saw carrying game traffic.
//
// The reporting key is part of the primary key, so the same person reporting the
// same address twice is one observation, not two. The promotion rule counts
// independent contributors and would otherwise be satisfiable by one.
func (s *Store) RecordObservation(ctx context.Context, contributorKey, gameID, dstIP string, dstPort int) error {
	tag, err := s.pool.Exec(ctx,
		`INSERT INTO observed_address (game_id, dst_ip, dst_port, key_hash)
		 SELECT $1, $2, $3, key_hash FROM contributor_key
		  WHERE key_hash = $4 AND status = 'active'
		 ON CONFLICT (game_id, dst_ip, dst_port, key_hash)
		 DO UPDATE SET last_seen = now()`,
		gameID, dstIP, dstPort, Hash(contributorKey))
	if err != nil {
		return fmt.Errorf("control: record observation: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrUnknownKey
	}
	return nil
}

// CandidateAddresses returns addresses reported by at least minReporters
// separate contributors.
//
// This is the middle tier: one contributor seeing an address is a lead, several
// independently seeing it is evidence. A wrong CIDR drags unrelated traffic
// through somebody's relay or breaks a player's connection, so nothing reaches a
// profile on one person's word.
func (s *Store) CandidateAddresses(ctx context.Context, gameID string, minReporters int) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT o.dst_ip
		   FROM observed_address o
		   JOIN contributor_key k ON k.key_hash = o.key_hash AND k.status = 'active'
		  WHERE o.game_id = $1
		  GROUP BY o.dst_ip
		 HAVING COUNT(DISTINCT o.key_hash) >= $2
		  ORDER BY o.dst_ip`, gameID, minReporters)
	if err != nil {
		return nil, fmt.Errorf("control: read candidates: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var ip string
		if err := rows.Scan(&ip); err != nil {
			return nil, fmt.Errorf("control: scan candidate: %w", err)
		}
		out = append(out, ip)
	}
	return out, rows.Err()
}
