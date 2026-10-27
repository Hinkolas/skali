// Shapes mirror the skali API (api/openapi.yaml), snake_case included.

import type { PlanChange, PlanValueChange } from './deploy';

export type RunKind =
	| 'deployment'
	| 'rollback'
	| 'restart'
	| 'teardown'
	| 'purge'
	| 'reconcile'
	| 'backup'
	| 'restore'
	| 'restore-accept'
	| 'rotation';

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
	/** A deployment that entered a promote-only environment on an admin's explicit bypass. */
	bypass_protection?: boolean;
	/** Routes whose TLS this run deferred because the domain did not reach here yet (list views only). */
	deferred_routes?: number;
	/** One-line reason a failed run recorded; the step logs hold the detail. */
	failure?: string;
	/** What a deployment, rollback, or restart changes; absent on other runs and older ones. */
	deployment?: RunDeployment;
}

/**
 * What a rollout run changes, as the server records it: a deployment's
 * plan (the one its deployer confirmed until promotion stores the real
 * revision), a rollback's plan back to an earlier revision, a restart's
 * applications. Lists are omitted when empty.
 */
export interface RunDeployment {
	/** Checksum of the revision the run moves the target to. */
	revision?: string;
	/** The environment a promotion copied. */
	from_environment?: string;
	changes?: PlanChange[];
	values?: RunValueChange[];
	/** Applications the run restarts, by dotted name. */
	restarts?: string[];
}

/** A plan's value change with the services that use the value (their pods roll, or their routes move). */
export interface RunValueChange extends PlanValueChange {
	/** Dotted names. */
	services?: string[];
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
	/** The service whose work the step is, by dotted name (applications.web); absent on environment-wide steps. */
	service?: string;
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

/** One builder step of a build, in the order it started. */
export interface BuildInsightsStep {
	name: string;
	status: 'done' | 'cached' | 'error' | 'canceled' | 'incomplete';
	/** Absent for cached and incomplete steps. */
	seconds?: number;
}

/** The report a build uploads once it ended (GET /v1/steps/{id}/build-insights). */
export interface BuildInsights {
	outcome: 'succeeded' | 'failed';
	engine: string;
	platform: string;
	started_at: string;
	finished_at: string;
	error?: string;
	steps: BuildInsightsStep[];
	log: string;
	log_lines: number;
	log_truncated: boolean;
}

/** A local build's step, artifacts.<application>.build: its output lives
 * in its insights report, not in the step's log. */
export function isBuildStep(key: string): boolean {
	const parts = key.split('.');
	return parts.length === 3 && parts[0] === 'artifacts' && parts[1] !== '' && parts[2] === 'build';
}

/** How long after a build finished its report may still be on its way:
 * the CLI uploads it beside the image push, for up to 30 seconds. */
export const INSIGHTS_ARRIVAL_MS = 45_000;
export const INSIGHTS_RETRY_MS = 2_000;

/** Whether a report missing at `now` may still arrive. */
export function reportMayArrive(finishedAt: string | null, now: number): boolean {
	if (!finishedAt) return false;
	return now - new Date(finishedAt).getTime() < INSIGHTS_ARRIVAL_MS;
}

/** The build's slowest steps, slowest first. */
export function slowestBuildSteps(insights: BuildInsights, count = 5): BuildInsightsStep[] {
	return insights.steps
		.filter((step) => (step.seconds ?? 0) > 0)
		.sort((a, b) => (b.seconds ?? 0) - (a.seconds ?? 0))
		.slice(0, count);
}

/**
 * How the Studio names each run kind. The API keeps its own words; the
 * Studio says stop and delete for the two teardown decisions, matching the
 * environment actions.
 */
const RUN_KIND_LABEL: Partial<Record<RunKind, string>> = {
	teardown: 'stop',
	purge: 'delete',
	'restore-accept': 'restore accept'
};

export function runKindLabel(kind: RunKind): string {
	return RUN_KIND_LABEL[kind] ?? kind;
}

/** Whether a run can still change (its streams stay worth watching). */
export function runUnsettled(status: RunStatus): boolean {
	return status === 'pending' || status === 'running';
}

/**
 * The run an environment is working on now: the running one (an
 * environment runs one at a time), else the newest one still queued. Runs
 * come newest first, as the API lists them.
 */
export function activeRun(runs: readonly Run[]): Run | null {
	return (
		runs.find((r) => r.status === 'running') ?? runs.find((r) => r.status === 'pending') ?? null
	);
}

export interface FollowedRun {
	envId: string;
	runId: string;
}

/**
 * The run a live view follows: the selected environment's active run, else
 * the run it already followed there, so an outcome stays on screen after
 * the run ends. A run of another environment is never kept. Returns
 * `followed` itself when nothing changed.
 */
export function followRun(
	followed: FollowedRun | null,
	envId: string | null,
	activeId: string | null
): FollowedRun | null {
	if (envId && activeId) {
		return followed?.envId === envId && followed.runId === activeId
			? followed
			: { envId, runId: activeId };
	}
	return followed?.envId === envId ? followed : null;
}

/**
 * Whether cancelling the run undoes anything. A stop or a delete is a
 * persisted decision the reconciler executes whatever happens to its run,
 * so a cancel would only end the journal while the work goes on.
 */
export function runCancellable(run: Run): boolean {
	return runUnsettled(run.status) && run.kind !== 'teardown' && run.kind !== 'purge';
}
