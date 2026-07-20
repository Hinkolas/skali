-- Staged rows are the only deletable rows; promoted history is permanent.
-- The MAX+1 version allocation runs inside the staging transaction; a
-- concurrent allocation for the same name surfaces as a unique violation.

-- name: StageEnvironmentValue :one
INSERT INTO environment_values (id, environment_id, name, version, value, state, candidate_id)
SELECT $1, $2, $3, COALESCE(MAX(version), 0) + 1, $4, 'staged', $5
FROM environment_values WHERE environment_id = $2 AND name = $3
RETURNING *;

-- name: ListCurrentEnvironmentValues :many
SELECT * FROM environment_values
WHERE environment_id = $1 AND state = 'current'
ORDER BY name;

-- name: ListStagedEnvironmentValues :many
SELECT * FROM environment_values
WHERE environment_id = $1 AND candidate_id = $2 AND state = 'staged'
ORDER BY name;

-- name: SupersedeCurrentEnvironmentValues :exec
UPDATE environment_values AS live SET state = 'superseded'
WHERE live.environment_id = $1 AND live.state = 'current' AND live.name IN (
    SELECT staged.name FROM environment_values AS staged
    WHERE staged.environment_id = $1 AND staged.candidate_id = $2 AND staged.state = 'staged'
);

-- name: PromoteStagedEnvironmentValues :execrows
UPDATE environment_values SET state = 'current', candidate_id = NULL
WHERE environment_id = $1 AND candidate_id = $2 AND state = 'staged';

-- Discard and sweep never read the value column.
-- name: DiscardStagedEnvironmentValues :execrows
DELETE FROM environment_values
WHERE environment_id = $1 AND candidate_id = $2 AND state = 'staged';

-- name: SweepStagedEnvironmentValues :execrows
DELETE FROM environment_values WHERE state = 'staged' AND created_at < $1;
