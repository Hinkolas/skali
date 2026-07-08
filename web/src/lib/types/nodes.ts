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
	/** At-a-glance engine counts; present on the list endpoint only. */
	engine?: NodeEngineCounts;
	created_at: string;
	updated_at: string;
}

/** Engine counts per node; containers exclude `gone` breadcrumbs. */
export interface NodeEngineCounts {
	containers: number;
	images: number;
	volumes: number;
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

/**
 * How skali manages a container: `application` instances belong to a project;
 * a `database` container is an engine pool that may host logical databases of
 * many projects; `system` containers are skali's own infrastructure.
 */
export type ContainerKind = 'application' | 'database' | 'system';

/** Engine states plus `gone` (vanished from the node; kept briefly). */
export type ContainerState =
	| 'created'
	| 'running'
	| 'paused'
	| 'restarting'
	| 'removing'
	| 'exited'
	| 'dead'
	| 'gone';

/**
 * Per-container stats. cpu_pct uses docker-stats semantics (100 = one full
 * core, can exceed 100); rates are bytes/second; mem_limit is the host total
 * when the container is unlimited.
 */
export interface NodeContainerStats {
	cpu_pct: number;
	mem_used: number;
	mem_limit: number;
	net_rx_rate: number;
	net_tx_rate: number;
}

/** One observed skali-managed container on a node. */
export interface NodeContainer {
	/** Engine container id. */
	id: string;
	node_id: string;
	node_name: string;
	name: string;
	image: string;
	kind: ContainerKind;
	state: ContainerState;
	/** Healthcheck state; null when none is configured. */
	health: 'starting' | 'healthy' | 'unhealthy' | null;
	exit_code: number | null;
	restart_count: number;
	labels: Record<string, string>;
	/** Latest stats; null until the node has sampled twice. */
	stats: NodeContainerStats | null;
	created_at: string | null;
	started_at: string | null;
	first_seen: string;
	last_seen: string;
}

export interface NodeContainerList {
	containers: NodeContainer[];
}

/**
 * One image observed on a node. Unfiltered inventory — every image on the
 * machine, skali-managed or not; `containers` counts references from
 * containers in any state (0 = unused).
 */
export interface NodeImage {
	/** Content-addressable image id (`sha256:…`). */
	id: string;
	node_id: string;
	node_name: string;
	/** Empty for dangling images. */
	repo_tags: string[];
	repo_digests: string[];
	size_bytes: number;
	dangling: boolean;
	containers: number;
	/** The image's own build time; null when unknown. */
	created_at: string | null;
	first_seen: string;
	last_seen: string;
}

export interface NodeImageList {
	images: NodeImage[];
}

/**
 * One named volume observed on a node (anonymous volumes included). No size —
 * computing it walks the volume's filesystem.
 */
export interface NodeVolume {
	name: string;
	node_id: string;
	node_name: string;
	driver: string;
	scope: 'local' | 'global';
	/** Node-local storage path. */
	mountpoint: string;
	labels: Record<string, string>;
	containers: number;
	created_at: string | null;
	first_seen: string;
	last_seen: string;
}

export interface NodeVolumeList {
	volumes: NodeVolume[];
}

/** Roles an operator can grant — `master` is fixed at boot. */
export const ASSIGNABLE_NODE_ROLES: NodeRole[] = ['worker', 'edge', 'builder'];
