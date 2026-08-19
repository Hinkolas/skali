-- +goose Up

-- Project membership: one role per (project, user) on the shared ladder
-- read < deploy < maintain < admin. The role is the member's default on
-- every environment of the project and carries the project-level rights
-- (docs/permissions.md). Non-members do not see the project at all. What a
-- role means lives in internal/authz, never here.
CREATE TABLE project_members (
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role       TEXT NOT NULL CHECK (role IN ('read', 'deploy', 'maintain', 'admin')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, user_id)
);
CREATE INDEX project_members_user_id_idx ON project_members (user_id);

-- Cells override the project role for one user on one environment, up or
-- down, 'none' included (locked: listed by name, contents refused). A cell
-- hangs off the membership: the composite key to project_members drops the
-- cells when the member leaves, and the composite key to environments keeps
-- project_id honest (a cell can only reference an environment of the
-- project whose membership it extends).
ALTER TABLE environments ADD CONSTRAINT environments_id_project_id_key UNIQUE (id, project_id);

CREATE TABLE environment_access (
    environment_id UUID NOT NULL,
    project_id     UUID NOT NULL,
    user_id        UUID NOT NULL,
    role           TEXT NOT NULL CHECK (role IN ('none', 'read', 'deploy', 'maintain', 'admin')),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (environment_id, user_id),
    FOREIGN KEY (environment_id, project_id)
        REFERENCES environments(id, project_id) ON DELETE CASCADE,
    FOREIGN KEY (project_id, user_id)
        REFERENCES project_members(project_id, user_id) ON DELETE CASCADE
);
CREATE INDEX environment_access_project_user_idx ON environment_access (project_id, user_id);

-- Environment settings, edited by environment admins server-side (nothing
-- of this lives in skali.yml): max_role caps the role members inherit from
-- their project role (cells are not capped); deploy_policy and promote_from
-- are the protection policy (enforced by a later change); priority marks
-- environments that keep running when resources are tight and is raised to
-- 'high' by instance admins only.
ALTER TABLE environments ADD COLUMN max_role TEXT NOT NULL DEFAULT 'admin'
    CHECK (max_role IN ('none', 'read', 'deploy', 'maintain', 'admin'));
ALTER TABLE environments ADD COLUMN deploy_policy TEXT NOT NULL DEFAULT 'direct'
    CHECK (deploy_policy IN ('direct', 'promote-only'));
ALTER TABLE environments ADD COLUMN promote_from TEXT[] NOT NULL DEFAULT '{}';
ALTER TABLE environments ADD COLUMN priority TEXT NOT NULL DEFAULT 'normal'
    CHECK (priority IN ('normal', 'high'));

-- Members may create projects only when granted; instance admins always
-- may. Whoever creates a project becomes its admin.
ALTER TABLE users ADD COLUMN create_projects BOOLEAN NOT NULL DEFAULT FALSE;

-- No backfill: existing member users keep their accounts and hold no
-- project membership until an admin grants one (skalid migrate prints the
-- affected emails). Admins are unaffected.

-- +goose Down

ALTER TABLE users DROP COLUMN create_projects;
ALTER TABLE environments DROP COLUMN priority;
ALTER TABLE environments DROP COLUMN promote_from;
ALTER TABLE environments DROP COLUMN deploy_policy;
ALTER TABLE environments DROP COLUMN max_role;
DROP TABLE environment_access;
ALTER TABLE environments DROP CONSTRAINT environments_id_project_id_key;
DROP TABLE project_members;
