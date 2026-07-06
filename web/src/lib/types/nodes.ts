// Shapes mirror the skali API (api/openapi.yaml) — snake_case preserved.

export type NodeRole = 'master' | 'edge' | 'worker' | 'builder';

export type NodeStatus = 'online' | 'offline';

export interface Node {
	id: string;
	name: string;
	roles: NodeRole[];
	/** host:port the master dials on the private network. */
	advertise_addr: string;
	/** Where public DNS points (edge role); operator-set. */
	public_addr: string | null;
	arch: string | null;
	os: string | null;
	skalid_version: string | null;
	status: NodeStatus;
	last_seen: string | null;
	/** Latest resource snapshot; null until the node first reports. */
	metrics: NodeMetrics | null;
	created_at: string;
	updated_at: string;
}

/**
 * A node resource snapshot. Rates are bytes/second; cpu_pct is 0–100 across
 * effective cores; load1 is the host-wide 1-minute load average (advisory).
 */
export interface NodeMetrics {
	cpu_pct: number;
	mem_used: number;
	mem_total: number;
	disk_used: number;
	disk_total: number;
	net_rx_rate: number;
	net_tx_rate: number;
	disk_read_rate: number;
	disk_write_rate: number;
	load1: number;
}

/** One bucketed history point from GET /v1/nodes/{id}/metrics (2min averages). */
export interface NodeMetricsSample extends NodeMetrics {
	sampled_at: string;
}

export interface NodeMetricsHistory {
	samples: NodeMetricsSample[];
}

/** POST /v1/nodes/tokens response. The token is shown exactly once. */
export interface JoinTokenCreated {
	token: string;
	expires_at: string;
	enroll_command: string;
}

/** Roles an operator can grant — `master` is fixed at boot. */
export const ASSIGNABLE_NODE_ROLES: NodeRole[] = ['worker', 'edge', 'builder'];
