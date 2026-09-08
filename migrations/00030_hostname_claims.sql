-- +goose Up
CREATE TABLE hostname_claims (
 hostname TEXT PRIMARY KEY,
 environment_id UUID REFERENCES environments(id) ON DELETE RESTRICT,
 target_revision_id UUID REFERENCES revisions(id),
 reserved BOOLEAN NOT NULL DEFAULT false,
 CHECK ((reserved AND environment_id IS NULL AND target_revision_id IS NULL) OR (NOT reserved AND environment_id IS NOT NULL))
);
CREATE INDEX hostname_claims_environment ON hostname_claims(environment_id);

-- +goose Down
DROP TABLE hostname_claims;
