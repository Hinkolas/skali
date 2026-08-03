// Shapes mirror the skali API (api/openapi.yaml), snake_case included.

import type { Observation } from './status';

export type NodeRole = 'server' | 'agent';

/** One observed cluster node from GET /v1/nodes. Kube-observed facts only. */
export interface ClusterNode {
	name: string;
	role: NodeRole;
	capabilities: string[];
	arch?: string;
	os?: string;
	kubelet_version?: string;
	ready: boolean;
	schedulable: boolean;
	internal_ip?: string;
	external_ip?: string;
	last_heartbeat: string | null;
}

export interface NodesResponse {
	nodes: ClusterNode[];
	observation: Observation;
}
