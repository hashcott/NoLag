-- Applied on start. Every statement is IF NOT EXISTS so this is idempotent and
-- needs no migration framework at this size.

CREATE TABLE IF NOT EXISTS contributor_key (
    key_hash      TEXT PRIMARY KEY,
    status        TEXT        NOT NULL DEFAULT 'active',
    granted_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Contributors from the free phase keep access permanently, including after
    -- the product starts charging. Recorded here so the promise outlives whoever
    -- remembers making it.
    grandfathered BOOLEAN     NOT NULL DEFAULT true
);

CREATE TABLE IF NOT EXISTS relay (
    id            TEXT        PRIMARY KEY,
    key_hash      TEXT        NOT NULL REFERENCES contributor_key(key_hash),
    region        TEXT        NOT NULL DEFAULT '',
    hostname      TEXT        NOT NULL DEFAULT '',
    endpoint      TEXT        NOT NULL,
    wg_pubkey     TEXT        NOT NULL UNIQUE,
    inner_subnet  TEXT        NOT NULL,
    -- pending until a reachability check or a real handshake proves the UDP port
    -- is open in the provider's security group as well as on the host.
    status        TEXT        NOT NULL DEFAULT 'pending',
    last_seen     TIMESTAMPTZ,
    active_peers  INT         NOT NULL DEFAULT 0,
    rx_bytes      BIGINT      NOT NULL DEFAULT 0,
    tx_bytes      BIGINT      NOT NULL DEFAULT 0,
    -- A newly registered relay carries no user traffic until this passes, which
    -- leaves an observation window before it can attract anyone.
    trusted_after TIMESTAMPTZ NOT NULL,
    capacity      INT         NOT NULL DEFAULT 200,
    token_hash    TEXT        NOT NULL UNIQUE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS device (
    id         TEXT        PRIMARY KEY,
    key_hash   TEXT        NOT NULL REFERENCES contributor_key(key_hash),
    wg_pubkey  TEXT        NOT NULL UNIQUE,
    name       TEXT        NOT NULL DEFAULT '',
    last_seen  TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS peer_binding (
    device_id TEXT NOT NULL REFERENCES device(id) ON DELETE CASCADE,
    relay_id  TEXT NOT NULL REFERENCES relay(id)  ON DELETE CASCADE,
    inner_ip  TEXT NOT NULL,
    PRIMARY KEY (device_id, relay_id),
    -- Two devices on one relay can never hold the same address. Enforced here
    -- rather than in application code: this is the invariant that stops one
    -- client receiving another's return traffic.
    UNIQUE (relay_id, inner_ip)
);

-- The active game CIDR list the agents pull. One row per published version;
-- agents always receive the newest.
CREATE TABLE IF NOT EXISTS game_profile (
    version    INT         PRIMARY KEY,
    cidrs      TEXT[]      NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Relay /16 allocation. A sequence rather than a row count: deleting a relay must
-- not cause the next registration to reuse a subnet that is still in service.
CREATE SEQUENCE IF NOT EXISTS relay_octet_seq START 77 MAXVALUE 255;

CREATE INDEX IF NOT EXISTS relay_status_idx     ON relay (status, last_seen);
CREATE INDEX IF NOT EXISTS peer_binding_relay_idx ON peer_binding (relay_id);
