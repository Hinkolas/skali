-- name: CreateProjectDraft :one
INSERT INTO project_drafts (project_id, definition_version_id)
VALUES ($1, $2)
RETURNING *;

-- name: GetProjectDraft :one
SELECT * FROM project_drafts WHERE project_id = $1;

-- Every project's current draft definition, for service-count summaries on
-- the project list.
-- name: ListDraftDefinitions :many
SELECT d.project_id, v.definition
FROM project_drafts d
JOIN definition_versions v ON v.id = d.definition_version_id;

-- Optimistic compare-and-swap: zero rows updated means the caller's expected
-- version is stale and the submission must be rejected, not merged.
-- name: UpdateProjectDraft :execrows
UPDATE project_drafts
SET version = version + 1, definition_version_id = $3, updated_at = now()
WHERE project_id = $1 AND version = $2;

-- Unconditional advance used inside the deploy promotion transaction, where
-- the promoted definition wins by design. Upserts because a project that has
-- only ever been deployed from the CLI has no draft row yet.
-- name: AdvanceProjectDraft :execrows
INSERT INTO project_drafts (project_id, definition_version_id)
VALUES ($1, $2)
ON CONFLICT (project_id) DO UPDATE
SET version = project_drafts.version + 1,
    definition_version_id = EXCLUDED.definition_version_id,
    updated_at = now()
WHERE project_drafts.definition_version_id <> EXCLUDED.definition_version_id;
