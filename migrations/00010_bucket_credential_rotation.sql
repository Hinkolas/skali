-- +goose Up

-- Credential rotation overlap: set while the keypair a rotation replaced
-- is still accepted by the store, cleared once it is retired. The previous
-- keypair itself lives only in the credential Secret (its previous_* keys
-- and the retire-at annotation), never in rows; this column projects the
-- deadline for the API and the console.
ALTER TABLE bucket_allocations ADD COLUMN credential_retire_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE bucket_allocations DROP COLUMN credential_retire_at;
