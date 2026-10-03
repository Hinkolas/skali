-- +goose Up

-- Credential rotation for database tenants. role_name stays the owner role
-- (it owns the logical database and every object in it and never changes);
-- login_role is the role consumers log in as. Both are the same until the
-- first rotation, after which the owner never logs in again and login
-- roles alternate as members of it. A rotation moves through pending (the
-- next login role, committed by the API but not yet taken by the claim
-- worker), current, and previous (retiring at credential_retire_at). Rows
-- hold role and Secret names only; passwords live in the Secrets.
ALTER TABLE database_tenants
    ADD COLUMN login_role TEXT,
    ADD COLUMN pending_login_role TEXT,
    ADD COLUMN pending_credential_secret TEXT,
    ADD COLUMN previous_login_role TEXT,
    ADD COLUMN previous_credential_secret TEXT,
    ADD COLUMN credential_retire_at TIMESTAMPTZ;

UPDATE database_tenants SET login_role = role_name;

ALTER TABLE database_tenants ALTER COLUMN login_role SET NOT NULL;

-- +goose Down
ALTER TABLE database_tenants
    DROP COLUMN login_role,
    DROP COLUMN pending_login_role,
    DROP COLUMN pending_credential_secret,
    DROP COLUMN previous_login_role,
    DROP COLUMN previous_credential_secret,
    DROP COLUMN credential_retire_at;
