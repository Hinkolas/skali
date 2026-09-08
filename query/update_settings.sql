-- name: GetUpdateSettings :one
SELECT * FROM update_settings;

-- name: SaveUpdateSettings :one
UPDATE update_settings
SET channel     = $1,
    auto_update = $2,
    updated_at  = now()
RETURNING *;

-- A scan result replaces the previous one wholesale; a failed scan keeps
-- the last release found and records why the check did not complete.
-- name: RecordUpdateScan :one
UPDATE update_settings
SET last_checked_at     = now(),
    latest_version      = COALESCE(sqlc.narg('latest_version'), latest_version),
    latest_k3s          = COALESCE(sqlc.narg('latest_k3s'), latest_k3s),
    latest_published_at = COALESCE(sqlc.narg('latest_published_at'), latest_published_at),
    latest_url          = COALESCE(sqlc.narg('latest_url'), latest_url),
    last_error          = sqlc.narg('last_error'),
    last_error_kind     = sqlc.narg('last_error_kind'),
    updated_at          = now()
RETURNING *;
