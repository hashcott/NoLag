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

-- How many devices one contributor key may activate at once. Spec 8: a key is a
-- credential people can share, and device slots are what makes sharing it
-- pointless rather than forbidden.
ALTER TABLE contributor_key ADD COLUMN IF NOT EXISTS max_devices INT NOT NULL DEFAULT 3;

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
    -- Shown back when a key runs out of slots, so the person can tell which of
    -- their own machines to release rather than guessing at opaque ids.
    fingerprint TEXT       NOT NULL DEFAULT '',
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
-- When an external check last proved this relay reachable. A relay that syncs has
-- only shown it can reach US; the provider's security group sits in front of its
-- UDP port and is invisible from inside the machine, so outbound success says
-- nothing about whether a player can get in. NULL means never verified.
ALTER TABLE relay ADD COLUMN IF NOT EXISTS reachable_at TIMESTAMPTZ;
ALTER TABLE relay ADD COLUMN IF NOT EXISTS unreachable_detail TEXT NOT NULL DEFAULT '';

CREATE SEQUENCE IF NOT EXISTS relay_octet_seq START 77 MAXVALUE 255;

-- Addresses contributors have seen carrying a game's traffic.
--
-- Destination only. Not the payload, not the source address: building a profile
-- needs where the game lives and nothing else, and keeping more would turn this
-- into a record of what people were doing.
--
-- The reporting key IS part of the key, because the promotion rule is "three
-- INDEPENDENT contributors". Counting reports instead would let one person
-- reporting the same address three times promote it on their own - which is
-- exactly the case the rule exists to exclude.
CREATE TABLE IF NOT EXISTS observed_address (
    game_id    TEXT        NOT NULL,
    dst_ip     TEXT        NOT NULL,
    dst_port   INT         NOT NULL,
    key_hash   TEXT        NOT NULL,
    first_seen TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (game_id, dst_ip, dst_port, key_hash)
);

CREATE INDEX IF NOT EXISTS relay_status_idx     ON relay (status, last_seen);
CREATE INDEX IF NOT EXISTS peer_binding_relay_idx ON peer_binding (relay_id);
