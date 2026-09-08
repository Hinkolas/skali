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
	name: string;
	role: 'server' | 'agent';
	k3s_version?: string;
	agent_version?: string;
	phase: string;
	last_seen?: string;
}

export type UpdateStepPhase = 'pending' | 'running' | 'complete' | 'failed';

export interface UpdateStep {
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
