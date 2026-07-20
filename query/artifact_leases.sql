-- name: CreateArtifactLease :exec
INSERT INTO artifact_leases (revision_id, artifact_id)
VALUES ($1, $2)
ON CONFLICT DO NOTHING;

-- name: ListArtifactLeasesByRevision :many
SELECT * FROM artifact_leases WHERE revision_id = $1;
