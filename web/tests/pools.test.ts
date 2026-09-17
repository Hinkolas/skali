import { expect, test } from 'vitest';
import {
	formatGiB,
	overridesEqual,
	parseGiB,
	poolPhase,
	restartRequired,
	type DatabasePool
} from '../src/lib/types/pools';

const MIB = 1024 * 1024;

function pool(overrides: Partial<DatabasePool> = {}): DatabasePool {
	return {
		name: 'pg17-shared',
		class: 'shared',
		engine: 'postgres',
		major: 17,
		instances: 2,
		storage_bytes: 100 * 1024 * MIB,
		state: 'active',
		memory: { bytes: 3328 * MIB, auto: true, node: 'db-1' },
		parameters: {
			effective: { shared_buffers: '832MB', work_mem: '8MB' },
			overrides: {},
			restart_keys: ['max_connections', 'max_worker_processes', 'shared_buffers', 'wal_buffers'],
			allowed: ['shared_buffers', 'work_mem', 'max_connections']
		},
		observed: null,
		created_at: '2026-09-16T00:00:00Z',
		updated_at: '2026-09-16T00:00:00Z',
		...overrides
	};
}

test('GiB input parses to whole MiB and formats back without trailing zeros', () => {
	expect(parseGiB('4')).toBe(4096 * MIB);
	expect(parseGiB(' 3.25 ')).toBe(3328 * MIB);
	expect(parseGiB('0.001')).toBe(1 * MIB);
	for (const bad of ['', '0', '-1', '4Gi', '1e3', 'abc']) expect(parseGiB(bad)).toBeNull();

	expect(formatGiB(4096 * MIB)).toBe('4');
	expect(formatGiB(3328 * MIB)).toBe('3.25');
	expect(formatGiB(1536 * MIB)).toBe('1.5');
});

test('observed phase renders as a pill', () => {
	expect(poolPhase(pool())).toEqual({ text: 'not observed', tone: 'neutral' });
	expect(
		poolPhase(
			pool({ observed: { phase: 'Cluster in healthy state', instances: 2, ready_instances: 2 } })
		)
	).toEqual({ text: 'healthy · 2/2 ready', tone: 'success' });
	expect(
		poolPhase(pool({ observed: { phase: 'Upgrading cluster', instances: 2, ready_instances: 1 } }))
	).toEqual({ text: 'upgrading cluster · 1/2 ready', tone: 'warning' });
});

test('restart keys come from the budget and from changed restart overrides', () => {
	const p = pool({
		parameters: {
			effective: {},
			overrides: { max_connections: '200', work_mem: '64MB' },
			restart_keys: ['max_connections', 'shared_buffers'],
			allowed: []
		}
	});
	expect(restartRequired(p, false, { max_connections: '200', work_mem: '32MB' })).toEqual([]);
	expect(restartRequired(p, true, { max_connections: '200' })).toEqual(['shared_buffers']);
	expect(restartRequired(p, false, { max_connections: '300' })).toEqual(['max_connections']);
	expect(restartRequired(p, true, {})).toEqual(['max_connections', 'shared_buffers']);
});

test('override maps compare by key and value', () => {
	expect(overridesEqual({}, {})).toBe(true);
	expect(overridesEqual({ a: '1' }, { a: '1' })).toBe(true);
	expect(overridesEqual({ a: '1' }, { a: '2' })).toBe(false);
	expect(overridesEqual({ a: '1' }, {})).toBe(false);
});
