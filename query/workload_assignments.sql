-- name: InsertWorkloadAssignment :exec
INSERT INTO workload_assignments (workload_id, ordinal, container_name)
VALUES ($1, $2, $3);

-- The reconciler's bulk read, one query per pass.
-- name: ListWorkloadAssignments :many
SELECT * FROM workload_assignments ORDER BY workload_id, ordinal;

-- Convergence re-reads its own rows: fresher than the pass snapshot, and
-- the in-flight guard keeps each workload single-writer.
-- name: ListAssignmentsForWorkload :many
SELECT * FROM workload_assignments WHERE workload_id = $1 ORDER BY ordinal;

-- The API's list: assignments with their node names. COALESCE because
-- node_id may be NULL (unplaced slots).
-- name: ListWorkloadAssignmentsWithNodes :many
SELECT sqlc.embed(workload_assignments), COALESCE(nodes.name, '') AS node_name
FROM workload_assignments
LEFT JOIN nodes ON nodes.id = workload_assignments.node_id
ORDER BY workload_assignments.workload_id, workload_assignments.ordinal;

-- name: AssignAssignmentNode :exec
UPDATE workload_assignments SET
    node_id = $3, phase = 'pending', last_error = NULL, updated_at = now()
WHERE workload_id = $1 AND ordinal = $2;

-- name: SetAssignmentPhase :exec
UPDATE workload_assignments SET phase = $3, updated_at = now()
WHERE workload_id = $1 AND ordinal = $2;

-- Convergence success: the slot reached its target phase for this
-- generation and the error state clears.
-- name: MarkAssignmentConverged :exec
UPDATE workload_assignments SET
    phase = $3, container_id = $4, generation = $5,
    last_error = NULL, retries = 0, next_attempt_at = NULL, updated_at = now()
WHERE workload_id = $1 AND ordinal = $2;

-- name: MarkAssignmentFailed :exec
UPDATE workload_assignments SET
    last_error = $3, retries = retries + 1, next_attempt_at = $4, updated_at = now()
WHERE workload_id = $1 AND ordinal = $2;

-- Blocked ≠ failed: an offline node surfaces without burning retries — the
-- slot converges the moment the node returns.
-- name: MarkAssignmentBlocked :exec
UPDATE workload_assignments SET last_error = $3, updated_at = now()
WHERE workload_id = $1 AND ordinal = $2;

-- name: MarkAssignmentUnschedulable :exec
UPDATE workload_assignments SET
    phase = 'unschedulable', node_id = NULL, last_error = $3, updated_at = now()
WHERE workload_id = $1 AND ordinal = $2;

-- name: DeleteWorkloadAssignment :execrows
DELETE FROM workload_assignments WHERE workload_id = $1 AND ordinal = $2;
