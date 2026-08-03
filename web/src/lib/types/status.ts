// Shapes mirror the skali API (api/openapi.yaml), snake_case included.

import type { EnvironmentState, ServiceHealth } from './project';

export interface Observation {
	state: 'fresh' | 'stale' | 'unknown';
	stale_since: string | null;
	last_sync: string | null;
}

export interface RevisionRef {
	id: string;
	checksum: string;
}

export interface HealthDiagnostic {
	severity: 'info' | 'warning' | 'error';
	code: string;
	message: string;
	resource?: string;
}

export interface PodStatus {
	name: string;
	node: string;
	phase: string;
	ready: boolean;
	restarts: number;
	reason?: string;
	started_at: string | null;
}

export interface ServiceStatus {
	key: string;
	type: string;
	health: ServiceHealth;
	diagnostics: HealthDiagnostic[];
	pods: PodStatus[];
}

export interface EnvironmentStatus {
	environment_id: string;
	state: EnvironmentState;
	target_revision: RevisionRef | null;
	active_revision: RevisionRef | null;
	observation: Observation;
	platforms: string[];
	services: ServiceStatus[];
}
