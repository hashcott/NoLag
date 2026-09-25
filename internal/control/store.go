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
			  WHERE wg_pubkey = $1 AND key_hash = $2`,
			relayPubKey, keyHash)
	} else {
		// Not cleared: a relay that was reachable and now is not keeps its
		// reachable_at, so the record shows it worked once. Status says it does not
		// work now, which is what selection needs to read.
		tag, err = s.pool.Exec(ctx,
			`UPDATE relay
			    SET status = 'unreachable', unreachable_detail = $3
			  WHERE wg_pubkey = $1 AND key_hash = $2`,
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
// No published profile is not an error: it means nothing may be forwarded yet,
// which is the correct state for a relay brought up before the first profile
// exists.
func (s *Store) DesiredState(ctx context.Context, relayID string) ([]api.Peer, []string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT d.wg_pubkey, b.inner_ip
		   FROM peer_binding b
		   JOIN device d ON d.id = b.device_id
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
