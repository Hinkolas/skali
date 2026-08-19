-- name: UpsertProjectMember :one
INSERT INTO project_members (project_id, user_id, role)
VALUES ($1, $2, $3)
ON CONFLICT (project_id, user_id) DO UPDATE
    SET role = EXCLUDED.role, updated_at = now()
RETURNING *;

-- name: GetProjectMember :one
SELECT * FROM project_members WHERE project_id = $1 AND user_id = $2;

-- One project's members with the user columns the access surfaces show.
-- name: ListProjectMembers :many
SELECT m.project_id, m.user_id, m.role, m.created_at, m.updated_at, u.email, u.name
FROM project_members m
JOIN users u ON u.id = m.user_id
WHERE m.project_id = $1
ORDER BY lower(u.email);

-- name: DeleteProjectMember :execrows
DELETE FROM project_members WHERE project_id = $1 AND user_id = $2;

-- Every membership of one user, the resolver's first read.
-- name: ListProjectMembersForUser :many
SELECT * FROM project_members WHERE user_id = $1;

-- name: ListProjectsForUser :many
SELECT p.* FROM projects p
JOIN project_members m ON m.project_id = p.id
WHERE m.user_id = $1
ORDER BY p.name;

-- Deployer somewhere: a project role of deploy or above, or any cell of
-- deploy or above. Gates the shared registry cache repositories.
-- name: UserIsDeployerAnywhere :one
SELECT (EXISTS (
    SELECT 1 FROM project_members m
    WHERE m.user_id = @user_id AND m.role IN ('deploy', 'maintain', 'admin')
) OR EXISTS (
    SELECT 1 FROM environment_access a
    WHERE a.user_id = @user_id AND a.role IN ('deploy', 'maintain', 'admin')
))::boolean AS deployer;

-- Member users without any project membership; named by the migrate notice.
-- name: ListMembersWithoutMembership :many
SELECT u.email FROM users u
WHERE u.role = 'member'
  AND NOT EXISTS (SELECT 1 FROM project_members m WHERE m.user_id = u.id)
ORDER BY lower(u.email);
