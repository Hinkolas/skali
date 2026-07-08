// Task-shaped background work (GET /v1/operations): the read side of every
// 202 the API returns for long-running requests.

export type OperationStatus = 'running' | 'succeeded' | 'failed';

export interface Operation {
	id: string;
	/** e.g. "registry_import". */
	kind: string;
	status: OperationStatus;
	/** Kind-specific, e.g. the upstream reference being imported. */
	subject: string;
	/** Kind-specific success payload; {} until then. */
	result: Record<string, unknown>;
	/** Present when failed. */
	error?: string;
	created_at: string;
	updated_at: string;
	/** Present once the operation leaves "running". */
	finished_at?: string;
}
