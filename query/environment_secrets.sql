-- The single environment value store: every value is a secret, stored as
-- ciphertext in append-only per-name versions. Version listings deliberately
-- never select the ciphertext; only the redaction and resolution paths read
-- it.

-- name: StageEnvironmentSecret :one
INSERT INTO environment_secrets (id, environment_id, name, version, ciphertext, state, candidate_id)
SELECT $1, $2, $3, COALESCE(MAX(version), 0) + 1, $4, 'staged', $5
FROM environment_secrets WHERE environment_id = $2 AND name = $3
RETURNING id, environment_id, name, version, state, candidate_id, created_at;

-- name: ListCurrentEnvironmentSecretVersions :many
SELECT name, version FROM environment_secrets
WHERE environment_id = $1 AND state = 'current'
ORDER BY name;

-- name: ListStagedEnvironmentSecretVersions :many
SELECT name, version FROM environment_secrets
WHERE environment_id = $1 AND candidate_id = $2 AND state = 'staged'
ORDER BY name;

-- name: ListCurrentEnvironmentSecretCiphertexts :many
SELECT name, version, ciphertext FROM environment_secrets
WHERE environment_id = $1 AND state = 'current'
ORDER BY name;

-- name: ListStagedEnvironmentSecretCiphertexts :many
SELECT name, ciphertext FROM environment_secrets
WHERE environment_id = $1 AND candidate_id = $2 AND state = 'staged'
ORDER BY name;

-- Resolution path only: decrypting the exact version a revision pinned.
-- Superseded rows are retained precisely so this keeps resolving.
-- name: GetEnvironmentSecretCiphertext :one
SELECT ciphertext FROM environment_secrets
WHERE environment_id = $1 AND name = $2 AND version = $3;

-- name: SupersedeCurrentEnvironmentSecrets :exec
UPDATE environment_secrets AS live SET state = 'superseded'
WHERE live.environment_id = $1 AND live.state = 'current' AND live.name IN (
    SELECT staged.name FROM environment_secrets AS staged
    WHERE staged.environment_id = $1 AND staged.candidate_id = $2 AND staged.state = 'staged'
);

-- name: PromoteStagedEnvironmentSecrets :execrows
UPDATE environment_secrets SET state = 'current', candidate_id = NULL
WHERE environment_id = $1 AND candidate_id = $2 AND state = 'staged';

-- Discard and sweep never read the ciphertext column.
-- name: DiscardStagedEnvironmentSecrets :execrows
DELETE FROM environment_secrets
WHERE environment_id = $1 AND candidate_id = $2 AND state = 'staged';

-- name: SweepStagedEnvironmentSecrets :execrows
DELETE FROM environment_secrets WHERE state = 'staged' AND created_at < $1;

-- Tombstone: supersede the current generation without a successor. The value
-- disappears from future revisions while pinned (name, version) resolution
-- keeps working; a later stage of the same name continues the version
-- sequence because version allocation ignores state.
-- name: UnsetCurrentEnvironmentSecrets :execrows
UPDATE environment_secrets SET state = 'superseded'
WHERE environment_id = @environment_id AND state = 'current' AND name = ANY(@names::text[]);
