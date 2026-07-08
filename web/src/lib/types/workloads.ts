// Workload payloads (GET /v1/workloads): desired state plus the live
// convergence status the reconciler reports back through assignments.

export type WorkloadKind = 'application' | 'database';
export type WorkloadDesiredState = 'running' | 'stopped' | 'deleting';
export type WorkloadStatus =
	| 'running'
	| 'stopped'
	| 'converging'
	| 'importing'
	| 'degraded'
	| 'deleting';
export type AssignmentPhase =
	| 'pending'
	| 'unschedulable'
	| 'pulling'
	| 'deploying'
	| 'stopping'
	| 'ready'
	| 'stopped'
	| 'removing';

export interface WorkloadConstraints {
	/** Pin to these nodes; empty = any. Placement stays reconciler-owned. */
	node_ids?: string[];
	node_roles?: string[];
}

export interface WorkloadSpec {
	env?: Record<string, string>;
	command?: string[];
	ports?: { host_ip?: string; host_port?: number; container_port: number; protocol?: string }[];
	mounts?: { type: 'bind' | 'volume'; source: string; target: string; read_only?: boolean }[];
	restart_policy?: string;
	restart_max_retries?: number;
	/** Cores per replica (0.5 = half a core); 0 = unlimited. */
	cpus?: number;
	/** Bytes; 0 = unlimited. */
	memory_limit?: number;
	networks?: string[];
	labels?: Record<string, string>;
	healthcheck?: {
		test: string[];
		interval_seconds?: number;
		timeout_seconds?: number;
		start_period_seconds?: number;
		retries?: number;
	};
}

/** One replica slot, placed and converged by the reconciler. */
export interface WorkloadAssignment {
	ordinal: number;
	node_id?: string;
	node_name?: string;
	phase: AssignmentPhase;
	container_name: string;
	container_id?: string;
	/** Workload generation this slot last converged to. */
	generation: number;
	retries: number;
	next_attempt_at?: string;
	last_error?: string;
	updated_at: string;
}

export interface Workload {
	id: string;
	name: string;
	kind: WorkloadKind;
	desired_state: WorkloadDesiredState;
	replicas: number;
	constraints: WorkloadConstraints;
	/** Upstream tag reference, e.g. "postgres:17". */
	image: string;
	spec: WorkloadSpec;
	generation: number;
	status: WorkloadStatus;
	/** The mirror pin the replicas actually run. */
	image_repository?: string;
	image_digest?: string;
	last_error?: string;
	next_attempt_at?: string;
	created_at: string;
	updated_at: string;
	assignments: WorkloadAssignment[];
}
