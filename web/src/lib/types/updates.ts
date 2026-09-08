// Shapes mirror the skali API (api/openapi.yaml), snake_case included.

export type UpdateChannel = 'stable' | 'beta';

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
