-- +goose Up

-- Browser device authorization for the CLI. A device request is the
-- short-lived handshake between a terminal and a browser: the CLI creates
-- one, shows the user code, and polls with the device code; the signed-in
-- browser looks the user code up and approves or denies it. Two intents:
-- login mints a session for the approver on the CLI's next poll; reauth
-- re-stamps the bound CLI session (session_id) with the approver's fresh
-- identity, so a sudo-gated command confirms in the browser instead of
-- retyping the password. Only the device code's hash is stored; the
-- plaintext lives in the CLI process alone.
CREATE TABLE device_requests (
    id               UUID PRIMARY KEY,
    intent           TEXT NOT NULL CHECK (intent IN ('login', 'reauth')),
    user_code        TEXT NOT NULL UNIQUE,
    device_code_hash BYTEA NOT NULL UNIQUE,
    status           TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'denied')),
    -- reauth: the CLI session the approval re-stamps; login: unused.
    session_id       UUID REFERENCES sessions(id) ON DELETE CASCADE,
    -- reauth: the session's owner, set at creation; login: the approver,
    -- set at approval.
    user_id          UUID REFERENCES users(id) ON DELETE CASCADE,
    client_label     TEXT NOT NULL DEFAULT '',
    ip_address       TEXT NOT NULL DEFAULT '',
    user_agent       TEXT NOT NULL DEFAULT '',
    poll_count       INTEGER NOT NULL DEFAULT 0,
    last_polled_at   TIMESTAMPTZ,
    expires_at       TIMESTAMPTZ NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX device_requests_expires_at_idx ON device_requests (expires_at);

-- +goose Down
DROP TABLE device_requests;
