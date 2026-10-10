-- The artifacts a revision leases.
-- name: ListLeasedArtifacts :many
SELECT artifacts.* FROM artifacts
JOIN artifact_leases ON artifact_leases.artifact_id = artifacts.id
WHERE artifact_leases.revision_id = $1;
