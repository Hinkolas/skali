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

export type CertificateState = 'pending' | 'issuing' | 'active' | 'failing' | 'expired';

export interface CertificateStatus {
	name: string;
	secret_name: string;
	state: CertificateState;
	reason?: string;
	message?: string;
	not_after: string | null;
	renewal_time: string | null;
}

// One public route with its edge policies; certificate is absent where none
// exists by design (tls disabled, local installation).
export interface RouteStatus {
	key: string;
	domain: string;
	path: string;
	tls: 'automatic' | 'optional' | 'disabled';
	strategy: 'round-robin' | 'least-requests';
	certificate?: CertificateStatus | null;
}

export interface ServiceStatus {
	key: string;
	type: string;
	health: ServiceHealth;
	diagnostics: HealthDiagnostic[];
	pods: PodStatus[];
	routes?: RouteStatus[];
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
