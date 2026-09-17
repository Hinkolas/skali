// Shapes mirror the skali API (api/openapi.yaml), snake_case included.

import type { MetricsWindow } from '$lib/types/metrics';
import { formatBytes } from '$lib/format';

export type DatabasePoolClass = 'shared' | 'environment' | 'dedicated';

/** A pool's memory budget; bytes is null until a database node is observed. */
export interface DatabasePoolMemory {
	bytes: number | null;
	auto: boolean;
	node?: string;
	capped?: boolean;
}

export interface DatabasePoolParameters {
	/** The postgresql.conf values the pool runs, PostgreSQL units (512MB). */
	effective: Record<string, string>;
	overrides: Record<string, string>;
	/** Tunables whose change restarts the instances. */
	restart_keys: string[];
	/** Every parameter an override may name. */
	allowed: string[];
}

export interface DatabasePoolObserved {
	phase: string;
	instances: number;
	ready_instances: number;
	primary?: string;
}

/** One managed PostgreSQL pool from GET /v1/system/database-pools. */
export interface DatabasePool {
	name: string;
	class: DatabasePoolClass;
	engine: string;
	major: number;
	instances: number;
	storage_bytes: number;
	image: string;
	node_port: number | null;
	state: string;
	memory: DatabasePoolMemory;
	parameters: DatabasePoolParameters;
	observed: DatabasePoolObserved | null;
	created_at: string;
	updated_at: string;
}

export interface DatabasePoolsResponse {
	pools: DatabasePool[];
}

/** One instance pod of a pool as the cluster reports it. */
export interface DatabasePoolMember {
	name: string;
	/** primary or replica; empty while the operator has not labeled the pod. */
	role: 'primary' | 'replica' | '';
	node?: string;
	ready: boolean;
	restarts: number;
	started_at: string | null;
	phase: string;
}

/** One logical database on a pool; project and environment are null for
 * the platform's own (system-owned) databases. */
export interface DatabasePoolDatabase {
	database_name: string;
	role_name: string;
	owner: 'service' | 'system';
	system_key?: string;
	service_key?: string;
	project: { id: string; name: string; display_name: string } | null;
	environment: { id: string; name: string } | null;
	phase: string;
	storage_bytes: number;
	used_bytes: number | null;
	created_at: string;
}

/** GET /v1/system/database-pools/{name} */
export interface DatabasePoolDetail extends DatabasePool {
	members: DatabasePoolMember[];
	databases: DatabasePoolDatabase[];
}

export interface DatabasePoolDetailResponse {
	pool: DatabasePoolDetail;
}

/** The newest sample with the denominators the tiles draw against. */
export interface PoolMetricsCurrent {
	sampled_at: string;
	cpu_millicores: number;
	memory_bytes: number;
	instances: number;
	instances_ready: number;
	connections: number | null;
	database_bytes: number | null;
	memory_budget_bytes: number | null;
	max_connections: number | null;
}

/** GET /v1/system/database-pools/{name}/metrics. CPU and memory sum the
 * instances; the rest is the primary's exporter view. commits, rollbacks,
 * blks_hit and blks_read are per-bucket counts. */
export interface PoolMetrics {
	pool: string;
	window: MetricsWindow;
	step_seconds: number;
	timestamps: string[];
	cpu_millicores: (number | null)[];
	memory_bytes: (number | null)[];
	connections: (number | null)[];
	commits: (number | null)[];
	rollbacks: (number | null)[];
	blks_hit: (number | null)[];
	blks_read: (number | null)[];
	cache_hit_ratio: (number | null)[];
	database_bytes: (number | null)[];
	instances_ready: (number | null)[];
	current: PoolMetricsCurrent | null;
}

const MIB = 1024 * 1024;
const GIB = 1024 * MIB;

/** The smallest budget the API accepts, mirrored from pgtune. */
export const MIN_POOL_MEMORY_BYTES = 256 * MIB;

/**
 * Parses a budget typed in GiB (decimals allowed) into whole MiB of bytes;
 * null for anything that is not a positive number.
 */
export function parseGiB(input: string): number | null {
	const trimmed = input.trim();
	if (!/^\d+(\.\d+)?$/.test(trimmed)) return null;
	const gib = Number(trimmed);
	if (!Number.isFinite(gib) || gib <= 0) return null;
	return Math.round(gib * 1024) * MIB;
}

/** Renders bytes as a GiB number for an input, without a unit. */
export function formatGiB(bytes: number): string {
	const gib = bytes / GIB;
	return Number.isInteger(gib) ? String(gib) : gib.toFixed(2).replace(/\.?0+$/, '');
}

/** The pill text and tone for a pool's observed state. */
export function poolPhase(pool: DatabasePool): {
	text: string;
	tone: 'success' | 'warning' | 'neutral';
} {
	const observed = pool.observed;
	if (!observed) return { text: 'not observed', tone: 'neutral' };
	const ready = `${observed.ready_instances}/${observed.instances} ready`;
	if (
		observed.phase === 'Cluster in healthy state' &&
		observed.ready_instances === observed.instances
	) {
		return { text: `healthy · ${ready}`, tone: 'success' };
	}
	return { text: `${observed.phase.toLowerCase()} · ${ready}`, tone: 'warning' };
}

/** Whether two override maps name the same keys with the same values. */
export function overridesEqual(a: Record<string, string>, b: Record<string, string>): boolean {
	const keys = new Set([...Object.keys(a), ...Object.keys(b)]);
	for (const key of keys) if (a[key] !== b[key]) return false;
	return true;
}

/**
 * The restart-requiring parameters a save would change: a budget change
 * moves shared_buffers, and any restart key whose override differs.
 */
export function restartRequired(
	pool: DatabasePool,
	memoryChanged: boolean,
	overrides: Record<string, string>
): string[] {
	const keys = new Set<string>();
	if (memoryChanged) keys.add('shared_buffers');
	for (const key of pool.parameters.restart_keys) {
		if ((pool.parameters.overrides[key] ?? '') !== (overrides[key] ?? '')) keys.add(key);
	}
	return [...keys].sort();
}

export const POOL_CLASS_TEXT: Record<DatabasePoolClass, string> = {
	shared: "shared · every project's databases",
	environment: 'environment · one pool per environment',
	dedicated: 'dedicated · one database'
};

/** The header line under a pool's name. */
export function poolSubtitle(pool: DatabasePool): string {
	const instances = pool.instances === 1 ? '1 instance' : `${pool.instances} instances`;
	return `${pool.class} · ${pool.engine} ${pool.major} · ${instances} · ${formatBytes(pool.storage_bytes)}`;
}

/** The budget as one fact: its size and where it came from. */
export function poolBudgetText(pool: DatabasePool): string {
	const memory = pool.memory;
	if (memory.bytes === null) return 'unknown · no database node observed yet';
	if (!memory.auto) return `${formatBytes(memory.bytes)} · custom`;
	const from = memory.node ? ` from ${memory.node}` : '';
	const capped = memory.capped ? ', capped' : '';
	return `${formatBytes(memory.bytes)} · automatic${from}${capped}`;
}

/** How many primaries and replicas a pool runs right now. */
export function memberSummary(members: DatabasePoolMember[]): string {
	if (members.length === 0) return 'no instances observed';
	const primaries = members.filter((m) => m.role === 'primary').length;
	const replicas = members.filter((m) => m.role === 'replica').length;
	const parts: string[] = [];
	if (primaries > 0) parts.push(primaries === 1 ? '1 primary' : `${primaries} primaries`);
	if (replicas > 0) parts.push(replicas === 1 ? '1 replica' : `${replicas} replicas`);
	const unlabeled = members.length - primaries - replicas;
	if (unlabeled > 0) parts.push(`${unlabeled} starting`);
	return parts.join(' · ');
}

/** A claim phase as a pill. */
export function databasePhase(phase: string): {
	text: string;
	tone: 'success' | 'warning' | 'neutral';
} {
	if (phase === 'provisioned') return { text: phase, tone: 'success' };
	if (phase === 'releasing' || phase === 'released') return { text: phase, tone: 'warning' };
	return { text: phase, tone: 'neutral' };
}

export type ParameterGroup = 'memory' | 'connections' | 'planner' | 'wal' | 'workers' | 'other';
export type ParameterUnit = 'memory' | 'integer' | 'decimal';

export const PARAMETER_GROUPS: { id: ParameterGroup; label: string }[] = [
	{ id: 'memory', label: 'Memory' },
	{ id: 'connections', label: 'Connections' },
	{ id: 'planner', label: 'Planner' },
	{ id: 'wal', label: 'WAL and checkpoints' },
	{ id: 'workers', label: 'Workers' },
	{ id: 'other', label: 'Other' }
];

export const UNIT_HINT: Record<ParameterUnit, string> = {
	memory: 'PostgreSQL memory syntax: 64MB, 2GB',
	integer: 'whole number',
	decimal: 'decimal number, for example 0.9'
};

/**
 * What each tunable does, in one line. Mirrors pgtune.Allowed in the
 * backend; a key the API allows but this map does not know lands in the
 * Other group without a blurb.
 */
export const PARAMETER_META: Record<
	string,
	{ group: ParameterGroup; blurb: string; unit: ParameterUnit }
> = {
	shared_buffers: {
		group: 'memory',
		unit: 'memory',
		blurb: 'Buffer cache PostgreSQL manages itself; a quarter of the budget by default.'
	},
	effective_cache_size: {
		group: 'memory',
		unit: 'memory',
		blurb:
			"The planner's estimate of memory available for caching, OS cache included; allocates nothing."
	},
	work_mem: {
		group: 'memory',
		unit: 'memory',
		blurb: 'Per sort or hash operation, per backend; one query can use several at once.'
	},
	maintenance_work_mem: {
		group: 'memory',
		unit: 'memory',
		blurb: 'For VACUUM, CREATE INDEX and ALTER TABLE ADD FOREIGN KEY.'
	},
	wal_buffers: {
		group: 'memory',
		unit: 'memory',
		blurb: 'Shared memory for WAL that is not yet flushed to disk.'
	},
	max_connections: {
		group: 'connections',
		unit: 'integer',
		blurb: 'Concurrent client connections; work_mem is re-derived from it.'
	},
	random_page_cost: {
		group: 'planner',
		unit: 'decimal',
		blurb: 'Cost of a non-sequential page fetch relative to a sequential one; 1.1 suits SSDs.'
	},
	effective_io_concurrency: {
		group: 'planner',
		unit: 'integer',
		blurb: 'Concurrent I/O requests the storage is expected to handle.'
	},
	default_statistics_target: {
		group: 'planner',
		unit: 'integer',
		blurb: 'Rows ANALYZE samples per column; raise it for skewed data.'
	},
	max_wal_size: {
		group: 'wal',
		unit: 'memory',
		blurb: 'Soft cap on WAL between automatic checkpoints.'
	},
	min_wal_size: {
		group: 'wal',
		unit: 'memory',
		blurb: 'WAL kept and recycled rather than removed after a checkpoint.'
	},
	wal_keep_size: {
		group: 'wal',
		unit: 'memory',
		blurb: 'WAL kept for replicas that fall behind.'
	},
	checkpoint_completion_target: {
		group: 'wal',
		unit: 'decimal',
		blurb: 'Fraction of the checkpoint interval over which writes are spread.'
	},
	max_worker_processes: {
		group: 'workers',
		unit: 'integer',
		blurb: 'Background workers the server may start; bounds the parallel limits below.'
	},
	max_parallel_workers: {
		group: 'workers',
		unit: 'integer',
		blurb: 'Parallel workers across all queries at once.'
	},
	max_parallel_workers_per_gather: {
		group: 'workers',
		unit: 'integer',
		blurb: 'Parallel workers one query node may use.'
	}
};

export interface ParameterRow {
	key: string;
	effective: string;
	restart: boolean;
	blurb: string;
	unit: ParameterUnit;
}

/**
 * The allowed parameters grouped for the tuning form, in PARAMETER_META
 * order within each group; groups without an allowed key are omitted.
 */
export function groupedParameters(
	pool: DatabasePool
): { id: ParameterGroup; label: string; rows: ParameterRow[] }[] {
	const allowed = new Set(pool.parameters.allowed);
	const rowFor = (key: string): ParameterRow => {
		const meta = PARAMETER_META[key];
		return {
			key,
			effective: pool.parameters.effective[key] ?? '',
			restart: pool.parameters.restart_keys.includes(key),
			blurb: meta?.blurb ?? '',
			unit: meta?.unit ?? 'integer'
		};
	};
	const known = Object.keys(PARAMETER_META).filter((key) => allowed.has(key));
	const unknown = [...allowed].filter((key) => !(key in PARAMETER_META)).sort();
	return PARAMETER_GROUPS.map((group) => ({
		...group,
		rows: [
			...known.filter((key) => PARAMETER_META[key].group === group.id),
			...(group.id === 'other' ? unknown : [])
		].map(rowFor)
	})).filter((group) => group.rows.length > 0);
}
