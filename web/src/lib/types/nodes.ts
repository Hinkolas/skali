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
	created_at: string;
	updated_at: string;
}

/** POST /v1/nodes/tokens response. The token is shown exactly once. */
export interface JoinTokenCreated {
	token: string;
	expires_at: string;
	enroll_command: string;
}

/** Roles an operator can grant — `master` is fixed at boot. */
export const ASSIGNABLE_NODE_ROLES: NodeRole[] = ['worker', 'edge', 'builder'];
