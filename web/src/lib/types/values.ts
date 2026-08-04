// Shapes mirror the skali API (api/openapi.yaml), snake_case included.

/** One current environment value; values are write-only, name and version only. */
export interface ValueEntry {
	name: string;
	version: number;
}

export interface ValuesResponse {
	values: ValueEntry[];
}

/** PUT /v1/environments/{id}/values response: the staged candidate. */
export interface StageValuesResult {
	candidate_id: string;
	staged: string[];
	skipped: string[];
}
