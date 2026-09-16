// Shapes mirror the skali API (api/openapi.yaml), snake_case included.

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
