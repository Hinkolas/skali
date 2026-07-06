-- +goose Up

-- Instance-wide role, the whole permission model for now: 'admin' additionally
-- manages users and instance settings; 'member' has full access to all
-- projects. Finer-grained scoping (project membership, protected environments)
-- layers on top of this later without changing the column.
ALTER TABLE users ADD COLUMN role TEXT NOT NULL DEFAULT 'member'
    CHECK (role IN ('admin', 'member'));

-- Existing installs predate roles, where every user could do everything;
-- keeping them all admins preserves that. Only newly created users default
-- to 'member'.
UPDATE users SET role = 'admin';

-- +goose Down
ALTER TABLE users DROP COLUMN role;
