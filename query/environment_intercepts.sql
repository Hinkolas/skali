-- name: DeleteEnvironmentIntercepts :exec
DELETE FROM environment_intercepts WHERE environment_id = $1;

-- name: InsertEnvironmentIntercept :exec
INSERT INTO environment_intercepts (environment_id, application_key, ports)
VALUES ($1, $2, $3);

-- name: ListEnvironmentIntercepts :many
SELECT * FROM environment_intercepts
WHERE environment_id = $1
ORDER BY application_key;
