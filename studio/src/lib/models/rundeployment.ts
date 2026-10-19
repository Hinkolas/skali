// What a rollout changed, read from the document the server records on
// deployment, rollback, and restart runs (run.deployment): one line per
// run for the environment's history, and what each run did to one service
// for that service's own history.

import { shortId } from '$lib/format';
import type { Run } from '$lib/types/runs';
import { splitService } from './runservices';

// Service changes read in this order, the everyday ones first.
const ACTIONS = ['update', 'create', 'remove'];
const ACTION_WORDS: Record<string, string> = {
	create: 'created',
	update: 'updated',
	remove: 'removed',
	restart: 'restarted'
};

/** The past-tense word for a plan action ("updated"); unknown actions read as they come. */
export function actionWord(action: string): string {
	return ACTION_WORDS[action] ?? action;
}

// Names past three collapse into a count, so a line stays one line.
function names(services: string[]): string {
	const bare = services.map((s) => splitService(s).name);
	if (bare.length <= 3) return bare.join(', ');
	return `${bare.slice(0, 2).join(', ')} and ${bare.length - 2} more`;
}

/**
 * One line saying what a run changed, e.g. "from staging · web, worker
 * updated · main created · 2 values changed". A rollback names the
 * revision it returns to. Null for runs that record nothing (backups,
 * stops, and rollouts from before servers recorded changes).
 */
export function deploymentSummary(run: Run): string | null {
	const deployment = run.deployment;
	if (!deployment) return null;
	const parts: string[] = [];
	if (deployment.from_environment) parts.push(`from ${deployment.from_environment}`);
	if (run.kind === 'rollback' && deployment.revision) {
		parts.push(`to ${shortId(deployment.revision)}`);
	}
	const changes = deployment.changes ?? [];
	const actions = [...ACTIONS, ...changes.map((c) => c.action).filter((a) => !ACTIONS.includes(a))];
	for (const action of new Set(actions)) {
		const services = changes.filter((c) => c.action === action).map((c) => c.service);
		if (services.length > 0) parts.push(`${names(services)} ${actionWord(action)}`);
	}
	// A value dropped from the definition and pruned from the store is two
	// rows of one value.
	const values = new Set(deployment.values?.map((v) => v.name)).size;
	if (values > 0) parts.push(`${values} value${values === 1 ? '' : 's'} changed`);
	const restarts = deployment.restarts ?? [];
	if (restarts.length > 0) parts.push(`${names(restarts)} restarted`);
	if (changes.length === 0 && values === 0 && restarts.length === 0) parts.push('no changes');
	return parts.join(' · ');
}

export interface ServiceChange {
	run: Run;
	/** create, update, remove, or restart. */
	action: string;
	detail?: string;
	destructive?: boolean;
}

/**
 * What one run did to one service (by dotted name): its plan change, new
 * versions of values it uses (which roll its pods though its own plan row
 * may say nothing), a restart, or several of these; null when the run
 * left the service alone.
 */
export function serviceChange(run: Run, service: string): ServiceChange | null {
	const deployment = run.deployment;
	if (!deployment) return null;
	const change = deployment.changes?.find((c) => c.service === service);
	const restarted = deployment.restarts?.includes(service) ?? false;
	// A service created or removed with its values says so already.
	const values =
		!change || change.action === 'update'
			? [
					...new Set(
						(deployment.values ?? [])
							.filter((v) => v.services?.includes(service))
							.map((v) => v.name)
					)
				]
			: [];
	const valuesNote =
		values.length > 0
			? `value${values.length === 1 ? '' : 's'} ${values.join(', ')} changed`
			: null;
	const action = change?.action ?? (valuesNote ? 'update' : restarted ? 'restart' : null);
	if (!action) return null;
	const detail = [
		change?.detail,
		valuesNote,
		restarted && action !== 'restart' ? 'restarted' : null
	]
		.filter(Boolean)
		.join('; ');
	return {
		run,
		action,
		detail: detail || undefined,
		destructive: change?.destructive
	};
}

/** The runs that changed one service, in the order given (the API's newest first). */
export function serviceChanges(runs: readonly Run[], service: string): ServiceChange[] {
	const found: ServiceChange[] = [];
	for (const run of runs) {
		const change = serviceChange(run, service);
		if (change) found.push(change);
	}
	return found;
}
