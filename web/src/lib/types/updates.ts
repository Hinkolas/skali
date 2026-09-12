// Shapes mirror the skali API (api/openapi.yaml), snake_case included.

export type UpdateChannel = 'stable' | 'beta';

/** Why the last release scan failed; the page words its notice by kind. */
export type FeedErrorKind = 'offline' | 'not_found' | 'rate_limited' | 'unavailable' | 'invalid';

export const FEED_ERROR_TITLE: Record<FeedErrorKind, string> = {
	offline: 'Update servers are offline',
	not_found: 'Release feed not found',
	rate_limited: 'Release feed is rate limiting this installation',
	unavailable: 'Update servers are unavailable',
	invalid: 'Release feed returned an unexpected answer'
};

export const FEED_ERROR_HINT: Record<FeedErrorKind, string> = {
	offline:
		'skalid could not reach the release feed. GitHub may be down, or this installation has no outbound HTTPS. It retries every hour.',
	not_found:
		'The release feed answered 404. The repository may be private or renamed, or the configured feed URL is wrong. It retries every hour.',
	rate_limited: 'The feed refused the request for now. The next scheduled check usually succeeds.',
	unavailable: 'The release feed answered with a server error. It retries every hour.',
	invalid: 'The feed answered, but not with a release listing. Check the configured feed URL.'
};

/** The short pill text for a failed scan, by kind. */
export const FEED_ERROR_PILL: Record<FeedErrorKind, string> = {
	offline: 'update servers offline',
	not_found: 'release feed not found',
	rate_limited: 'feed rate limited',
	unavailable: 'update servers unavailable',
	invalid: 'check failed'
};

export interface Release {
	version: string;
	prerelease: boolean;
	published_at: string;
	url?: string;
	k3s?: string;
}

export interface UpdateNode {
	id: string;
	name: string;
	role: 'server' | 'agent';
	k3s_version?: string;
	agent_version?: string;
	phase: string;
	last_seen?: string;
	coordinator_version?: string;
	coordinator_last_seen?: string;
}

export type UpdateStepPhase = 'pending' | 'running' | 'complete' | 'failed';

export interface UpdateStep {
	node_id: string;
	node: string;
	action: string;
	phase: UpdateStepPhase;
	error?: string;
}

export interface UpdateOperation {
	id: string;
	phase: string;
	target_version?: string;
	from_version?: string;
	started_at: string;
	updated_at: string;
	completed_at?: string;
	error?: string;
	steps: UpdateStep[];
}

/** GET /v1/system/updates */
export interface UpdateStatus {
	summary: {
		state:
			| 'unknown'
			| 'not_checked'
			| 'no_release'
			| 'current'
			| 'available'
			| 'incomplete'
			| 'updating'
			| 'failed';
		converged_version?: string;
		target_version?: string;
		action: '' | 'update' | 'finish' | 'retry';
		detail?: string;
		progress: { done: number; total: number; percent: number; phase: string };
	};
	installed: { version: string; platform_version?: string };
	channel: UpdateChannel;
	auto_update: boolean;
	last_checked_at: string | null;
	last_error?: string;
	last_error_kind?: FeedErrorKind;
	latest: Release | null;
	update_available: boolean;
	managed: boolean;
	manageable: boolean;
	reason?: string;
	nodes: UpdateNode[];
	operation: UpdateOperation | null;
	last_successful: UpdateOperation | null;
}

export function updateSummary(status: UpdateStatus): UpdateStatus['summary'] {
	// A cached console can reach an older daemon during a rolling rollout.
	return (
		status.summary ?? {
			state: 'unknown',
			action: '',
			detail:
				'This platform has not reported cluster-wide update status yet. Reload after the platform rollout completes.',
			progress: { done: 0, total: 0, percent: 0, phase: 'Preparing' }
		}
	);
}

export function updatePresentation(status: UpdateStatus) {
	const summary = updateSummary(status);
	const target = summary?.target_version ?? '';
	const labels = {
		current: 'You are up to date',
		available: `${target} is available`,
		incomplete: 'Update incomplete',
		updating: `Updating to ${target}`,
		failed: 'Update needs attention',
		unknown: 'Update status unavailable',
		not_checked: 'Not checked yet',
		no_release: 'No releases on this channel'
	};
	return {
		title: summary ? labels[summary.state] : labels.unknown,
		action:
			summary?.action === 'finish'
				? 'Finish update'
				: summary?.action === 'retry'
					? 'Retry'
					: 'Update now',
		tone: (summary?.state === 'current'
			? 'success'
			: ['available', 'incomplete', 'updating', 'failed'].includes(summary?.state)
				? 'warning'
				: 'neutral') as 'success' | 'warning' | 'neutral'
	};
}

export function operationSettled(operation: UpdateOperation | null): boolean {
	return !operation || operation.phase === 'complete' || operation.phase === 'failed';
}

/** Operator-facing names for the coordinator's operation phases. */
export const OPERATION_PHASE_LABEL: Record<string, string> = {
	pending: 'Preparing',
	'upgrading-nodes': 'Upgrading nodes',
	'adding-nodes': 'Adding nodes',
	'activating-topology': 'Checking nodes',
	'awaiting-platform-initialization': 'Waiting for initialization',
	'reconciling-platform': 'Moving the platform',
	'removing-nodes': 'Removing nodes',
	'rebalancing-workloads': 'Rebalancing workloads',
	verifying: 'Verifying',
	complete: 'Complete',
	failed: 'Failed'
};

/** The version most nodes report, or undefined when none reported one. */
export function commonVersion(values: (string | undefined)[]): string | undefined {
	const counts = new Map<string, number>();
	for (const v of values) if (v) counts.set(v, (counts.get(v) ?? 0) + 1);
	return [...counts.entries()].toSorted((a, b) => b[1] - a[1])[0]?.[0];
}

/**
 * One line for a component across nodes: the version when they agree,
 * "n on vA · m on vB" when they do not, plus how many never reported.
 */
export function tallyVersions(values: (string | undefined)[]): string {
	const counts = new Map<string, number>();
	let missing = 0;
	for (const v of values) {
		if (v) counts.set(v, (counts.get(v) ?? 0) + 1);
		else missing++;
	}
	if (counts.size === 0) return 'not reported';
	const parts = [...counts.entries()]
		.toSorted((a, b) => b[1] - a[1])
		.map(([v, n]) => (counts.size === 1 ? v : `${n} on ${v}`));
	if (missing > 0) parts.push(`${missing} not reported`);
	return parts.join(' · ');
}
