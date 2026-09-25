package control

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"gamenolag/internal/api"
)

//go:embed schema.sql
var schemaSQL string

// Errors the HTTP layer turns into status codes.
var (
	ErrUnknownKey   = errors.New("control: unknown contributor key")
	ErrUnauthorized = errors.New("control: unauthorized")
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

	var relayID, subnet string
	err = tx.QueryRow(ctx,
		`SELECT id, inner_subnet FROM relay WHERE wg_pubkey = $1 FOR UPDATE`, in.PublicKey).
		Scan(&relayID, &subnet)

	switch {
	case errors.Is(err, pgx.ErrNoRows):
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
			 WHERE id = $5`,
			Hash(token), in.Endpoint, in.Region, in.Hostname, relayID)
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
	_, err := s.pool.Exec(ctx,
		`UPDATE relay
		    SET last_seen = now(), status = 'up',
		        active_peers = $2, rx_bytes = $3, tx_bytes = $4
		  WHERE id = $1`,
		relayID, st.ActivePeers, st.RxBytes, st.TxBytes)
	if err != nil {
		return fmt.Errorf("control: record status: %w", err)
	}
	return nil
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
