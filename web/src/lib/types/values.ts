// Shapes mirror the skali API (api/openapi.yaml), snake_case included.

/** One current environment value; secret entries never carry `value`. */
export interface ValueEntry {
	name: string;
	secret: boolean;
	version: number;
	value?: string;
}

export interface ValuesResponse {
	values: ValueEntry[];
}

/** PUT /v1/environments/{id}/values response: the staged candidate. */
export interface StageValuesResult {
	candidate_id: string;
	plain: string[];
	secret: string[];
}
