// Shapes mirror the skali API (api/openapi.yaml), snake_case included.

export interface RevisionSummary {
	id: string;
	project_id: string;
	environment_id: string;
	definition_version_id: string;
	schema_version: string;
	checksum: string;
	definition_hash: string;
	values_hash: string;
	compiler_version: string;
	created_at: string;
}

export interface Target {
	target_revision_id: string | null;
	active_revision_id: string | null;
	updated_at: string;
}
