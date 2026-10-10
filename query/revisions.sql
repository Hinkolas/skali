-- Revisions are immutable: insert and read, never update. StoreRevision
-- stores one and leases its artifacts in one statement, returning the
-- canonical row's id: re-preparing identical inputs reuses the row via the
-- (environment, checksum) unique, and the row gains any lease it lacks. A
-- row another transaction stored after this statement's snapshot is neither
-- inserted nor found, and no row returns.
-- name: StoreRevision :one
WITH inserted AS (
    INSERT INTO revisions
        (id, project_id, environment_id, definition_version_id, schema_version,
         checksum, definition_hash, values_hash, compiler_version, document)
    VALUES (sqlc.arg(id), sqlc.arg(project_id), sqlc.arg(environment_id), sqlc.arg(definition_version_id),
            sqlc.arg(schema_version), sqlc.arg(checksum), sqlc.arg(definition_hash), sqlc.arg(values_hash),
            sqlc.arg(compiler_version), sqlc.arg(document))
    ON CONFLICT (environment_id, checksum) DO NOTHING
    RETURNING revisions.id
), revision AS (
    SELECT inserted.id FROM inserted
    UNION ALL
    SELECT revisions.id FROM revisions
    WHERE revisions.environment_id = sqlc.arg(environment_id) AND revisions.checksum = sqlc.arg(checksum)
), leases AS (
    INSERT INTO artifact_leases (revision_id, artifact_id)
    SELECT revision.id, artifact_id FROM revision, unnest(sqlc.arg(artifact_ids)::uuid[]) AS artifact_id
    ON CONFLICT DO NOTHING
)
SELECT revision.id FROM revision;

-- name: GetRevisionByID :one
SELECT * FROM revisions WHERE id = $1;

-- name: ListRevisions :many
SELECT id, project_id, environment_id, definition_version_id, schema_version,
       checksum, definition_hash, values_hash, compiler_version, created_at
FROM revisions
WHERE environment_id = $1
ORDER BY created_at DESC;
