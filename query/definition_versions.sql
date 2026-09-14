-- Definition versions are immutable and content-addressed: inserting an
-- already-known (project, hash) pair is a no-op and callers re-read by hash.

-- name: InsertDefinitionVersion :execrows
INSERT INTO definition_versions
    (id, project_id, schema_version, definition_hash, definition, source, format, compiler_version)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (project_id, definition_hash) DO NOTHING;

-- name: GetDefinitionVersionByID :one
SELECT * FROM definition_versions WHERE id = $1;

-- name: GetDefinitionVersionByHash :one
SELECT * FROM definition_versions WHERE project_id = $1 AND definition_hash = $2;

-- name: CountDefinitionVersions :one
SELECT count(*) FROM definition_versions WHERE project_id = $1;
