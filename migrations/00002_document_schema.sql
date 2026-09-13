-- +goose Up
-- Stored documents carry their own schema generation, an integer the
-- server owns, instead of the manifest's version knob (docs/versioning.md,
-- decision 4). Every row written so far reads 1 either way; the JSON
-- documents inside keep their legacy envelope and the decoders read both.
ALTER TABLE definition_versions ALTER COLUMN schema_version TYPE INTEGER USING schema_version::integer;
ALTER TABLE definition_versions RENAME COLUMN schema_version TO schema;
ALTER TABLE revisions ALTER COLUMN schema_version TYPE INTEGER USING schema_version::integer;
ALTER TABLE revisions RENAME COLUMN schema_version TO schema;

-- +goose Down
ALTER TABLE revisions RENAME COLUMN schema TO schema_version;
ALTER TABLE revisions ALTER COLUMN schema_version TYPE TEXT USING schema_version::text;
ALTER TABLE definition_versions RENAME COLUMN schema TO schema_version;
ALTER TABLE definition_versions ALTER COLUMN schema_version TYPE TEXT USING schema_version::text;
