// A run's progress per service: the steps of its tree grouped by the
// service each records (step.service), one row per service the way the
// CLI's deploy view lists them. Steps without a service are the
// environment's own work (validate, render, prune, verify, activate).

import type { ServiceKind } from '$lib/service-types';
import type { Step, StepStatus } from '$lib/types/runs';

export interface ServiceProgress {
	/** Dotted name, e.g. "applications.web"; null for the environment's own work. */
	service: string | null;
	/** Null for the environment and for a collection this Studio does not know. */
	kind: ServiceKind | null;
	/** Bare key ("web"), the dotted name of an unknown kind, or "environment". */
	name: string;
	/** The steps that do the work, in journal order; their parents only group them. */
	steps: Step[];
	status: StepStatus;
	/** The step a row names: the one in flight, else the one that failed, else the latest. */
	current: Step;
	/** When the work began: its first step's first observation. */
	started_at: string | null;
	/** Null while any of the work is unsettled. */
	finished_at: string | null;
}

const KINDS: Record<string, ServiceKind> = {
	applications: 'application',
	databases: 'database',
	buckets: 'bucket'
};

/**
 * A dotted service name's kind and bare key ("applications.web" is the
 * application "web"). A collection this Studio does not know keeps its
 * dotted name and no kind.
 */
export function splitService(service: string): { kind: ServiceKind | null; name: string } {
	const dot = service.indexOf('.');
	const kind = dot > 0 ? (KINDS[service.slice(0, dot)] ?? null) : null;
	return { kind, name: kind ? service.slice(dot + 1) : service };
}
const KIND_ORDER: (ServiceKind | null)[] = ['application', 'database', 'bucket', null];

// The status a row shows, most telling first: a failure anywhere, then
// work in flight. A row is skipped only when all of it was.
const STATUS_ORDER: StepStatus[] = [
	'failed',
	'running',
	'waiting',
	'pending',
	'cancelled',
	'succeeded',
	'skipped'
];

function unsettled(status: StepStatus): boolean {
	return status === 'pending' || status === 'waiting' || status === 'running';
}

/**
 * Groups a run's steps by service: services first (applications,
 * databases, buckets, each by key), the environment's own work last.
 * Returns null when no step names a service (a backup, or a run journaled
 * before servers recorded services): the step tree is then the only view.
 */
export function serviceProgress(steps: readonly Step[]): ServiceProgress[] | null {
	// Leaves do the work and their parents group it, so a parent stays out
	// of the rows, but one that names its service (an application's
	// artifacts) still counts toward the status: it runs while its
	// children are done and the next has not started.
	const groups = new Map<string | null, { leaves: Step[]; parents: Step[] }>();
	const group = (service: string | null) => {
		let entry = groups.get(service);
		if (!entry) groups.set(service, (entry = { leaves: [], parents: [] }));
		return entry;
	};
	let tagged = false;
	const walk = (list: readonly Step[], inherited: string | null) => {
		for (const step of list) {
			const service = step.service ?? inherited;
			if (service) tagged = true;
			if (step.children?.length) {
				if (step.service) group(service).parents.push(step);
				walk(step.children, service);
			} else {
				group(service).leaves.push(step);
			}
		}
	};
	walk(steps, null);
	if (!tagged) return null;

	const rows: ServiceProgress[] = [];
	for (const [service, { leaves, parents }] of groups) {
		// A service whose parent has no children yet has no rows of work.
		if (leaves.length === 0) continue;
		rows.push(summarize(service, leaves, [...leaves, ...parents]));
	}
	return rows.sort(
		(a, b) =>
			Number(a.service === null) - Number(b.service === null) ||
			KIND_ORDER.indexOf(a.kind) - KIND_ORDER.indexOf(b.kind) ||
			a.name.localeCompare(b.name)
	);
}

function summarize(service: string | null, steps: Step[], counted: Step[]): ServiceProgress {
	const status = STATUS_ORDER.find((s) => counted.some((step) => step.status === s)) ?? 'pending';
	const current =
		steps.find((s) => s.status === 'running') ??
		steps.find((s) => s.status === 'waiting') ??
		steps.find((s) => s.status === 'pending') ??
		steps.find((s) => s.status === 'failed') ??
		steps[steps.length - 1];
	// The work starts at its first observation: a step the reconciler saw
	// waiting (a claim provisioning, a release running) is stamped started
	// only when it completes, but it was created when the wait began.
	const started = counted
		.filter((s) => s.status !== 'pending')
		.map((s) => s.created_at)
		.sort();
	const finished = counted
		.map((s) => s.finished_at)
		.filter((t): t is string => t !== null)
		.sort();
	const { kind, name } = service ? splitService(service) : { kind: null, name: 'environment' };
	return {
		service,
		kind,
		name,
		steps,
		status,
		current,
		started_at: started[0] ?? null,
		finished_at: counted.some((s) => unsettled(s.status))
			? null
			: (finished[finished.length - 1] ?? null)
	};
}
