-- +goose Up

-- Sudo mode: the last moment this session proved the user's identity. Login
-- stamps it; POST /v1/auth/reauth re-stamps it. Only the timestamp is stored —
-- the freshness window lives in config (REAUTH_WINDOW), so the operator can
-- change it without touching existing sessions.
ALTER TABLE sessions ADD COLUMN reauthenticated_at TIMESTAMPTZ NOT NULL DEFAULT now();

-- Pre-existing sessions last proved identity at login, i.e. their creation.
UPDATE sessions SET reauthenticated_at = created_at;

-- +goose Down
ALTER TABLE sessions DROP COLUMN reauthenticated_at;
