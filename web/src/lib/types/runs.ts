// Shapes mirror the skali API (api/openapi.yaml), snake_case included.

export type RunKind = 'deployment' | 'rollback' | 'teardown' | 'reconcile';

export type RunStatus = 'pending' | 'running' | 'succeeded' | 'failed' | 'cancelled';

export type StepStatus =
	'pending' | 'waiting' | 'running' | 'succeeded' | 'failed' | 'skipped' | 'cancelled';

export type AttemptStatus = 'running' | 'succeeded' | 'failed' | 'cancelled';

export interface Run {
	id: string;
	kind: RunKind;
	project_id: string | null;
	environment_id: string | null;
	actor: string;
	status: RunStatus;
	created_at: string;
	started_at: string | null;
	finished_at: string | null;
}

export interface Attempt {
	id: string;
	number: number;
	status: AttemptStatus;
	started_at: string;
	finished_at: string | null;
}

export interface Step {
	id: string;
	key: string;
	title: string;
	status: StepStatus;
	progress_current?: number;
	progress_total?: number;
	created_at: string;
	started_at: string | null;
	finished_at: string | null;
	attempts: Attempt[];
	children?: Step[];
}

/** The full run document served by GET /v1/runs/{id} and its SSE stream. */
export interface RunTree {
	run: Run;
	steps: Step[];
}

export interface RunsList {
	runs: Run[];
}

export interface LogEntry {
	attempt: number;
	seq: number;
	ts: string;
	level: string;
	message: string;
	fields: Record<string, unknown>;
}

export interface LogsPage {
	logs: LogEntry[];
	next?: string;
}

/** Whether a run can still change (its streams stay worth watching). */
export function runUnsettled(status: RunStatus): boolean {
	return status === 'pending' || status === 'running';
}
