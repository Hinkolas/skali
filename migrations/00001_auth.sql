-- +goose Up

-- App-user accounts. Login is by email (case-insensitive). skali is
-- self-hosted: users are created by the operator via `skalid user create`;
-- there is no public signup endpoint.
CREATE TABLE users (
    id                 UUID PRIMARY KEY,
    email              TEXT NOT NULL,
    name               TEXT NOT NULL DEFAULT '',
    two_factor_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- Functional index instead of citext: avoids CREATE EXTENSION for self-hosters.
-- Every email lookup must go through lower(email) = lower($1).
CREATE UNIQUE INDEX users_email_key ON users (lower(email));

-- Login methods, separate from users (better-auth shape): today only
-- provider_id = 'credential' (password), leaving room for optional OIDC later.
CREATE TABLE accounts (
    id          UUID PRIMARY KEY,
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider_id TEXT NOT NULL,
    password    TEXT, -- argon2id PHC string; only for provider_id = 'credential'
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, provider_id)
);

-- Bearer sessions. token_hash is sha256 of the opaque token; the plaintext
-- token exists only in the login response.
CREATE TABLE sessions (
    id         UUID PRIMARY KEY,
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash BYTEA NOT NULL UNIQUE,
    expires_at TIMESTAMPTZ NOT NULL,
    ip_address TEXT NOT NULL DEFAULT '',
    user_agent TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX sessions_user_id_idx ON sessions (user_id);
CREATE INDEX sessions_expires_at_idx ON sessions (expires_at);

-- TOTP enrollment, one per user. secret is AES-256-GCM encrypted
-- (version byte || nonce || ciphertext), keyed from AUTH_SECRET.
-- confirmed_at IS NULL means enrollment is pending the first valid code.
-- last_used_step only ever advances, which rejects TOTP replay atomically.
CREATE TABLE two_factors (
    id             UUID PRIMARY KEY,
    user_id        UUID NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,
    secret         BYTEA NOT NULL,
    confirmed_at   TIMESTAMPTZ,
    last_used_step BIGINT NOT NULL DEFAULT 0,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Single-use 2FA recovery codes. Hashed with argon2id, not sha256: the codes
-- are short enough to brute-force offline if the database leaks.
CREATE TABLE backup_codes (
    id            UUID PRIMARY KEY,
    two_factor_id UUID NOT NULL REFERENCES two_factors(id) ON DELETE CASCADE,
    code_hash     TEXT NOT NULL,
    used_at       TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX backup_codes_two_factor_id_idx ON backup_codes (two_factor_id);

-- Short-lived tokens bridging password login and 2FA verification for users
-- with 2FA enabled. DB-backed so they survive restarts and multiple daemon
-- instances. attempts caps code guesses per challenge.
CREATE TABLE login_challenges (
    id         UUID PRIMARY KEY,
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash BYTEA NOT NULL UNIQUE,
    attempts   INT NOT NULL DEFAULT 0,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE login_challenges;
DROP TABLE backup_codes;
DROP TABLE two_factors;
DROP TABLE sessions;
DROP TABLE accounts;
DROP TABLE users;
