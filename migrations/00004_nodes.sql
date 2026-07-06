-- +goose Up

-- Cluster machines. The master creates its own row on first `serve` boot;
-- workers get one at enrollment. status is derived by the master's heartbeat
-- poller (online/offline from last_seen vs a staleness threshold) and
-- persisted so list reads stay trivial. cert_serial is the revocation handle:
-- the master refuses any presented node cert whose serial does not match the
-- row, so deleting/re-enrolling a node invalidates its old identity.
CREATE TABLE nodes (
    id             UUID PRIMARY KEY,
    name           TEXT NOT NULL,
    roles          TEXT[] NOT NULL DEFAULT '{worker}'
                   CHECK (roles <@ ARRAY['master','edge','worker','builder']::text[]
                          AND array_length(roles, 1) >= 1),
    advertise_addr TEXT NOT NULL DEFAULT '',  -- host:port the master dials for NodeService
    public_addr    TEXT,                      -- operator-set (edge role): where public DNS points
    arch           TEXT,                      -- reported by heartbeat (amd64|arm64|...)
    os             TEXT,
    skalid_version TEXT,
    cert_serial    TEXT,                      -- hex serial of the node's current mTLS cert
    status         TEXT NOT NULL DEFAULT 'offline' CHECK (status IN ('online', 'offline')),
    last_seen      TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One-time node enrollment secrets. The full token a client holds is
-- <id>.<secret>.<ca-fingerprint>; only sha256(secret) is stored (mirrors
-- sessions.token_hash). used_at is the burn mark: enrollment sets it
-- atomically, so a token can never admit two nodes.
CREATE TABLE join_tokens (
    id         UUID PRIMARY KEY,
    hash       BYTEA NOT NULL UNIQUE,
    roles      TEXT[] NOT NULL DEFAULT '{worker}'
               CHECK (roles <@ ARRAY['edge','worker','builder']::text[]
                      AND array_length(roles, 1) >= 1),
    expires_at TIMESTAMPTZ NOT NULL,
    used_at    TIMESTAMPTZ,
    created_by UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The cluster certificate authority — exactly one row (boolean PK trick).
-- key_cipher is the CA private key sealed with internal/crypt: AES-256-GCM
-- keyed from AUTH_SECRET (HKDF info "skali/cluster/ca-key/v1"). Rotating
-- AUTH_SECRET therefore orphans the CA and every node identity signed by it —
-- all nodes would need to re-enroll.
CREATE TABLE cluster_ca (
    id         BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id),
    cert_pem   TEXT NOT NULL,
    key_cipher BYTEA NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE cluster_ca;
DROP TABLE join_tokens;
DROP TABLE nodes;
