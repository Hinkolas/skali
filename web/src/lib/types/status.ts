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
	/** Blue-green color of the pod's Deployment; absent on rolling and recreate workloads. */
	color?: string;
	/** Whether the application's Service currently selects this pod. */
	serving: boolean;
}

export type CertificateState = 'pending' | 'issuing' | 'active' | 'failing' | 'expired';

export interface CertificateStatus {
	failed_attempts: number;
	last_failure_time: string | null;
	next_retry_time: string | null;
	next_private_key_secret_name?: string;
	name: string;
	secret_name: string;
	state: CertificateState;
	reason?: string;
	message?: string;
	not_after: string | null;
	renewal_time: string | null;
}

export type EdgeState = 'reachable' | 'partial' | 'unreachable' | 'unresolved' | 'unknown';

// The reconciler's last probe of whether the route's domain reaches this
// installation; absent where no certificate is expected or before the
// first probe.
export interface EdgeStatus {
	state: EdgeState;
	message?: string;
	checked_at: string | null;
	addresses: string[];
	// The reconciler's conclusion: TLS waits for the domain to point here
	// and nothing usable is on hand (a certificate for the route's previous
	// domain keeps serving but does not count).
	deferred: boolean;
}

// One manual edge probe of a route domain (POST .../routes/probe): the
// verdict with its per-address detail. The reconciler's own conclusion
// follows in the status stream once its pass acted on it.
export interface RouteProbe {
	service: string;
	key: string;
	domain: string;
	edge: {
		state: EdgeState;
		message?: string;
		checked_at: string;
		addresses: { address: string; outcome: 'ours' | 'foreign' | 'unreachable'; detail?: string }[];
	};
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
	edge?: EdgeStatus;
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
